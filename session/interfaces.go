package session

import (
	"context"

	"github.com/ncsurfus/ssmlib/messages"
)

type Reader interface {
	Read(ctx context.Context) (*messages.AgentMessage, error)
}

type Writer interface {
	Write(ctx context.Context, message *messages.AgentMessage) error
}

type ReaderWriter interface {
	Reader
	Writer
}

// Adapters
type WriterFunc func(ctx context.Context, message *messages.AgentMessage) error

func (swf WriterFunc) Write(ctx context.Context, message *messages.AgentMessage) error {
	return swf(ctx, message)
}

type ReaderFunc func(ctx context.Context) (*messages.AgentMessage, error)

func (srf ReaderFunc) Read(ctx context.Context) (*messages.AgentMessage, error) {
	return srf(ctx)
}
