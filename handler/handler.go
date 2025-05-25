package handler

import (
	"context"
	"fmt"
	"io"

	"github.com/ncsurfus/ssmlib/messages"
	"github.com/ncsurfus/ssmlib/session"
)

func CopySessionReaderToWriter(ctx context.Context, writer io.Writer, reader session.Reader) error {
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

func CopyReaderToSessionWriter(ctx context.Context, reader io.Reader, writer session.Writer) error {
	for {
		buffer := make([]byte, 1024)
		bytes, err := reader.Read(buffer)
		if err != nil {
			return fmt.Errorf("failed to read data from reader: %w", err)
		}

		message := messages.NewStreamMessage(buffer[:bytes])
		err = writer.Write(ctx, message)
		if err != nil {
			return fmt.Errorf("failed to write message to session: %w", err)
		}
	}
}

func SetTerminalSize(ctx context.Context, session session.Writer, width int, height int) error {
	msg, err := messages.NewSizeMessage(width, height)
	if err != nil {
		return fmt.Errorf("failed to create size message: %w", err)
	}

	err = session.Write(ctx, msg)
	if err != nil {
		return fmt.Errorf("failed to write size message to session: %w", err)
	}

	return nil
}
