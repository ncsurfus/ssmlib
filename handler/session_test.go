package handler

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ncsurfus/ssmlib/messages"
	"github.com/stretchr/testify/assert"
)

func TestDiscardHandler_Handle(t *testing.T) {
	err := DiscardHandler.Handle(context.Background(), slog.Record{})
	assert.NoError(t, err)
}

func TestDiscardHandler_WithAttrs(t *testing.T) {
	h := DiscardHandler.WithAttrs([]slog.Attr{slog.String("key", "val")})
	assert.Equal(t, DiscardHandler, h)
}

func TestDiscardHandler_WithGroup(t *testing.T) {
	h := DiscardHandler.WithGroup("group")
	assert.Equal(t, DiscardHandler, h)
}

func TestDiscardHandler_Enabled(t *testing.T) {
	assert.False(t, DiscardHandler.Enabled(context.Background(), slog.LevelDebug))
	assert.False(t, DiscardHandler.Enabled(context.Background(), slog.LevelInfo))
	assert.False(t, DiscardHandler.Enabled(context.Background(), slog.LevelError))
}

func TestSessionWriterFunc(t *testing.T) {
	called := false
	var gotMsg *messages.AgentMessage
	swf := SessionWriterFunc(func(ctx context.Context, msg *messages.AgentMessage) error {
		called = true
		gotMsg = msg
		return nil
	})

	msg := messages.NewAgentMessage()
	err := swf.Write(context.Background(), msg)
	assert.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, msg, gotMsg)
}

func TestSessionWriterFunc_Error(t *testing.T) {
	expectedErr := errors.New("write error")
	swf := SessionWriterFunc(func(ctx context.Context, msg *messages.AgentMessage) error {
		return expectedErr
	})

	err := swf.Write(context.Background(), messages.NewAgentMessage())
	assert.ErrorIs(t, err, expectedErr)
}

func TestSessionReaderFunc(t *testing.T) {
	expectedMsg := messages.NewAgentMessage()
	srf := SessionReaderFunc(func(ctx context.Context) (*messages.AgentMessage, error) {
		return expectedMsg, nil
	})

	msg, err := srf.Read(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, expectedMsg, msg)
}

func TestSessionReaderFunc_Error(t *testing.T) {
	expectedErr := errors.New("read error")
	srf := SessionReaderFunc(func(ctx context.Context) (*messages.AgentMessage, error) {
		return nil, expectedErr
	})

	msg, err := srf.Read(context.Background())
	assert.Nil(t, msg)
	assert.ErrorIs(t, err, expectedErr)
}
