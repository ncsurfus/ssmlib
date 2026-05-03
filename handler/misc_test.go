package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/ncsurfus/ssmlib/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestSetTerminalSize_Success(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockSession.On("Write", mock.Anything, mock.MatchedBy(func(msg *messages.AgentMessage) bool {
		return msg.PayloadType == messages.Size
	})).Return(nil)

	err := SetTerminalSize(context.Background(), mockSession, 120, 40)
	assert.NoError(t, err)
	mockSession.AssertExpectations(t)
}

func TestSetTerminalSize_WriteError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockSession.On("Write", mock.Anything, mock.Anything).Return(errors.New("write failed"))

	err := SetTerminalSize(context.Background(), mockSession, 80, 24)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrWriteSizeMessage)
}

func TestCopySessionReaderToWriter_SkipsNonOutputPayload(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	writer := &MockWriter{}

	// First message: non-Output payload type (should be skipped)
	sizeMsg := messages.NewAgentMessage()
	sizeMsg.PayloadType = messages.Size
	sizeMsg.Payload = []byte(`{"cols":80,"rows":24}`)

	// Second message: Output payload type (should be written)
	outputMsg := messages.NewAgentMessage()
	outputMsg.PayloadType = messages.Output
	outputMsg.Payload = []byte("hello")

	mockSession.On("Read", mock.Anything).Return(sizeMsg, nil).Once()
	mockSession.On("Read", mock.Anything).Return(outputMsg, nil).Once()
	// Third read returns error to exit the loop
	mockSession.On("Read", mock.Anything).Return(nil, errors.New("done"))

	writer.On("Write", []byte("hello")).Return(5, nil)

	err := CopySessionReaderToWriter(context.Background(), writer, mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrGetSessionMessage)
	writer.AssertCalled(t, "Write", []byte("hello"))
}

func TestCopySessionReaderToWriter_ReadError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	writer := &MockWriter{}

	mockSession.On("Read", mock.Anything).Return(nil, errors.New("read failed"))

	err := CopySessionReaderToWriter(context.Background(), writer, mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrGetSessionMessage)
}

func TestCopySessionReaderToWriter_WriteError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	writer := &MockWriter{}

	outputMsg := messages.NewAgentMessage()
	outputMsg.PayloadType = messages.Output
	outputMsg.Payload = []byte("data")

	mockSession.On("Read", mock.Anything).Return(outputMsg, nil)
	writer.On("Write", []byte("data")).Return(0, errors.New("write failed"))

	err := CopySessionReaderToWriter(context.Background(), writer, mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrWriteData)
}

func TestCopyReaderToSessionWriter_ReadError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	reader := &MockReader{}

	reader.On("Read", mock.Anything).Return(0, errors.New("read failed"))

	err := CopyReaderToSessionWriter(context.Background(), reader, mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrReadData)
}

func TestCopyReaderToSessionWriter_WriteError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	reader := &MockReader{}

	reader.On("Read", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(0).([]byte)
		copy(p, "test")
	}).Return(4, nil)
	mockSession.On("Write", mock.Anything, mock.Anything).Return(errors.New("write failed"))

	err := CopyReaderToSessionWriter(context.Background(), reader, mockSession)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrWriteSessionMessage)
}
