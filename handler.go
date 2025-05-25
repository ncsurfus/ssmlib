package ssmlib

import (
	"context"

	"github.com/ncsurfus/ssmlib/handler"
)

type Handler interface {
	Start(ctx context.Context, session handler.SessionReaderWriter) error
	Stop()
	Wait(ctx context.Context) error
}

type NopHandler struct {
	stopped chan struct{}
}

func (n *NopHandler) Start(ctx context.Context, session handler.SessionReaderWriter) error {
	n.stopped = make(chan struct{})
	return nil
}

func (n *NopHandler) Stop() {
	close(n.stopped)
}

func (n *NopHandler) Wait(ctx context.Context) error {
	select {
	case <-n.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
