package ssmlib

import (
	"context"
	"sync"

	"github.com/ncsurfus/ssmlib/handler"
)

type Handler interface {
	Start(ctx context.Context, session handler.SessionReaderWriter) error
	Stop()
	Wait(ctx context.Context) error
}

type NopHandler struct {
	stopped  chan struct{}
	stopOnce sync.Once
	initOnce sync.Once
}

func (n *NopHandler) init() {
	n.initOnce.Do(func() { n.stopped = make(chan struct{}) })
}

func (n *NopHandler) Start(ctx context.Context, session handler.SessionReaderWriter) error {
	n.init()
	return nil
}

func (n *NopHandler) Stop() {
	n.init()
	n.stopOnce.Do(func() { close(n.stopped) })
}

func (n *NopHandler) Wait(ctx context.Context) error {
	n.init()
	select {
	case <-n.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
