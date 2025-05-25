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

const maxInflightMessages = 50
const resendIntervalDuration = 10 * time.Second
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

type Session struct {
	Handler Handler
	Dialer  WebsocketDialer
	Log     *slog.Logger

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

		// Resend the next unacknowledged message if the timeout has passed
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
	incomingSequenceId := int64(0)

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
			err := s.sendAcknowledgeMessage(ctx, msg)
			if err != nil {
				s.Log.Error("Failed to send acknowledgement", "sequence", msg.SequenceNumber, "error", err)
			}

			// We have two choices if the receiver is not keepig up with the packets.
			// We can either drop incoming data messages or we can stop reading new
			// packets until they catch up. This implementation stops everything.
			// It might be better to just drop the packets instead. This would
			if incomingSequenceId == msg.SequenceNumber {
				select {
				case s.incomingDataMessages <- msg:
					incomingSequenceId += 1
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			} else {
				s.Log.Debug("Unexpected Sequence Number", "ExpectedSequence", incomingSequenceId, "ActualSequence", msg.SequenceNumber)
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
