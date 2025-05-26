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

func CopySessionReaderToWriter(ctx context.Context, writer io.Writer, reader SessionReader) error {
	for {
		message, err := reader.Read(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrGetSessionMessage, err)
		}

		if message.PayloadType != messages.Output {
			continue
		}

		_, err = writer.Write(message.Payload)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrWriteData, err)
		}
	}
}

func CopyReaderToSessionWriter(ctx context.Context, reader io.Reader, writer SessionWriter) error {
	// In the future we should make this optionally buffered with a timer to flush data. This would
	// enable callers to pick Latency vs Throughput. A small delay has been added to help prevent
	// enable some buffering and prevent sending lots of tiny packets.
	for {
		// callers to read should always process any bytes first *and then* handle the error.)
		buffer := make([]byte, 1024)
		bytes, readerErr := reader.Read(buffer)
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
