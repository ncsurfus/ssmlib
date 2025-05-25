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

// Stream provides bidirectional streaming over an SSM session, typically used for
// interactive shell sessions. It copies data between local Reader/Writer interfaces
// and the SSM session, while also managing terminal size updates for proper display
// formatting on the remote system.
//
// The Stream handler automatically detects terminal capabilities and periodically
// sends terminal size updates to ensure proper formatting of interactive applications.
type Stream struct {
	// Reader provides input data to send to the remote session (default os.Stdin)
	Reader io.Reader
	// Writer receives output data from the remote session (default os.Stdout)
	Writer io.Writer
	// TerminalSize provides terminal dimensions for the remote session
	TerminalSize TerminalSize
	// Log provides structured logging for stream events
	Log *slog.Logger

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
			s.Log = slog.New(DiscardHandler)
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

// Start initializes the streaming handler and begins bidirectional data copying.
// It starts background goroutines to copy data between the local Reader/Writer
// and the SSM session, and also starts a goroutine to periodically update the
// terminal size on the remote system. The method returns after all background
// processing has been started.
//
// Parameters:
//   - ctx: Context for the startup process (not used for handler lifetime)
//   - session: The SSM session to stream data through
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

// Wait blocks until the handler has completely stopped or the context is cancelled.
// If the context is cancelled, Wait returns immediately with the context error,
// but the handler and its background goroutines may still be running. This means
// that if the context cancels, the handler could still be active.
//
// Wait should be called after Start() to block until the handler terminates.
// It returns nil if the handler stopped gracefully, or an error if it stopped
// due to a failure.
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

// Stop initiates a graceful shutdown of the streaming handler.
// This method does NOT wait for the handler to fully shutdown - it only signals
// the shutdown request. The handler and its background goroutines may continue
// running after Stop() returns. Use Wait() to block until the handler has
// completely stopped.
//
// Stop() is safe to call multiple times and from multiple goroutines.
func (s *Stream) Stop() {
	s.init()
	s.signalStop()
}
