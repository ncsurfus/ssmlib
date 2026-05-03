package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/ncsurfus/ssmlib/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestPerformHandshake_Success(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion:           "3.1.0.0",
		RequestedClientActions: []messages.RequestedClientAction{{ActionType: messages.SessionType}},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	completeMsg := messages.NewAgentMessage()
	completeMsg.PayloadType = messages.HandshakeComplete

	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(nil).Once()
	mockSession.On("Read", mock.Anything).Return(completeMsg, nil).Once()

	result, err := PerformHandshake(context.Background(), slog.New(DiscardHandler), mockSession)
	assert.NoError(t, err)
	assert.Equal(t, "3.1.0.0", result.RemoteVersion)
}

func TestPerformHandshake_ReadError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockSession.On("Read", mock.Anything).Return(nil, errors.New("read failed"))

	_, err := PerformHandshake(context.Background(), slog.New(DiscardHandler), mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionHandshake)
}

func TestPerformHandshake_UnmarshalError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = []byte("not json")

	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()

	_, err := PerformHandshake(context.Background(), slog.New(DiscardHandler), mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnmarshalHandshake)
}

func TestPerformHandshake_WriteError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion:           "3.1.0.0",
		RequestedClientActions: []messages.RequestedClientAction{{ActionType: messages.SessionType}},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(errors.New("write failed"))

	_, err := PerformHandshake(context.Background(), slog.New(DiscardHandler), mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSendHandshakeResp)
}

func TestPerformHandshake_NonHandshakeMessageBeforeRequest(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// First message is not a handshake request
	otherMsg := messages.NewAgentMessage()
	otherMsg.PayloadType = messages.Output
	otherMsg.Payload = []byte("some data")

	// Second message is the handshake request
	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion:           "3.1.0.0",
		RequestedClientActions: []messages.RequestedClientAction{{ActionType: messages.SessionType}},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	completeMsg := messages.NewAgentMessage()
	completeMsg.PayloadType = messages.HandshakeComplete

	mockSession.On("Read", mock.Anything).Return(otherMsg, nil).Once()
	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(nil).Once()
	mockSession.On("Read", mock.Anything).Return(completeMsg, nil).Once()

	result, err := PerformHandshake(context.Background(), slog.New(DiscardHandler), mockSession)
	assert.NoError(t, err)
	assert.Equal(t, "3.1.0.0", result.RemoteVersion)
}

func TestPerformHandshake_NonHandshakeMessageBeforeComplete(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion:           "3.1.0.0",
		RequestedClientActions: []messages.RequestedClientAction{{ActionType: messages.SessionType}},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	// Non-complete message before the actual complete
	otherMsg := messages.NewAgentMessage()
	otherMsg.PayloadType = messages.Output

	completeMsg := messages.NewAgentMessage()
	completeMsg.PayloadType = messages.HandshakeComplete

	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(nil).Once()
	mockSession.On("Read", mock.Anything).Return(otherMsg, nil).Once()
	mockSession.On("Read", mock.Anything).Return(completeMsg, nil).Once()

	result, err := PerformHandshake(context.Background(), slog.New(DiscardHandler), mockSession)
	assert.NoError(t, err)
	assert.Equal(t, "3.1.0.0", result.RemoteVersion)
}

func TestPerformHandshake_ReadErrorAfterResponse(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	handshakeReq := messages.HandshakeRequestPayload{
		AgentVersion:           "3.1.0.0",
		RequestedClientActions: []messages.RequestedClientAction{{ActionType: messages.SessionType}},
	}
	reqPayload, _ := json.Marshal(handshakeReq)
	reqMsg := messages.NewAgentMessage()
	reqMsg.PayloadType = messages.HandshakeRequest
	reqMsg.Payload = reqPayload

	mockSession.On("Read", mock.Anything).Return(reqMsg, nil).Once()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(nil).Once()
	mockSession.On("Read", mock.Anything).Return(nil, errors.New("read failed"))

	_, err := PerformHandshake(context.Background(), slog.New(DiscardHandler), mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionHandshake)
}
