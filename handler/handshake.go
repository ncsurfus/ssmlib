package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ncsurfus/ssmlib/messages"
)

// This hasn't changed for years...
const ClientVersion = "1.2.0.0"

type HandshakeResult struct {
	RemoteVersion string
}

func PerformHandshake(ctx context.Context, log *slog.Logger, session SessionReaderWriter) (HandshakeResult, error) {
	// Wait for handshake request
	handshakeRequest := messages.HandshakeRequestPayload{}
	for {
		msg, err := session.Read(ctx)
		if err != nil {
			return HandshakeResult{}, fmt.Errorf("failed to do session handshake: %w", err)
		}

		if msg.PayloadType == messages.HandshakeRequest {
			if err := json.Unmarshal(msg.Payload, &handshakeRequest); err != nil {
				return HandshakeResult{}, fmt.Errorf("failed to unmarshal handshake request: %w", err)
			}
			break
		}
		log.Warn("received unexpected payload type, expected HandshakeRequest", "messageType", msg.PayloadType)
	}

	// Send handshake response
	// Eventually we need a better way for the caller to do something with the RequestedClientActions...
	// like verifying the SessionType is accurate...
	resp, err := messages.NewHandshakeResponse(ClientVersion, handshakeRequest.RequestedClientActions)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("failed to create handshake response: %w", err)
	}

	err = session.Write(ctx, resp)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("failed to send handshake response to session: %w", err)
	}

	// Wait for handshake completion
	for {
		msg, err := session.Read(ctx)
		if err != nil {
			return HandshakeResult{}, fmt.Errorf("failed to do session handshake: %w", err)
		}

		if msg.PayloadType == messages.HandshakeComplete {
			break
		}
		log.Warn("received unexpected payload type, expected HandshakeComplete", "messageType", msg.PayloadType)
	}

	return HandshakeResult{RemoteVersion: handshakeRequest.AgentVersion}, nil
}
