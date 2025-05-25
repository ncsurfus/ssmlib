package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/ncsurfus/ssmlib/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockSessionReaderWriter implements SessionReaderWriter for testing
type MockSessionReaderWriter struct {
	mock.Mock
}

func (m *MockSessionReaderWriter) Read(ctx context.Context) (*messages.AgentMessage, error) {
	args := m.Called(ctx)
	if msg, ok := args.Get(0).(*messages.AgentMessage); ok {
		return msg, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockSessionReaderWriter) Write(ctx context.Context, message *messages.AgentMessage) error {
	args := m.Called(ctx, message)
	return args.Error(0)
}

func TestMuxPortForward_Start_HandshakeFailed(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockSession.On("Read", mock.Anything).Return(nil, errors.New("read error"))

	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := mux.Start(ctx, mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrHandshakeFailed)
}

func TestMuxPortForward_Start_RemoteVersionNotSupported(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// Create handshake request with old version
	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion: "1.0.0.0", // Version that doesn't support mux
		RequestedClientActions: []messages.RequestedClientAction{
			{ActionType: messages.SessionType},
		},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	// Create handshake complete message
	completeMsg := messages.NewAgentMessage()
	completeMsg.PayloadType = messages.HandshakeComplete

	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(nil).Once()
	mockSession.On("Read", mock.Anything).Return(completeMsg, nil).Once()

	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := mux.Start(ctx, mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRemoteVersionNotSupported)
}

func TestMuxPortForward_Start_Success(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// Create handshake request with supported version
	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion: "3.1.0.0", // Version that supports mux
		RequestedClientActions: []messages.RequestedClientAction{
			{ActionType: messages.SessionType},
		},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	// Create handshake complete message
	completeMsg := messages.NewAgentMessage()
	completeMsg.PayloadType = messages.HandshakeComplete

	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(nil).Once()
	mockSession.On("Read", mock.Anything).Return(completeMsg, nil).Once()

	// Mock the ongoing read/write operations that will be canceled
	mockSession.On("Read", mock.Anything).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := mux.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Stop the mux to trigger clean shutdown
	mux.Stop()

	// Wait for the mux to finish
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = mux.Wait(waitCtx)
	assert.NoError(t, err)
}

func TestMuxPortForward_Stop(t *testing.T) {
	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	// Stop should not panic even if not started
	mux.Stop()

	// Test that Stop can be called multiple times
	mux.Stop()
}

func TestMuxPortForward_Wait_ContextCanceled(t *testing.T) {
	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := mux.Wait(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMuxPortForward_Wait_AfterStop(t *testing.T) {
	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	// Stop the mux
	mux.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := mux.Wait(ctx)
	// When stopped without being started, it should timeout
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMuxPortForward_Dial_NotStarted(t *testing.T) {
	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	conn, err := mux.Dial(ctx)
	assert.Nil(t, conn)
	assert.Error(t, err)
	// When not started, it should timeout waiting for dial requests
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMuxPortForward_Dial_ContextCanceled(t *testing.T) {
	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	conn, err := mux.Dial(ctx)
	assert.Nil(t, conn)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMuxPortForward_Dial_AfterStop(t *testing.T) {
	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	// Stop the mux first
	mux.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	conn, err := mux.Dial(ctx)
	assert.Nil(t, conn)
	assert.Error(t, err)
	// When stopped, the errctx should be done, causing a timeout
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMuxPortForward_MultipleInit(t *testing.T) {
	mux := &MuxPortForward{}

	// Multiple calls to init should be safe
	mux.init()
	mux.init()
	mux.init()

	assert.NotNil(t, mux.dialRequests)
	assert.NotNil(t, mux.stopped)
	assert.NotNil(t, mux.Log)
	assert.NotNil(t, mux.errgrp)
	assert.NotNil(t, mux.errctx)
}

func TestMuxPortForward_ErrorVariables(t *testing.T) {
	// Test that all error variables are defined
	assert.NotNil(t, ErrHandshakeFailed)
	assert.NotNil(t, ErrRemoteVersionNotSupported)
	assert.NotNil(t, ErrCreateSmuxClientFailed)
	assert.NotNil(t, ErrFailedGetDialRequest)
	assert.NotNil(t, ErrFailedGetDialResponse)
	assert.NotNil(t, ErrFailedRequestDial)
	assert.NotNil(t, ErrFailedDial)

	// Test error messages
	assert.Contains(t, ErrHandshakeFailed.Error(), "handshake failed")
	assert.Contains(t, ErrRemoteVersionNotSupported.Error(), "remote ssm agent version")
	assert.Contains(t, ErrCreateSmuxClientFailed.Error(), "failed to create smux client")
	assert.Contains(t, ErrFailedDial.Error(), "failed to dial")
}

func TestMuxPortForward_VersionFlags(t *testing.T) {
	// Test that version flags are defined
	assert.NotEmpty(t, MuxSupported)
	assert.NotEmpty(t, SmuxKeepAliveSupported)

	// Test version support
	assert.True(t, MuxSupported.SupportsVersion("3.1.0.0"))
	assert.False(t, MuxSupported.SupportsVersion("2.0.0.0"))

	assert.True(t, SmuxKeepAliveSupported.SupportsVersion("3.2.0.0"))
	assert.False(t, SmuxKeepAliveSupported.SupportsVersion("3.0.0.0"))
}

// Test integration scenario with a mock network connection
func TestMuxPortForward_Integration(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// Create handshake request with supported version
	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion: "3.1.0.0",
		RequestedClientActions: []messages.RequestedClientAction{
			{ActionType: messages.SessionType},
		},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	completeMsg := messages.NewAgentMessage()
	completeMsg.PayloadType = messages.HandshakeComplete

	// Set up mock expectations
	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(nil).Once()
	mockSession.On("Read", mock.Anything).Return(completeMsg, nil).Once()

	// Mock ongoing operations that will be canceled
	mockSession.On("Read", mock.Anything).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	mux := &MuxPortForward{
		Log: slog.New(DiscardHandler),
	}

	// Start the mux
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := mux.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Stop the mux
	mux.Stop()

	// Wait for completion
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = mux.Wait(waitCtx)
	assert.NoError(t, err)

	mockSession.AssertExpectations(t)
}

// MockConn implements net.Conn for testing
type MockConn struct {
	mock.Mock
}

func (m *MockConn) Read(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}

func (m *MockConn) Write(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}

func (m *MockConn) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockConn) LocalAddr() net.Addr {
	args := m.Called()
	if addr, ok := args.Get(0).(net.Addr); ok {
		return addr
	}
	return nil
}

func (m *MockConn) RemoteAddr() net.Addr {
	args := m.Called()
	if addr, ok := args.Get(0).(net.Addr); ok {
		return addr
	}
	return nil
}

func (m *MockConn) SetDeadline(t time.Time) error {
	args := m.Called(t)
	return args.Error(0)
}

func (m *MockConn) SetReadDeadline(t time.Time) error {
	args := m.Called(t)
	return args.Error(0)
}

func (m *MockConn) SetWriteDeadline(t time.Time) error {
	args := m.Called(t)
	return args.Error(0)
}
