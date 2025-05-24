package handler

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/ncsurfus/ssmlib/session"
	"golang.org/x/sync/errgroup"
)

type Stream struct {
	Reader io.Reader
	Writer io.Writer

	errgrp *errgroup.Group
	errctx context.Context

	stopped  chan struct{}
	stopOnce sync.Once
	initOnce sync.Once
}

func (s *Stream) init() {
	s.initOnce.Do(func() {
		s.stopped = make(chan struct{})
		s.errgrp, s.errctx = errgroup.WithContext(context.Background())
	})
}

func (s *Stream) signalStop() {
	s.stopOnce.Do(func() { close(s.stopped) })
}

func (s *Stream) Start(ctx context.Context, session session.ReaderWriter) error {
	s.init()

	// Cleanup resources
	s.errgrp.Go(func() error {
		<-s.errctx.Done()
		s.signalStop()
		return nil
	})

	// Handle a Stop() event.
	s.errgrp.Go(func() error {
		// The cleanup resources goroutine will ensure stop gets closed.
		<-s.stopped
		return io.EOF
	})

	// Copy data from SSM to the writer
	s.errgrp.Go(func() error {
		return CopySessionReaderToWriter(s.errctx, s.Writer, session)
	})

	// Copy data from the reader to SSM
	s.errgrp.Go(func() error {
		return CopyReaderToSessionWriter(s.errctx, s.Reader, session)
	})

	return nil
}

func (s *Stream) Wait(ctx context.Context) error {
	s.init()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait cancelled: %w", ctx.Err())
	case <-s.errctx.Done():
		err := s.errgrp.Wait()
		if err != io.EOF {
			return err
		}
		return nil
	}
}

func (s *Stream) Stop(ctx context.Context) {
	s.init()
	s.signalStop()
}
