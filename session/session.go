package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/ncsurfus/ssmlib/messages"
	"golang.org/x/sync/errgroup"
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

func NewSession(streamURL string, tokenValue string) *Session {
	return &Session{
		StreamURL:  streamURL,
		TokenValue: tokenValue,
	}
}

type Session struct {
	StreamURL  string
	TokenValue string
	Dialer     WebsocketDialer
	Log        *slog.Logger

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

		if s.Dialer == nil {
			s.Dialer = WebsocketDialerFunc(func(url string) (WebsocketConection, error) {
				ws, _, err := websocket.DefaultDialer.Dial(url, http.Header{})
				if err != nil {
					return nil, fmt.Errorf("failed to dial websocket stream: %w", err)
				}
				return ws, nil
			})
		}

		if s.Log == nil {
			s.Log = slog.New(slog.DiscardHandler)
		}
	})
}

func (s *Session) signalStop() {
	s.stopOnce.Do(func() { close(s.stopped) })
}

func (s *Session) Start(ctx context.Context) error {
	s.init()

	if s.TokenValue == "" {
		return fmt.Errorf("TokenValue cannot be empty")
	}

	if s.StreamURL == "" {
		return fmt.Errorf("StreamURL cannot be empty")
	}

	ws, err := s.Dialer.Dial(s.StreamURL)
	if err != nil {
		return fmt.Errorf("failed to dial ssm websocket url: %w", err)
	}

	err = s.openDataChannel(ws)
	if err != nil {
		ws.Close()
		return fmt.Errorf("failed to open data channel: %w", err)
	}

	// We should note that we should never return nil from the errgroup, except
	// when the errctx is Done()

	// Cleanup resources
	s.errgrp.Go(func() error {
		<-s.errctx.Done()
		ws.Close()
		s.signalStop()
		return nil
	})

	// Handle a Stop() event.
	s.errgrp.Go(func() error {
		// The cleanup resources goroutine will ensure stop gets closed.
		<-s.stopped
		return io.EOF
	})

	// Handle messages being sent
	s.errgrp.Go(func() error {
		return s.handleOutgoingMessages(s.errctx, ws)
	})

	// Handle messages coming the remote SSM session.
	s.errgrp.Go(func() error {
		return s.handleIncomingMessages(s.errctx, ws)
	})

	return nil
}

func (s *Session) openDataChannel(socket WebsocketConection) error {
	openDataChannelMessage := map[string]string{
		"MessageSchemaVersion": "1.0",
		"RequestId":            uuid.NewString(),
		"TokenValue":           s.TokenValue,
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
		return fmt.Errorf("wait cancelled: %w", ctx.Err())
	case <-s.errctx.Done():
		err := s.errgrp.Wait()
		if err != io.EOF {
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
	case <-s.stopped:
		return nil, fmt.Errorf("session is stopped: %w", io.EOF)
	}
}

func (s *Session) Write(ctx context.Context, message *messages.AgentMessage) error {
	s.init()

	select {
	case s.outgoingMessages <- message:
		return nil
	case <-s.stopped:
		return fmt.Errorf("session is stopped: %w", io.EOF)
	}
}

func (s *Session) handleOutgoingMessages(ctx context.Context, socket WebsocketConection) error {
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
			err := s.writeMessage(message, socket)
			if err != nil {
				// This isn't good, but if we fail to send something, then we wont get an acknowledgment
				// and we'd just resend it anyway.
				s.Log.Error("failed to send message", "messageType", message.MessageType, "error", err)
			}
			nextOutgoingSequenceNumber += 1

		// Resend the next unacknowledged message if the timeout has passed
		case <-resendTimer:
			s.Log.Debug("Resending message", "sequenceNumber", resendSequenceNumber)
			s.writeMessage(unacknowledgedMessages[resendSequenceNumber], socket)

		case <-resendTimeout:
			s.Log.Error("Remote didn't acknowledge message!", "sequenceNumber", resendSequenceNumber)
			return fmt.Errorf("remote did not ack message before timeout")

		case <-ctx.Done():
			s.Log.Error("Writer stopping due to cancellation", "error", ctx.Err())
			return fmt.Errorf("failed to send outgoing messages: %w", ctx.Err())
		}
	}
}

func (s *Session) handleIncomingMessages(ctx context.Context, socket WebsocketConection) error {
	incomingSequenceId := int64(0)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		data, err := s.readMessage(socket)
		if err != nil {
			return fmt.Errorf("failed to get next message: %w", err)
		}

		m := &messages.AgentMessage{}
		if err := m.UnmarshalBinary(data); err != nil {
			return fmt.Errorf("failed to unmarshal message: %w", err)
		}

		switch m.MessageType {
		case messages.Acknowledge:
			select {
			case s.ackReceived <- m.SequenceNumber:
			case <-ctx.Done():
				return fmt.Errorf("failed to send acknowledgment into queue: %w", ctx.Err())
			}

		// Doesn't appear to actually be used by the ssm plugin.
		case messages.PausePublication:

		// Doesn't appear to actually be used by the ssm plugin.
		case messages.StartPublication:

		// This is handled by the handler that cares about the data stream.
		case messages.OutputStreamData:
			err := s.sendAcknowledgeMessage(ctx, m)
			if err != nil {
				s.Log.Error("Failed to send acknowledgement", "sequence", m.SequenceNumber, "error", err)
			}

			// We have two choices if the receiver is not keepig up with the packets.
			// We can either drop incoming data messages or we can stop reading new
			// packets until they catch up. This implementation stops everything.
			// We also don't want to deadlock during a cancellation and the queue
			// being full... a corner case... but it still exists!
			if incomingSequenceId == m.SequenceNumber {
				select {
				case s.incomingDataMessages <- m:
					m.SequenceNumber += 1
				case <-ctx.Done():
					return fmt.Errorf("failed to send data message into queue: %w", ctx.Err())
				}
			}
			return nil

		// Remote session is closing the session
		case messages.ChannelClosed:
			return fmt.Errorf("remote caller closed channel: %w", io.EOF)

		default:
			s.Log.Warn("Received an unknown message type", "messageType", m.MessageType)
			return nil
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
		return fmt.Errorf("failed to create acknowledgment: %w", err)
	}

	ackMessage := messages.NewAcknowledgementMessage(incomingMessage.SequenceNumber, payload)
	select {
	case s.outgoingControlMessages <- ackMessage:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("failed to queue acknowledgment: %w", ctx.Err())
	}
}
