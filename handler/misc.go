package handler

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ncsurfus/ssmlib/messages"
)

var (
	ErrGetSessionMessage   = errors.New("failed to get message from session")
	ErrWriteData           = errors.New("failed to write data to writer")
	ErrWriteSessionMessage = errors.New("failed to write message to session")
	ErrReadData            = errors.New("failed to read data from reader")
	ErrCreateSizeMessage   = errors.New("failed to create size message")
	ErrWriteSizeMessage    = errors.New("failed to write size message to session")
)

type readResult struct {
	n   int
	err error
}

// If the context is cancelled, we can't reliably abort a message to the reader/writer. This is problematic
// in some scenarios, like stdin likes to hang until there's input (even when closed!) These read/write data
// in a separate goroutine through a channel. Technically, if the caller wanted to reuse this reader/writer
// after stopping a handler, then our goroutine could "discard" a final reader/write.... this is an edge case
// as everyone should mostly close their reader/writer, enabling the goroutine to cleanly exit.

func CopySessionReaderToWriter(ctx context.Context, writer io.Writer, reader SessionReader) error {
	writeErrC := make(chan error, 1)
	writeC := make(chan []byte)
	defer close(writeC)
	go func() {
		for {
			payload, ok := <-writeC
			if !ok {
				return
			}
			_, err := writer.Write(payload)
			writeErrC <- err
		}
	}()

	for {
		message, err := reader.Read(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrGetSessionMessage, err)
		}

		if message.PayloadType != messages.Output {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case writeC <- message.Payload:
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-writeErrC:
			if err != nil {
				return fmt.Errorf("%w: %w", ErrWriteData, err)
			}
		}
	}
}

func CopyReaderToSessionWriter(ctx context.Context, reader io.Reader, writer SessionWriter) error {
	readResultC := make(chan readResult, 1)
	readC := make(chan []byte)
	defer close(readC)
	go func() {
		for {
			buffer, ok := <-readC
			if !ok {
				return
			}
			n, err := reader.Read(buffer)
			readResultC <- readResult{n: n, err: err}
		}
	}()

	for {
		// callers to read should always process any bytes first *and then* handle the error.)
		buffer := make([]byte, 1024)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case readC <- buffer:
		}

		var bytes int
		var readerErr error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-readResultC:
			bytes, readerErr = r.n, r.err
		}

		if bytes > 0 {
			message := messages.NewStreamMessage(buffer[:bytes])
			writerErr := writer.Write(ctx, message)
			if writerErr != nil {
				return fmt.Errorf("%w: %w", ErrWriteSessionMessage, writerErr)
			}
		}

		if readerErr != nil {
			return fmt.Errorf("%w: %w", ErrReadData, readerErr)
		}
	}
}

func SetTerminalSize(ctx context.Context, writer SessionWriter, width int, height int) error {
	msg, err := messages.NewSizeMessage(width, height)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCreateSizeMessage, err)
	}

	err = writer.Write(ctx, msg)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWriteSizeMessage, err)
	}

	return nil
}
