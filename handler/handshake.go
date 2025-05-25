package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ncsurfus/ssmlib/messages"
)

var (
	ErrSessionHandshake    = errors.New("failed to do session handshake")
	ErrUnmarshalHandshake  = errors.New("failed to unmarshal handshake request")
	ErrCreateHandshakeResp = errors.New("failed to create handshake response")
	ErrSendHandshakeResp   = errors.New("failed to send handshake response to session")
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
			return HandshakeResult{}, fmt.Errorf("%w: %w", ErrSessionHandshake, err)
		}

		if msg.PayloadType == messages.HandshakeRequest {
			if err := json.Unmarshal(msg.Payload, &handshakeRequest); err != nil {
				return HandshakeResult{}, fmt.Errorf("%w: %w", ErrUnmarshalHandshake, err)
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
		return HandshakeResult{}, fmt.Errorf("%w: %w", ErrCreateHandshakeResp, err)
	}

	err = session.Write(ctx, resp)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("%w: %w", ErrSendHandshakeResp, err)
	}

	// Wait for handshake completion
	for {
		msg, err := session.Read(ctx)
		if err != nil {
			return HandshakeResult{}, fmt.Errorf("%w: %w", ErrSessionHandshake, err)
		}

		if msg.PayloadType == messages.HandshakeComplete {
			break
		}
		log.Warn("received unexpected payload type, expected HandshakeComplete", "messageType", msg.PayloadType)
	}

	return HandshakeResult{RemoteVersion: handshakeRequest.AgentVersion}, nil
}
