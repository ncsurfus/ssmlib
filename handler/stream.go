package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
)

const TerminalWindowIntervalMilliseconds = 250

type Stream struct {
	TerminalSize TerminalSize
	Log          *slog.Logger

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

func (s *Stream) getTerminalSize(writer io.Writer) TerminalSize {
	if s.TerminalSize != nil {
		return s.TerminalSize
	}

	// If our output has an Fd method, like *os.File, then we should default to that.
	// Otherwise, just fallback to a safe default.
	fd, ok := writer.(interface{ Fd() uintptr })
	if !ok {
		return DefaultTerminalSizeFunc
	}

	return TerminalSizeFunc(func() (width int, height int, err error) {
		return term.GetSize(int(fd.Fd()))
	})
}

func (s *Stream) Start(ctx context.Context, session SessionReaderWriter, reader io.Reader, writer io.Writer) error {
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
		return CopySessionReaderToWriter(s.errctx, writer, session)
	})

	// Copy data from the reader to SSM
	s.errgrp.Go(func() error {
		return CopyReaderToSessionWriter(s.errctx, reader, session)
	})

	// Update Terminal Size
	s.errgrp.Go(func() error {
		// This failing should not force the errgrp to exit!
		terminalSize := s.getTerminalSize(writer)
		lastWidth, lastHeight := 0, 0
		for {
			select {
			case <-s.errctx.Done():
				return nil
			case <-time.After(TerminalWindowIntervalMilliseconds * time.Millisecond):
				width, height, err := terminalSize.Size()
				if err != nil {
					s.Log.Warn("failed to get terminal size!", "error", err)
				}
				if lastWidth != width || lastHeight != height {
					err = SetTerminalSize(s.errctx, session, width, height)
					if err != nil {
						s.Log.Warn("failed to set terminal size!", "error", err)
					}
					lastWidth, lastHeight = width, height
				}
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
