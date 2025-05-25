package ssmlib

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ncsurfus/ssmlib/handler"
	"github.com/ncsurfus/ssmlib/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockWebsocketConnection implements WebsocketConection for testing
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

func (m *MockWebsocketDialer) Dial(url string) (WebsocketConection, error) {
	args := m.Called(url)
	if conn, ok := args.Get(0).(WebsocketConection); ok {
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
