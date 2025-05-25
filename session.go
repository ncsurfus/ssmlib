package ssmlib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/ncsurfus/ssmlib/handler"
	"github.com/ncsurfus/ssmlib/messages"
	"golang.org/x/sync/errgroup"
)

var (
	ErrWebsocketDialFailed        = errors.New("failed to dial ssm websocket url")
	ErrOpenDataChannelFailed      = errors.New("failed to open data channel")
	ErrRemoteSSMClosedPipe        = errors.New("remote ssm closed channel")
	ErrCreateAcknowledgmentFailed = errors.New("failed to create acknowledgment")
	ErrQueueAcknowledgmentFailed  = errors.New("failed to queue acknowledgment")
	ErrSessionStopped             = errors.New("session is stopped")
	ErrAckTimeout                 = errors.New("remote did not ack message before timeout")
	ErrReadWebsocketMessageFailed = errors.New("failed to get next message")
	ErrUnmarshalMessageFailed     = errors.New("failed to unmarshal message")
	ErrSendAcknowledgmentFailed   = errors.New("failed to send acknowledgment")
	errSessionStopRequested       = errors.New("session stopped gracefully")
)

const maxOutOfOrderMessages = 50
const maxInflightMessages = 50
const resendIntervalDuration = 1 * time.Second
const resendTimeoutDuration = 1 * time.Minute
const clientVersion = "1.2.0.0"

type WebsocketDialer interface {
	Dial(url string) (WebsocketConection, error)
}

type WebsocketDialerFunc func(url string) (WebsocketConection, error)

func (wdf WebsocketDialerFunc) Dial(url string) (WebsocketConection, error) {
	return wdf(url)
}

type WebsocketConection interface {
	ReadMessage() (MessageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// Session manages an AWS Systems Manager (SSM) session connection over WebSocket.
// It handles the bidirectional communication protocol including message acknowledgments,
// sequencing, and retransmission. The Session coordinates with a Handler to process
// the actual data stream (e.g., shell commands, port forwarding).
type Session struct {
	// Handler processes the data stream for this session (e.g., Stream, MuxPortForward)
	Handler Handler
	// Dialer establishes the WebSocket connection to the SSM service
	Dialer WebsocketDialer
	// Log provides structured logging for session events
	Log *slog.Logger

	// Messages that are queued to be sent and do not need
	// acknowledgments.
	outgoingControlMessages chan *messages.AgentMessage

	// Messages that are queued to be sent, but cannot be sent
	// due to a full buffer.
	outgoingMessages chan *messages.AgentMessage

	// A channel that is used to indicate that a message has been
	// acknowledged and can be removed from the sent messages.
	ackReceived chan int64

	// Messages that have been received and we've sent acknowledgements for.
	incomingDataMessages chan *messages.AgentMessage

	errgrp *errgroup.Group
	errctx context.Context

	stopped  chan struct{}
	stopOnce sync.Once
	initOnce sync.Once
}

func (s *Session) init() {
	s.initOnce.Do(func() {
		// TODO: Make the queue sizes configurable
		s.outgoingControlMessages = make(chan *messages.AgentMessage, 20)
		s.outgoingMessages = make(chan *messages.AgentMessage, 20)
		s.ackReceived = make(chan int64, 20)
		s.incomingDataMessages = make(chan *messages.AgentMessage, 20)
		s.stopped = make(chan struct{})
		s.errgrp, s.errctx = errgroup.WithContext(context.Background())

		if s.Handler == nil {
			s.Handler = &NopHandler{}
		}

		if s.Dialer == nil {
			s.Dialer = WebsocketDialerFunc(func(url string) (WebsocketConection, error) {
				ws, _, err := websocket.DefaultDialer.Dial(url, http.Header{})
				return ws, err
			})
		}

		if s.Log == nil {
			s.Log = slog.New(handler.DiscardHandler)
		}
	})
}

func (s *Session) signalStop() {
	s.stopOnce.Do(func() { close(s.stopped) })
}

// Start establishes the SSM session connection and begins processing messages.
// It dials the WebSocket connection, performs the initial handshake, and starts
// background goroutines to handle message processing. The method returns after
// the Handler has been successfully started, but the session continues running
// in the background until Stop() is called or an error occurs.
//
// Parameters:
//   - ctx: Context for the handler startup (not used for the session lifetime)
//   - streamURL: The WebSocket URL for the SSM session
//   - tokenValue: Authentication token for the SSM session
//
// Returns an error if the connection cannot be established or the handler fails to start.
func (s *Session) Start(ctx context.Context, streamURL string, tokenValue string) error {
	s.init()

	s.Log.Debug("Dialing StreamURL")
	ws, err := s.Dialer.Dial(streamURL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWebsocketDialFailed, err)
	}

	s.Log.Debug("Opening DataChannel")
	err = s.openDataChannel(ws, tokenValue)
	if err != nil {
		wErr := ws.Close()
		if wErr != nil {
			return fmt.Errorf("%w: %w: %w", wErr, ErrOpenDataChannelFailed, err)
		}
		return fmt.Errorf("%w: %w", ErrOpenDataChannelFailed, err)
	}

	// We should note that we should never return nil from the errgroup, except
	// when the errctx is Done()

	// Cleanup resources
	s.errgrp.Go(func() error {
		<-s.errctx.Done()
		if err := ws.Close(); err != nil {
			s.Log.Debug("websocket failed to close!", "error", err)
		}
		s.Handler.Stop()
		s.signalStop()
		return nil
	})

	// Handle a Stop() event.
	s.errgrp.Go(func() error {
		<-s.stopped
		return errSessionStopRequested
	})

	// Handle messages being sent
	s.errgrp.Go(func() error {
		return s.handleOutgoingMessages(s.errctx, ws)
	})

	// Handle messages coming the remote SSM session.
	s.errgrp.Go(func() error {
		return s.handleIncomingMessages(s.errctx, ws)
	})

	// Ensure the handler starts properly. If it fails, then ensure everything
	// is shut down, before Start returns.
	s.Log.Info("Starting session handler")
	err = s.Handler.Start(ctx, s)
	if err != nil {
		s.Log.Info("Starting handler failed!", "error", err)
		s.errgrp.Go(func() error {
			return fmt.Errorf("handler failed to start: %w", err)
		})
		s.Log.Info("Waiting for session to shutdown due to session handler start failure.")
		return s.errgrp.Wait()
	}

	// Ensure that if the handler stops, everything else exits too. If the session stops
	// then the cleanup goroutine will have already signaled for it to stop.
	s.errgrp.Go(func() error {
		s.Log.Debug("Starting session handler.")
		defer s.Log.Debug("Session handler has stopped.")
		err := s.Handler.Wait(context.Background())
		s.Log.Debug("Session handler failed with error: %w", "error", err)
		return err
	})

	return nil
}

func (s *Session) openDataChannel(socket WebsocketConection, tokenValue string) error {
	openDataChannelMessage := map[string]string{
		"MessageSchemaVersion": "1.0",
		"RequestId":            uuid.NewString(),
		"TokenValue":           tokenValue,
		"ClientId":             uuid.NewString(),
		"ClientVersion":        clientVersion,
	}
	payload, err := json.Marshal(openDataChannelMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal handshake message: %w", err)
	}

	err = socket.WriteMessage(websocket.TextMessage, payload)
	if err != nil {
		return fmt.Errorf("failed to write handshake message: %w", err)
	}

	return nil
}

// Stop initiates a graceful shutdown of the session by sending a terminate message.
// This method does NOT wait for the session to fully shutdown - it only signals
// the shutdown request. The session and its background goroutines may continue
// running after Stop() returns. Use Wait() to block until the session has
// completely stopped.
//
// Stop() is safe to call multiple times and from multiple goroutines.
func (s *Session) Stop() {
	s.init()
	defer s.signalStop()

	// Attempt to gracefully close, but don't become hung in the process.
	select {
	case s.outgoingMessages <- messages.NewTerminateSessionMessage():
		return
	default:
		return
	}
}

// Wait blocks until the session has completely stopped or the context is cancelled.
// If the context is cancelled, Wait returns immediately with the context error,
// but the session and its background goroutines may still be running. This means
// that if the context cancels, the session/handler could still be active.
//
// Wait should be called after Start() to block until the session terminates.
// It returns nil if the session stopped gracefully, or an error if it stopped
// due to a failure.
func (s *Session) Wait(ctx context.Context) error {
	s.init()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.errctx.Done():
		err := s.errgrp.Wait()
		if err != errSessionStopRequested {
			return err
		}
		return nil
	}
}

// Read retrieves the next incoming data message from the SSM session.
// This method blocks until a message is available, the context is cancelled,
// or the session has stopped. Only data messages are returned through this
// method - control messages (acknowledgments, etc.) are handled internally.
//
// Returns ErrSessionStopped if the session has been stopped.
func (s *Session) Read(ctx context.Context) (*messages.AgentMessage, error) {
	s.init()

	select {
	case message := <-s.incomingDataMessages:
		return message, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.errctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrSessionStopped, context.Cause(s.errctx))
	}
}

// Write sends a data message through the SSM session.
// The message will be queued for transmission with proper sequencing and
// acknowledgment handling. This method blocks until the message is queued,
// the context is cancelled, or the session has stopped.
//
// Returns ErrSessionStopped if the session has been stopped.
func (s *Session) Write(ctx context.Context, message *messages.AgentMessage) error {
	s.init()

	select {
	case s.outgoingMessages <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.errctx.Done():
		return fmt.Errorf("%w: %w", ErrSessionStopped, context.Cause(s.errctx))
	}
}

func (s *Session) handleOutgoingMessages(ctx context.Context, socket WebsocketConection) error {
	// TODO: It might be good to keep this state in a struct, separate from the method. That way
	// if we reconnect to the websocket, we can pickup exactly where we left off.
	nextOutgoingSequenceNumber := int64(0)
	unacknowledgedMessages := map[int64]*messages.AgentMessage{}

	resendSequenceNumber := int64(0)
	resendTimer := (<-chan time.Time)(nil)
	resendTimeout := (<-chan time.Time)(nil)

	for {
		// If we can't send any more messages, then we don't want to pull any
		// messages from the outgoing message channel. We can accomplish this
		// by simply setting the channel to nil.
		var outgoingMessages chan *messages.AgentMessage
		if len(unacknowledgedMessages) < maxInflightMessages {
			outgoingMessages = s.outgoingMessages
		} else {
			s.Log.Debug("Waiting on remote to acknowledge messages before sending more")
		}

		select {
		case seqNumber := <-s.ackReceived:
			s.Log.Debug("Received acknowledgment", "sequenceNumber", seqNumber)
			delete(unacknowledgedMessages, seqNumber)

			if len(unacknowledgedMessages) == 0 {
				s.Log.Debug("Remote has acknowledged all messages")
				resendTimer, resendTimeout = nil, nil
			} else {
				// Get the lowest unacknowledged message number.
				// It may not be the next number if the acknowledgments
				// came out of order.
				resendSequenceNumber = math.MaxInt64
				for num := range unacknowledgedMessages {
					resendSequenceNumber = min(resendSequenceNumber, num)
				}
				resendTimer = time.Tick(resendIntervalDuration)
				resendTimeout = time.After(resendTimeoutDuration)
			}

		case controlMessage := <-s.outgoingControlMessages:
			s.Log.Debug("Sending control message", "messageType", controlMessage.MessageType)
			err := s.writeMessage(controlMessage, socket)
			if err != nil {
				// This isn't good, but if we fail to acknowledge something, then the sender
				// would just resend it again anyway.
				s.Log.Error("failed to send control message", "messageType", controlMessage.MessageType)
			}

		case message := <-outgoingMessages:
			s.Log.Debug("Sending message to remote", "messageType", message.MessageType, "sequenceNumber", nextOutgoingSequenceNumber)
			message.SequenceNumber = nextOutgoingSequenceNumber
			unacknowledgedMessages[nextOutgoingSequenceNumber] = message
			if message.SequenceNumber == 0 && message.Flags != messages.Fin {
				message.Flags = messages.Syn
			}

			err := s.writeMessage(message, socket)
			if err != nil {
				// This isn't good, but if we fail to send something, then we wont get an acknowledgment
				// and we'd just resend it anyway. If everything starts failing, then we'd fill up our unacknowledged
				// message buffer, and the resend timer would resolve an intermittent issue, or it would error out.
				//
				// TODO: Perhaps we should store any error messages, so that way the error can bubble back up
				// in resendTimeout....
				s.Log.Error("failed to send message", "messageType", message.MessageType, "error", err)
			}
			nextOutgoingSequenceNumber += 1

			// Don't send things too fast, otherwise the remote end appears to have a lot of issues keeping up.
			time.Sleep(2 * time.Millisecond)

		// TODO: Store a timestamp with each message, and this can run on an interval
		// inspecting them and then resend relevant ones...
		case <-resendTimer:
			s.Log.Debug("Resending message", "sequenceNumber", resendSequenceNumber)
			message := unacknowledgedMessages[resendSequenceNumber]
			err := s.writeMessage(message, socket)
			if err != nil {
				s.Log.Error("failed to resend message", "messageType", message.MessageType, "error", err)
			}

		case <-resendTimeout:
			s.Log.Error("Remote didn't acknowledge message!", "sequenceNumber", resendSequenceNumber)
			return ErrAckTimeout

		case <-ctx.Done():
			s.Log.Error("Writer stopping due to cancellation", "error", ctx.Err())
			return context.Cause(ctx)
		}
	}
}

func (s *Session) handleIncomingMessages(ctx context.Context, socket WebsocketConection) error {
	// TODO: It might be good to keep this state in a struct, separate from the method. That way
	// if we reconnect to the websocket, we can pickup exactly where we left off.
	outOfOrderMessages := map[int64]*messages.AgentMessage{}
	expectedSequenceId := int64(0)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		data, err := s.readMessage(socket)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrReadWebsocketMessageFailed, err)
		}

		msg := &messages.AgentMessage{}
		if err := msg.UnmarshalBinary(data); err != nil {
			return fmt.Errorf("%w: %w", ErrUnmarshalMessageFailed, err)
		}

		s.Log.Debug("Received Message", "MessageType", msg.MessageType)

		switch msg.MessageType {
		case messages.Acknowledge:
			acknowledgedSequence, err := messages.ParseAcknowledgment(msg)
			if err != nil {
				s.Log.Warn("failed to parse acknowledgement.", "error", err)
			} else {
				select {
				case s.ackReceived <- acknowledgedSequence:
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			}

		// Doesn't appear to actually be used by the ssm plugin.
		case messages.PausePublication:

		// Doesn't appear to actually be used by the ssm plugin.
		case messages.StartPublication:

		// This is handled by the handler that cares about the data stream.
		case messages.OutputStreamData:

			// Send acknowledgments for anything we've already read or the next message we expect.
			if expectedSequenceId >= msg.SequenceNumber {
				err := s.sendAcknowledgeMessage(ctx, msg)
				if err != nil {
					s.Log.Error("Failed to send acknowledgement", "sequence", msg.SequenceNumber, "error", err)
				}
			} else {
				if len(outOfOrderMessages) < maxOutOfOrderMessages {
					outOfOrderMessages[msg.SequenceNumber] = msg
					s.Log.Debug("Queued out of order message", "ExpectedSequence", expectedSequenceId, "ActualSequence", msg.SequenceNumber)
				} else {
					s.Log.Debug("Dropped out of order message", "ExpectedSequence", expectedSequenceId, "ActualSequence", msg.SequenceNumber)
				}
			}

			// We have two choices if the receiver is not keepig up with the packets.
			// We can either drop incoming data messages or we can stop reading new
			// packets until they catch up. This implementation stops everything.
			// It might be better to just drop the packets instead. This would
			if expectedSequenceId == msg.SequenceNumber {
				s.Log.Debug("Received expected sequence number", "ExpectedSequence", expectedSequenceId)
				select {
				case s.incomingDataMessages <- msg:
					expectedSequenceId += 1
				case <-ctx.Done():
					return context.Cause(ctx)
				}

				// Attempt to send out of order messages. It might be better just to put all messages into
				// the map and then we can consolidate this logic to the below logic....
				for {
					if msg, ok := outOfOrderMessages[expectedSequenceId]; ok {
						select {
						case s.incomingDataMessages <- msg:
							s.Log.Debug("Dequeued out of order message", "Sequence", expectedSequenceId)
							delete(outOfOrderMessages, expectedSequenceId)
							expectedSequenceId += 1
						case <-ctx.Done():
							return context.Cause(ctx)
						}
					} else {
						break
					}
				}
			}

		// Remote session is closing the session
		case messages.ChannelClosed:
			return fmt.Errorf("%w: %w", ErrRemoteSSMClosedPipe, io.ErrClosedPipe)

		default:
			s.Log.Warn("Received an unknown message type", "messageType", msg.MessageType)
		}
	}
}

// writeMessage should never be called directly, except for handleOutgoingMessages
func (s *Session) writeMessage(msg *messages.AgentMessage, socket WebsocketConection) error {
	data, err := msg.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal outgoing message: %w", err)
	}

	err = socket.WriteMessage(websocket.BinaryMessage, data)
	if err != nil {
		return fmt.Errorf("failed to send outgoing message: %w", err)
	}

	return nil
}

func (s *Session) readMessage(socket WebsocketConection) ([]byte, error) {
	_, msg, err := socket.ReadMessage()
	if err != nil {
		if websocket.IsCloseError(err, 1000, 1001, 1006) {
			// In the future we should attempt to reconnect!
			return nil, fmt.Errorf("websocket connection is closed: %w", io.EOF)
		}
		return nil, fmt.Errorf("failed to read from websocket: %w", err)
	}
	return msg, nil
}

func (s *Session) sendAcknowledgeMessage(ctx context.Context, incomingMessage *messages.AgentMessage) error {
	ack := map[string]any{
		"AcknowledgedMessageType":           incomingMessage.MessageType,
		"AcknowledgedMessageId":             incomingMessage.MessageID.String(),
		"AcknowledgedMessageSequenceNumber": incomingMessage.SequenceNumber,
		"IsSequentialMessage":               true,
	}

	payload, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCreateAcknowledgmentFailed, err)
	}

	ackMessage := messages.NewAcknowledgementMessage(incomingMessage.SequenceNumber, payload)
	select {
	case s.outgoingControlMessages <- ackMessage:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrQueueAcknowledgmentFailed, ctx.Err())
	}
}
