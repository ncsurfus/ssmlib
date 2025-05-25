package handler

import (
	"context"

	"github.com/ncsurfus/ssmlib/messages"
)

type SessionReader interface {
	Read(ctx context.Context) (*messages.AgentMessage, error)
}

type SessionWriter interface {
	Write(ctx context.Context, message *messages.AgentMessage) error
}

type SessionReaderWriter interface {
	SessionReader
	SessionWriter
}

// Adapters
type SessionWriterFunc func(ctx context.Context, message *messages.AgentMessage) error

func (swf SessionWriterFunc) Write(ctx context.Context, message *messages.AgentMessage) error {
	return swf(ctx, message)
}

type SessionReaderFunc func(ctx context.Context) (*messages.AgentMessage, error)

func (srf SessionReaderFunc) Read(ctx context.Context) (*messages.AgentMessage, error) {
	return srf(ctx)
}
