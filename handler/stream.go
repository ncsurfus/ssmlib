package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	tsize "github.com/kopoli/go-terminal-size"
	"github.com/ncsurfus/ssmlib/messages"
	"github.com/ncsurfus/ssmlib/session"
	"golang.org/x/sync/errgroup"
)

type Stream struct {
	Reader io.Reader
	Writer io.Writer
	Log    *slog.Logger

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
		if s.Log == nil {
			s.Log = slog.New(slog.DiscardHandler)
		}
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

	// Update Terminal Size
	s.errgrp.Go(func() error {
		// This failing should not force the errgrp to exit!
		listener, err := tsize.NewSizeListener()
		if err != nil {
			s.Log.Warn("Failed to initialize terminal listener. Cannot get terminal size!")
			return nil
		}
		defer listener.Close()

		updateSize := func(size tsize.Size) {
			msg, err := messages.NewSizeMessage(uint32(size.Height), uint32(size.Width))
			if err != nil {
				s.Log.Warn("failed to create terminal size message!", "error", err)
				return
			}
			err = session.Write(s.errctx, msg)
			if err != nil {
				s.Log.Warn("failed to send terminal size message!", "error", err)
				return
			}
		}

		initialSize, err := tsize.GetSize()
		if err != nil {
			s.Log.Warn("failed to get initial terminal size!", "error", err)
		} else {
			updateSize(initialSize)
		}

		for {
			select {
			case <-s.errctx.Done():
				return nil
			case size := <-listener.Change:
				updateSize(size)
			}
		}

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

func (s *Stream) Stop() {
	s.init()
	s.signalStop()
}
