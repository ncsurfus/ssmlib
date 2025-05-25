package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/ncsurfus/ssmlib/version"
	"github.com/xtaci/smux"
	"golang.org/x/sync/errgroup"
)

var (
	ErrHandshakeFailed           = errors.New("handshake failed")
	ErrRemoteVersionNotSupported = errors.New("the remote ssm agent version does not support mux")
	ErrCreateSmuxClientFailed    = errors.New("failed to create smux client")
	ErrFailedGetDialRequest      = errors.New("failed to get dial request, context cancelled")
	ErrFailedGetDialResponse     = errors.New("failed to get dial response")
	ErrFailedRequestDial         = errors.New("failed to request dial")
	ErrFailedDial                = errors.New("failed to dial")
	errMuxStopRequested          = errors.New("mux stop requested")
)

var MuxSupported version.FeatureFlag = "3.0.196.0"
var SmuxKeepAliveSupported version.FeatureFlag = "3.1.1511.0"

type muxDialResponse struct {
	conn net.Conn
	err  error
}

type muxDialRequest struct {
	response chan muxDialResponse
}

// MuxPortForward provides multiplexed port forwarding over an SSM session using smux.
// It performs a handshake with the remote SSM agent to establish multiplexing capabilities,
// then creates a smux session over the SSM data stream. This allows multiple concurrent
// connections to be tunneled through a single SSM session.
//
// The handler verifies that the remote SSM agent supports multiplexing before proceeding.
// If multiplexing is not supported, Start() will return an error.
type MuxPortForward struct {
	// Log provides structured logging for multiplexing events
	Log *slog.Logger

	dialRequests chan muxDialRequest

	errgrp *errgroup.Group
	errctx context.Context

	stopped  chan struct{}
	stopOnce sync.Once
	initOnce sync.Once
}

func (m *MuxPortForward) init() {
	m.initOnce.Do(func() {
		m.dialRequests = make(chan muxDialRequest) // unbuffered by design
		m.stopped = make(chan struct{})
		if m.Log == nil {
			m.Log = slog.New(DiscardHandler)
		}
		m.errgrp, m.errctx = errgroup.WithContext(context.Background())
	})

}

func (m *MuxPortForward) signalStop() {
	m.stopOnce.Do(func() { close(m.stopped) })
}

// Start initializes the multiplexed port forwarding handler.
// It performs a handshake with the remote SSM agent to verify multiplexing support,
// creates a smux session over the SSM data stream, and starts background goroutines
// to handle data copying and dial requests. The method returns after all background
// processing has been started.
//
// Parameters:
//   - ctx: Context for the startup process (not used for handler lifetime)
//   - session: The SSM session to multiplex over
//
// Returns ErrRemoteVersionNotSupported if the remote agent doesn't support multiplexing.
func (m *MuxPortForward) Start(ctx context.Context, session SessionReaderWriter) error {
	m.init()

	// Perform the handshake.
	handshakeResult, err := PerformHandshake(ctx, m.Log, session)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHandshakeFailed, err)
	}

	m.Log.Info("Handshake complete with remote agent.", "RemoteVersion", handshakeResult.RemoteVersion)
	if !MuxSupported.SupportsVersion(handshakeResult.RemoteVersion) {
		return fmt.Errorf("%w: %v", ErrRemoteVersionNotSupported, handshakeResult.RemoteVersion)
	}

	m.Log.Debug("Initializing smux.")
	smuxConfig := smux.DefaultConfig()
	if SmuxKeepAliveSupported.SupportsVersion(handshakeResult.RemoteVersion) {
		m.Log.Debug("Disabling smux keep alives.")
		smuxConfig.KeepAliveDisabled = true
	}

	// Data received from the SSM stream goes directly to the smuxClient
	dataConn, clientConn := net.Pipe()
	smuxSession, err := smux.Client(clientConn, smuxConfig)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCreateSmuxClientFailed, err)
	}

	// We should note that we should never return nil from the errgroup, except
	// when the errctx is Done()

	// Cleanup resources
	m.errgrp.Go(func() error {
		<-m.errctx.Done()
		err := smuxSession.Close()
		if err != nil {
			m.Log.Warn("smux failed to close", "error", err)
		}
		m.signalStop()
		return nil
	})

	// Handle a Stop() event.
	m.errgrp.Go(func() error {
		// The cleanup resources goroutine will ensure stop gets closed.
		<-m.stopped
		return errMuxStopRequested
	})

	// Copy data from SSM to smux
	m.errgrp.Go(func() error {
		return CopySessionReaderToWriter(m.errctx, dataConn, session)
	})

	// Copy data from smux to SSM
	m.errgrp.Go(func() error {
		return CopyReaderToSessionWriter(m.errctx, dataConn, session)
	})

	// Handle incoming dial requests
	m.errgrp.Go(func() error {
		return m.handleDialRequests(m.errctx, smuxSession)
	})

	return nil
}

// Stop initiates a graceful shutdown of the multiplexed port forwarding handler.
// This method does NOT wait for the handler to fully shutdown - it only signals
// the shutdown request. The handler and its background goroutines may continue
// running after Stop() returns. Use Wait() to block until the handler has
// completely stopped.
//
// Stop() is safe to call multiple times and from multiple goroutines.
func (m *MuxPortForward) Stop() {
	m.init()
	defer m.signalStop()
}

// Wait blocks until the handler has completely stopped or the context is cancelled.
// If the context is cancelled, Wait returns immediately with the context error,
// but the handler and its background goroutines may still be running. This means
// that if the context cancels, the handler could still be active.
//
// Wait should be called after Start() to block until the handler terminates.
// It returns nil if the handler stopped gracefully, or an error if it stopped
// due to a failure.
func (m *MuxPortForward) Wait(ctx context.Context) error {
	m.init()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.errctx.Done():
		err := m.errgrp.Wait()
		if err != errMuxStopRequested {
			return err
		}
		return nil
	}
}

func (m *MuxPortForward) handleDialRequests(ctx context.Context, smuxSession *smux.Session) error {
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case dialRequest := <-m.dialRequests:
			m.Log.Debug("opening smux stream")
			smuxConn, err := smuxSession.OpenStream()
			select {
			case dialRequest.response <- muxDialResponse{conn: smuxConn, err: err}:
				m.Log.Info("smux connection response sent", "error", err)
			default:
				m.Log.Info("smux connection response aborted")
			}
		}
	}
}

// Dial creates a new multiplexed connection over the SSM session.
// This method requests a new smux stream from the multiplexing session and
// returns it as a net.Conn. The connection can be used for bidirectional
// communication and will be automatically cleaned up when closed.
//
// This method blocks until a connection is established, the context is cancelled,
// or the handler has stopped. Multiple concurrent calls to Dial are supported.
//
// Returns ErrFailedDial if the connection cannot be established.
func (m *MuxPortForward) Dial(ctx context.Context) (net.Conn, error) {
	m.init()

	// We need to start listening for the response *before* we send the request.
	// This ensures that the dial handler doesn't try to return a response, before
	// we're listening. This is important, because if we're not listening on the
	// channel, then it will figure we've aborted the request and clean up any resources.
	request := muxDialRequest{response: make(chan muxDialResponse)}
	responseC := make(chan muxDialResponse)
	go func() {
		select {
		case <-ctx.Done():
			responseC <- muxDialResponse{err: ctx.Err()}
		case <-m.errctx.Done():
			responseC <- muxDialResponse{err: fmt.Errorf("%w: %w", ErrFailedDial, context.Cause(m.errctx))}
		case response := <-request.response:
			responseC <- response
		}
	}()

	// Send the dial request
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.errctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrFailedDial, context.Cause(m.errctx))
	case m.dialRequests <- request:
	}

	response := <-responseC
	if response.err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFailedDial, response.err)
	}
	return response.conn, nil
}
