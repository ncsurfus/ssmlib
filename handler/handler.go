package handler

import (
	"context"
	"fmt"
	"io"

	"github.com/ncsurfus/ssmlib/messages"
)

func CopySessionReaderToWriter(ctx context.Context, writer io.Writer, reader SessionReader) error {
	for {
		message, err := reader.Read(ctx)
		if err != nil {
			return fmt.Errorf("failed to get message from session: %w", err)
		}

		if message.PayloadType != messages.Output {
			continue
		}

		_, err = writer.Write(message.Payload)
		if err != nil {
			return fmt.Errorf("failed to write data to writer: %w", err)
		}
	}
}

func CopyReaderToSessionWriter(ctx context.Context, reader io.Reader, writer SessionWriter) error {
	for {
		// callers to read should always process any bytes first *and then* handle the error.)
		buffer := make([]byte, 1024)
		bytes, readerErr := reader.Read(buffer)
		if bytes > 0 {
			message := messages.NewStreamMessage(buffer[:bytes])
			writerErr := writer.Write(ctx, message)
			if writerErr != nil {
				return fmt.Errorf("failed to write message to session: %w", writerErr)
			}
		}

		if readerErr != nil {
			return fmt.Errorf("failed to read data from reader: %w", readerErr)
		}
	}
}

func SetTerminalSize(ctx context.Context, writer SessionWriter, width int, height int) error {
	msg, err := messages.NewSizeMessage(width, height)
	if err != nil {
		return fmt.Errorf("failed to create size message: %w", err)
	}

	err = writer.Write(ctx, msg)
	if err != nil {
		return fmt.Errorf("failed to write size message to session: %w", err)
	}

	return nil
}
