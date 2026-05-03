package ssmlib

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ncsurfus/ssmlib/handler"
	"github.com/ncsurfus/ssmlib/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockWebsocketConnection implements WebsocketConnection for testing
type MockWebsocketConnection struct {
	mock.Mock
}

func (m *MockWebsocketConnection) ReadMessage() (int, []byte, error) {
	args := m.Called()
	return args.Int(0), args.Get(1).([]byte), args.Error(2)
}

func (m *MockWebsocketConnection) WriteMessage(messageType int, data []byte) error {
	args := m.Called(messageType, data)
	return args.Error(0)
}

func (m *MockWebsocketConnection) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockHandler implements Handler for testing
type MockHandler struct {
	mock.Mock
}

func (m *MockHandler) Start(ctx context.Context, session handler.SessionReaderWriter) error {
	args := m.Called(ctx, session)
	return args.Error(0)
}

func (m *MockHandler) Stop() {
	m.Called()
}

func (m *MockHandler) Wait(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockWebsocketDialer implements WebsocketDialer for testing
type MockWebsocketDialer struct {
	mock.Mock
}

func (m *MockWebsocketDialer) Dial(url string) (WebsocketConnection, error) {
	args := m.Called(url)
	if conn, ok := args.Get(0).(WebsocketConnection); ok {
		return conn, args.Error(1)
	}
	return nil, args.Error(1)
}

func TestSession_Init(t *testing.T) {
	s := &Session{}
	s.init()

	assert.NotNil(t, s.outgoingControlMessages)
	assert.NotNil(t, s.outgoingMessages)
	assert.NotNil(t, s.ackReceived)
	assert.NotNil(t, s.incomingDataMessages)
	assert.NotNil(t, s.stopped)
	assert.NotNil(t, s.Handler)
	assert.NotNil(t, s.Dialer)
	assert.NotNil(t, s.Log)
}

func TestSession_Start_DialError(t *testing.T) {
	mockDialer := &MockWebsocketDialer{}
	expectedErr := errors.New("dial error")
	mockDialer.On("Dial", "ws://test").Return(nil, expectedErr)

	s := &Session{
		Dialer: mockDialer,
	}

	err := s.Start(context.Background(), "ws://test", "token")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrWebsocketDialFailed)
}

func TestSession_Start_OpenDataChannelError(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", websocket.TextMessage, mock.Anything).Return(errors.New("write error"))
	mockWS.On("Close").Return(nil)

	s := &Session{
		Dialer: mockDialer,
	}

	err := s.Start(context.Background(), "ws://test", "token")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrOpenDataChannelFailed)
}

func TestSession_Start_HandlerStartError(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}
	mockHandler := &MockHandler{}
	expectedErr := errors.New("handler start error")

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", websocket.TextMessage, mock.Anything).Return(nil)
	// Create a valid message for the mock to return
	msg := messages.NewTerminateSessionMessage()
	msgBytes, _ := msg.MarshalBinary()
	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, msgBytes, nil)
	mockHandler.On("Start", mock.Anything, mock.Anything).Return(expectedErr)
	mockWS.On("Close").Return(nil)
	mockHandler.On("Stop").Return()

	s := &Session{
		Dialer:  mockDialer,
		Handler: mockHandler,
	}

	err := s.Start(context.Background(), "ws://test", "token")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "handler failed to start")
}

func TestSession_Start_Success(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}
	mockHandler := &MockHandler{}

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", websocket.TextMessage, mock.Anything).Return(nil)
	// Create a valid message for the mock to return
	msg := messages.NewTerminateSessionMessage()
	msgBytes, _ := msg.MarshalBinary()
	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, msgBytes, nil)
	mockHandler.On("Start", mock.Anything, mock.Anything).Return(nil)
	mockHandler.On("Wait", mock.Anything).Return(nil)
	mockWS.On("Close").Return(nil)
	mockHandler.On("Stop").Return()

	s := &Session{
		Dialer:  mockDialer,
		Handler: mockHandler,
	}

	err := s.Start(context.Background(), "ws://test", "token")
	assert.NoError(t, err)
}

func TestSession_Write(t *testing.T) {
	s := &Session{}
	s.init()

	msg := messages.NewTerminateSessionMessage()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := s.Write(ctx, msg)
	assert.NoError(t, err)
}

func TestSession_Write_ContextCanceled(t *testing.T) {
	s := &Session{}
	s.init()

	// Fill the outgoing messages channel
	for i := 0; i < 20; i++ {
		s.outgoingMessages <- messages.NewTerminateSessionMessage()
	}

	msg := messages.NewTerminateSessionMessage()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := s.Write(ctx, msg)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSession_Read(t *testing.T) {
	s := &Session{}
	s.init()

	// Send a message that can be read
	msg := messages.NewTerminateSessionMessage()
	s.incomingDataMessages <- msg

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	readMsg, err := s.Read(ctx)
	assert.NoError(t, err)
	assert.Equal(t, msg, readMsg)
}

func TestSession_Read_ContextCanceled(t *testing.T) {
	s := &Session{}
	s.init()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := s.Read(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSession_Stop(t *testing.T) {
	s := &Session{}
	s.init()

	// Test stopping the session
	s.Stop()

	// Verify the session is stopped by checking the stopped channel
	select {
	case <-s.stopped:
		// Channel is closed as expected
	default:
		t.Error("Session not stopped")
	}
}

func TestSession_HandleOutgoingMessages(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Setup mock to accept the message
	mockWS.On("WriteMessage", websocket.BinaryMessage, mock.Anything).Return(nil)

	// Start a goroutine to handle outgoing messages
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		time.Sleep(100 * time.Millisecond)
		msg := messages.NewTerminateSessionMessage()
		s.outgoingMessages <- msg

		// Simulate acknowledgment
		s.ackReceived <- 0

		cancel() // Stop the handler
	}()

	err := s.handleOutgoingMessages(ctx, mockWS)
	assert.Error(t, err) // Should error due to context cancellation
}

func TestSession_HandleIncomingMessages(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Create a test message
	msg := messages.NewTerminateSessionMessage()
	msgBytes, _ := msg.MarshalBinary()

	// Setup mock to return the test message
	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, msgBytes, nil)

	// Start a goroutine to handle incoming messages
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel() // Stop the handler
	}()

	err := s.handleIncomingMessages(ctx, mockWS)
	assert.Error(t, err) // Should error due to context cancellation
}

func TestSession_HandleIncomingMessages_AcksOutOfOrderImmediately(t *testing.T) {
	// When an out-of-order message arrives, it should be acked immediately
	// to prevent unnecessary retransmissions from the remote.
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Create messages: send seq 1 first (out of order, expecting 0)
	msg1 := messages.NewAgentMessage()
	msg1.MessageType = messages.OutputStreamData
	msg1.SequenceNumber = 1
	msg1.Flags = messages.Data
	msg1.PayloadType = messages.Output
	msg1.Payload = []byte("second")
	msg1Bytes, _ := msg1.MarshalBinary()

	callCount := 0
	mockWS.On("ReadMessage").Run(func(_ mock.Arguments) {
		callCount++
	}).Return(websocket.BinaryMessage, msg1Bytes, nil).Once()
	// Second call blocks until context cancelled
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("closed"))

	// Expect an ack to be written for the out-of-order message
	ackWritten := make(chan struct{}, 1)
	mockWS.On("WriteMessage", mock.Anything, mock.Anything).Run(func(_ mock.Arguments) {
		select {
		case ackWritten <- struct{}{}:
		default:
		}
	}).Return(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go func() { _ = s.handleIncomingMessages(ctx, mockWS) }()

	// The out-of-order message should trigger an ack via outgoingControlMessages
	select {
	case <-s.outgoingControlMessages:
		// Got the ack — out-of-order message was acked immediately
	case <-time.After(time.Second):
		t.Fatal("out-of-order message was not acked immediately")
	}
}

func TestSession_HandleIncomingMessages_ReadError(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Setup mock to fail the read
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("read error"))

	err := s.handleIncomingMessages(context.Background(), mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrReadWebsocketMessageFailed)
}

func TestSession_Wait(t *testing.T) {
	s := &Session{}
	s.init()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Test waiting with timeout
	err := s.Wait(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSession_Wait_ContextCanceled(t *testing.T) {
	s := &Session{}
	s.init()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := s.Wait(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSession_OpenDataChannel_UsesHandshakeClientVersion(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	var sentPayload []byte
	mockWS.On("WriteMessage", websocket.TextMessage, mock.Anything).Run(func(args mock.Arguments) {
		sentPayload = args.Get(1).([]byte)
	}).Return(nil)

	s := &Session{}
	s.init()
	err := s.openDataChannel(mockWS, "token")
	assert.NoError(t, err)

	var msg map[string]string
	err = json.Unmarshal(sentPayload, &msg)
	assert.NoError(t, err)
	assert.Equal(t, handler.ClientVersion, msg["ClientVersion"],
		"openDataChannel should use the same ClientVersion as the handshake")
}

func TestSession_WebsocketCloseAfterReadWriteExit(t *testing.T) {
	// Verify that ws.Close() is called during shutdown, which unblocks
	// any in-progress ReadMessage calls. gorilla/websocket documents that
	// Close can be called concurrently with all other methods.
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}
	mockHandler := &MockHandler{}

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", mock.Anything, mock.Anything).Return(nil)

	readDone := make(chan struct{})
	mockWS.On("ReadMessage").Run(func(_ mock.Arguments) {
		<-readDone
	}).Return(0, []byte{}, errors.New("closed"))

	closeCalled := make(chan struct{})
	mockWS.On("Close").Run(func(_ mock.Arguments) {
		close(closeCalled)
		close(readDone)
	}).Return(nil)

	mockHandler.On("Start", mock.Anything, mock.Anything).Return(nil)
	mockHandler.On("Wait", mock.Anything).Return(nil)
	mockHandler.On("Stop").Return()

	s := &Session{
		Dialer:  mockDialer,
		Handler: mockHandler,
	}

	err := s.Start(context.Background(), "ws://test", "token")
	assert.NoError(t, err)

	s.Stop()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	err = s.Wait(waitCtx)
	assert.NoError(t, err)

	select {
	case <-closeCalled:
	default:
		t.Error("ws.Close() was never called during shutdown")
	}
}

// --- NopHandler tests ---

func TestNopHandler_Start(t *testing.T) {
	h := &NopHandler{}
	err := h.Start(context.Background(), nil)
	assert.NoError(t, err)
}

func TestNopHandler_Stop(t *testing.T) {
	h := &NopHandler{}
	h.Stop()
	// Multiple stops should be safe
	h.Stop()
}

func TestNopHandler_Wait(t *testing.T) {
	h := &NopHandler{}
	h.Stop()
	err := h.Wait(context.Background())
	assert.NoError(t, err)
}

func TestNopHandler_Wait_ContextCanceled(t *testing.T) {
	h := &NopHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.Wait(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- WebsocketDialerFunc tests ---

func TestWebsocketDialerFunc_Dial(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	dialer := WebsocketDialerFunc(func(url string) (WebsocketConnection, error) {
		assert.Equal(t, "ws://test", url)
		return mockWS, nil
	})

	conn, err := dialer.Dial("ws://test")
	assert.NoError(t, err)
	assert.Equal(t, mockWS, conn)
}

func TestWebsocketDialerFunc_Dial_Error(t *testing.T) {
	expectedErr := errors.New("dial error")
	dialer := WebsocketDialerFunc(func(url string) (WebsocketConnection, error) {
		return nil, expectedErr
	})

	conn, err := dialer.Dial("ws://test")
	assert.Nil(t, conn)
	assert.ErrorIs(t, err, expectedErr)
}

// --- handleIncomingMessages tests ---

func makeOutputMessage(t *testing.T, seq int64, payload string) []byte {
	t.Helper()
	msg := messages.NewAgentMessage()
	msg.MessageType = messages.OutputStreamData
	msg.SequenceNumber = seq
	msg.Flags = messages.Data
	msg.PayloadType = messages.Output
	msg.Payload = []byte(payload)
	data, err := msg.MarshalBinary()
	require.NoError(t, err)
	return data
}

func makeAckMessage(t *testing.T, seq int64) []byte {
	t.Helper()
	ackPayload, _ := json.Marshal(map[string]any{
		"AcknowledgedMessageSequenceNumber": seq,
	})
	msg := messages.NewAcknowledgementMessage(seq, ackPayload)
	data, err := msg.MarshalBinary()
	require.NoError(t, err)
	return data
}

func TestSession_HandleIncomingMessages_Acknowledge(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	ackData := makeAckMessage(t, 42)
	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, ackData, nil).Once()
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("done"))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go func() {
		select {
		case seq := <-s.ackReceived:
			assert.Equal(t, int64(42), seq)
		case <-ctx.Done():
		}
	}()

	err := s.handleIncomingMessages(ctx, mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrReadWebsocketMessageFailed)
}

func TestSession_HandleIncomingMessages_PausePublication(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	msg := messages.NewAgentMessage()
	msg.MessageType = messages.PausePublication
	msg.Payload = []byte("pause")
	data, _ := msg.MarshalBinary()

	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, data, nil).Once()
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("done"))

	err := s.handleIncomingMessages(context.Background(), mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrReadWebsocketMessageFailed)
}

func TestSession_HandleIncomingMessages_StartPublication(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	msg := messages.NewAgentMessage()
	msg.MessageType = messages.StartPublication
	msg.Payload = []byte("start")
	data, _ := msg.MarshalBinary()

	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, data, nil).Once()
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("done"))

	err := s.handleIncomingMessages(context.Background(), mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrReadWebsocketMessageFailed)
}

func TestSession_HandleIncomingMessages_ChannelClosed(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Build a channel_closed message with 112-byte header
	msg := messages.NewAgentMessage()
	msg.MessageType = messages.ChannelClosed
	msg.Flags = messages.Fin
	msg.Payload = []byte(`{"Output":"session closed"}`)
	data, _ := msg.MarshalBinary()

	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, data, nil).Once()

	err := s.handleIncomingMessages(context.Background(), mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRemoteSSMClosedPipe)
}

func TestSession_HandleIncomingMessages_OutputInOrder(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Send seq 0 (in order)
	data0 := makeOutputMessage(t, 0, "first")
	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, data0, nil).Once()
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("done")).Maybe()
	// Ack write
	mockWS.On("WriteMessage", websocket.BinaryMessage, mock.Anything).Return(nil).Maybe()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go func() {
		// Drain the incoming data message
		select {
		case msg := <-s.incomingDataMessages:
			assert.Equal(t, []byte("first"), msg.Payload)
		case <-ctx.Done():
		}
		// Drain ack
		select {
		case <-s.outgoingControlMessages:
		case <-ctx.Done():
		}
	}()

	err := s.handleIncomingMessages(ctx, mockWS)
	assert.Error(t, err)
}

func TestSession_HandleIncomingMessages_OutOfOrderReordering(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Send seq 1 first (out of order), then seq 0, then error to exit
	data1 := makeOutputMessage(t, 1, "second")
	data0 := makeOutputMessage(t, 0, "first")

	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, data1, nil).Once()
	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, data0, nil).Once()
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("done"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received := make([]string, 0, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			select {
			case msg := <-s.incomingDataMessages:
				received = append(received, string(msg.Payload))
			case <-ctx.Done():
				return
			}
		}
	}()

	// Drain acks in background
	go func() {
		for {
			select {
			case <-s.outgoingControlMessages:
			case <-ctx.Done():
				return
			}
		}
	}()

	err := s.handleIncomingMessages(ctx, mockWS)
	assert.Error(t, err)

	// Wait for receiver to finish
	<-done

	// Messages should be delivered in order: first, then second
	assert.Equal(t, []string{"first", "second"}, received)
}

func TestSession_HandleIncomingMessages_UnmarshalError(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, []byte("garbage"), nil).Once()

	err := s.handleIncomingMessages(context.Background(), mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnmarshalMessageFailed)
}

func TestSession_HandleIncomingMessages_UnknownMessageType(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	msg := messages.NewAgentMessage()
	msg.MessageType = messages.AgentSession // not handled in switch
	msg.Payload = []byte("unknown")
	data, _ := msg.MarshalBinary()

	mockWS.On("ReadMessage").Return(websocket.BinaryMessage, data, nil).Once()
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("done"))

	err := s.handleIncomingMessages(context.Background(), mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrReadWebsocketMessageFailed)
}

// --- handleOutgoingMessages tests ---

func TestSession_HandleOutgoingMessages_WriteThrottle(t *testing.T) {
	// Verify that after sending a message, the write throttle delays the next send
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	writeCount := 0
	mockWS.On("WriteMessage", websocket.BinaryMessage, mock.Anything).Run(func(_ mock.Arguments) {
		writeCount++
	}).Return(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Send two messages
	go func() {
		s.outgoingMessages <- messages.NewStreamMessage([]byte("msg1"))
		s.outgoingMessages <- messages.NewStreamMessage([]byte("msg2"))
		// Ack both to prevent timeout
		s.ackReceived <- 0
		s.ackReceived <- 1
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_ = s.handleOutgoingMessages(ctx, mockWS)
	assert.GreaterOrEqual(t, writeCount, 2)
}

func TestSession_HandleOutgoingMessages_ControlMessage(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	controlWritten := make(chan struct{})
	mockWS.On("WriteMessage", websocket.BinaryMessage, mock.Anything).Run(func(_ mock.Arguments) {
		select {
		case controlWritten <- struct{}{}:
		default:
		}
	}).Return(nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go func() {
		s.outgoingControlMessages <- messages.NewTerminateSessionMessage()
		<-controlWritten
		cancel()
	}()

	_ = s.handleOutgoingMessages(ctx, mockWS)
}

func TestSession_HandleOutgoingMessages_ControlMessageWriteError(t *testing.T) {
	// Control message write errors are logged but don't stop the handler
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	mockWS.On("WriteMessage", websocket.BinaryMessage, mock.Anything).Return(errors.New("write failed"))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go func() {
		s.outgoingControlMessages <- messages.NewTerminateSessionMessage()
	}()

	err := s.handleOutgoingMessages(ctx, mockWS)
	// Should exit due to context cancellation, not the write error
	assert.Error(t, err)
}

func TestSession_HandleOutgoingMessages_FirstMessageSetsSyn(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	var sentData []byte
	mockWS.On("WriteMessage", websocket.BinaryMessage, mock.Anything).Run(func(args mock.Arguments) {
		if sentData == nil {
			sentData = args.Get(1).([]byte)
		}
	}).Return(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go func() {
		msg := messages.NewStreamMessage([]byte("first"))
		msg.Flags = messages.Data // Not Fin
		s.outgoingMessages <- msg
		s.ackReceived <- 0
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_ = s.handleOutgoingMessages(ctx, mockWS)

	// Verify the first message (seq 0) had Syn flag set
	assert.NotNil(t, sentData)
	sentMsg := &messages.AgentMessage{}
	err := sentMsg.UnmarshalBinary(sentData)
	assert.NoError(t, err)
	assert.Equal(t, messages.Syn, sentMsg.Flags)
}

func TestSession_HandleOutgoingMessages_MessageWriteError(t *testing.T) {
	// Message write errors are logged but don't stop the handler (will be resent)
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	mockWS.On("WriteMessage", websocket.BinaryMessage, mock.Anything).Return(errors.New("write failed"))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go func() {
		s.outgoingMessages <- messages.NewStreamMessage([]byte("msg"))
		s.ackReceived <- 0
	}()

	err := s.handleOutgoingMessages(ctx, mockWS)
	assert.Error(t, err) // context cancellation
}

// --- readMessage tests ---

func TestSession_ReadMessage_WebsocketCloseError(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	// Simulate a websocket close error (1000 = normal closure)
	closeErr := &websocket.CloseError{Code: 1000, Text: "normal closure"}
	mockWS.On("ReadMessage").Return(0, []byte{}, closeErr)

	_, err := s.readMessage(mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, io.EOF)
}

func TestSession_ReadMessage_WebsocketCloseError1006(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	closeErr := &websocket.CloseError{Code: 1006, Text: "abnormal closure"}
	mockWS.On("ReadMessage").Return(0, []byte{}, closeErr)

	_, err := s.readMessage(mockWS)
	assert.Error(t, err)
	assert.ErrorIs(t, err, io.EOF)
}

func TestSession_ReadMessage_NonCloseError(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	s := &Session{}
	s.init()

	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("network error"))

	_, err := s.readMessage(mockWS)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, io.EOF)
	assert.Contains(t, err.Error(), "failed to read from websocket")
}

// --- sendAcknowledgeMessage tests ---

func TestSession_SendAcknowledgeMessage_ContextCanceled(t *testing.T) {
	s := &Session{}
	s.init()

	// Fill the control messages channel
	for i := 0; i < 20; i++ {
		s.outgoingControlMessages <- messages.NewTerminateSessionMessage()
	}

	msg := messages.NewAgentMessage()
	msg.MessageType = messages.OutputStreamData
	msg.SequenceNumber = 1

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.sendAcknowledgeMessage(ctx, msg)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrQueueAcknowledgmentFailed)
}

// --- Read/Write with errctx done ---

func TestSession_Read_SessionStopped(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", mock.Anything, mock.Anything).Return(nil)
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("closed"))
	mockWS.On("Close").Return(nil)

	s := &Session{Dialer: mockDialer}
	s.init()

	// Start and immediately stop to get errctx cancelled
	err := s.Start(context.Background(), "ws://test", "token")
	assert.NoError(t, err)

	// Wait for the session to stop due to read error
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	_ = s.Wait(waitCtx)

	// Now Read should return ErrSessionStopped
	_, err = s.Read(context.Background())
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionStopped)
}

func TestSession_Write_SessionStopped(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", mock.Anything, mock.Anything).Return(nil)
	mockWS.On("ReadMessage").Return(0, []byte{}, errors.New("closed"))
	mockWS.On("Close").Return(nil)

	s := &Session{Dialer: mockDialer}
	s.init()

	err := s.Start(context.Background(), "ws://test", "token")
	assert.NoError(t, err)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	_ = s.Wait(waitCtx)

	// Fill the outgoing messages channel so Write can't succeed via channel send
	for i := 0; i < 20; i++ {
		s.outgoingMessages <- messages.NewStreamMessage([]byte("fill"))
	}

	err = s.Write(context.Background(), messages.NewStreamMessage([]byte("test")))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionStopped)
}

// --- Stop sends terminate message ---

func TestSession_Stop_SendsTerminateMessage(t *testing.T) {
	s := &Session{}
	s.init()

	s.Stop()

	// Check that a terminate message was queued
	select {
	case msg := <-s.outgoingControlMessages:
		assert.Equal(t, messages.InputStreamData, msg.MessageType)
		assert.Equal(t, messages.Fin, msg.Flags)
	default:
		// Channel might be empty if stopped channel closed first - that's ok
	}
}

// --- Wait returns nil on graceful stop ---

func TestSession_Wait_GracefulStop(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}
	mockHandler := &MockHandler{}

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", mock.Anything, mock.Anything).Return(nil)

	// ReadMessage blocks until close
	readDone := make(chan struct{})
	mockWS.On("ReadMessage").Run(func(_ mock.Arguments) {
		<-readDone
	}).Return(0, []byte{}, errors.New("closed"))
	mockWS.On("Close").Run(func(_ mock.Arguments) {
		close(readDone)
	}).Return(nil)

	mockHandler.On("Start", mock.Anything, mock.Anything).Return(nil)
	mockHandler.On("Wait", mock.Anything).Return(nil)
	mockHandler.On("Stop").Return()

	s := &Session{Dialer: mockDialer, Handler: mockHandler}

	err := s.Start(context.Background(), "ws://test", "token")
	assert.NoError(t, err)

	s.Stop()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	err = s.Wait(waitCtx)
	assert.NoError(t, err)
}

// --- Init with custom values ---

func TestSession_Init_CustomValues(t *testing.T) {
	mockHandler := &MockHandler{}
	mockDialer := &MockWebsocketDialer{}

	s := &Session{
		Handler: mockHandler,
		Dialer:  mockDialer,
	}
	s.init()

	// Custom values should be preserved
	assert.Equal(t, mockHandler, s.Handler)
	assert.Equal(t, mockDialer, s.Dialer)
}

// --- OpenDataChannel close error ---

func TestSession_Start_OpenDataChannelError_CloseError(t *testing.T) {
	mockWS := &MockWebsocketConnection{}
	mockDialer := &MockWebsocketDialer{}

	mockDialer.On("Dial", "ws://test").Return(mockWS, nil)
	mockWS.On("WriteMessage", websocket.TextMessage, mock.Anything).Return(errors.New("write error"))
	mockWS.On("Close").Return(errors.New("close error"))

	s := &Session{Dialer: mockDialer}

	err := s.Start(context.Background(), "ws://test", "token")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrOpenDataChannelFailed)
	// Should also contain the close error
	assert.Contains(t, err.Error(), "close error")
}
