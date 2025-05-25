package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
)

var errStreamStopRequested = errors.New("stream stopped requested")

const TerminalWindowIntervalMilliseconds = 250

type Stream struct {
	Reader       io.Reader
	Writer       io.Writer
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
		if s.Reader == nil {
			s.Reader = os.Stdin
		}
		if s.Writer == nil {
			s.Writer = os.Stdout
		}
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

func (s *Stream) getTerminalSize() TerminalSize {
	if s.TerminalSize != nil {
		return s.TerminalSize
	}

	// If our output has an Fd method, like *os.File, then we should default to that.
	// Otherwise, just fallback to a safe default.
	fd, ok := s.Writer.(interface{ Fd() uintptr })
	if !ok {
		return DefaultTerminalSizeFunc
	}

	return TerminalSizeFunc(func() (width int, height int, err error) {
		return term.GetSize(int(fd.Fd()))
	})
}

func (s *Stream) Start(ctx context.Context, session SessionReaderWriter) error {
	s.init()

	// Cleanup resources
	s.errgrp.Go(func() error {
		s.Log.Debug("Listening for internal cancellation")
		defer s.Log.Debug("Internal cancellation triggered.")

		<-s.errctx.Done()
		s.signalStop()

		return nil
	})

	// Handle a Stop() event.
	s.errgrp.Go(func() error {
		s.Log.Debug("Listening for stop event")
		defer s.Log.Debug("Stop event triggered.")

		// The cleanup resources goroutine will ensure stop gets closed.
		<-s.stopped
		return errStreamStopRequested
	})

	// Copy data from SSM to the writer
	s.errgrp.Go(func() error {
		s.Log.Debug("Starting copy of session to writer")
		defer s.Log.Debug("Stopping copy of session to writer.")

		return CopySessionReaderToWriter(s.errctx, s.Writer, session)
	})

	// Copy data from the reader to SSM
	s.errgrp.Go(func() error {
		s.Log.Debug("Starting copy of reader to session")
		defer s.Log.Debug("Stopping copy of reader to session.")

		return CopyReaderToSessionWriter(s.errctx, s.Reader, session)
	})

	// Update Terminal Size
	s.errgrp.Go(func() error {
		// This failing should not force the errgrp to exit!
		terminalSize := s.getTerminalSize()
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
		return ctx.Err()
	case <-s.errctx.Done():
		err := s.errgrp.Wait()
		if err != errStreamStopRequested {
			return err
		}
		return nil
	}
}

func (s *Stream) Stop() {
	s.init()
	s.signalStop()
}
