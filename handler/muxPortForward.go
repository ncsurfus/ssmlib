package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/ncsurfus/ssmlib/session"
	"github.com/ncsurfus/ssmlib/version"
	"github.com/xtaci/smux"
	"golang.org/x/sync/errgroup"
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

type MuxPortForward struct {
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
			m.Log = slog.New(slog.DiscardHandler)
		}
		m.errgrp, m.errctx = errgroup.WithContext(context.Background())
	})

}

func (m *MuxPortForward) signalStop() {
	m.stopOnce.Do(func() { close(m.stopped) })
}

func (m *MuxPortForward) Start(ctx context.Context, session session.ReaderWriter) error {
	m.init()

	// Perform the handshake.
	handshakeResult, err := PerformHandshake(ctx, m.Log, session)
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	m.Log.Info("Handshake complete with remote agent.", "RemoteVersion", handshakeResult.RemoteVersion)
	if !MuxSupported.SupportsVersion(handshakeResult.RemoteVersion) {
		return fmt.Errorf("the remote ssm agent version, %v, does not support mux", handshakeResult.RemoteVersion)
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
		return fmt.Errorf("failed to create smux client")
	}

	// We should note that we should never return nil from the errgroup, except
	// when the errctx is Done()

	// Cleanup resources
	m.errgrp.Go(func() error {
		<-m.errctx.Done()
		smuxSession.Close()
		m.signalStop()
		return nil
	})

	// Handle a Stop() event.
	m.errgrp.Go(func() error {
		// The cleanup resources goroutine will ensure stop gets closed.
		<-m.stopped
		return io.EOF
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

func (m *MuxPortForward) Stop() {
	m.init()
	defer m.signalStop()
}

func (m *MuxPortForward) Wait(ctx context.Context) error {
	m.init()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait cancelled: %w", ctx.Err())
	case <-m.errctx.Done():
		err := m.errgrp.Wait()
		if err != io.EOF {
			return err
		}
		return nil
	}
}

func (m *MuxPortForward) handleDialRequests(ctx context.Context, smuxSession *smux.Session) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to get dial request, context cancelled: %w", ctx.Err())
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
			responseC <- muxDialResponse{err: fmt.Errorf("failed to get dial response: %w", ctx.Err())}
		case <-m.stopped:
			responseC <- muxDialResponse{err: fmt.Errorf("failed to request dial: mux is stopped")}
		case response := <-request.response:
			responseC <- response
		}
	}()

	// Send the dial request
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("failed to request dial: %w", ctx.Err())
	case <-m.stopped:
		return nil, fmt.Errorf("failed to request dial: mux is stopped")
	case m.dialRequests <- request:
	}

	response := <-responseC
	if response.err != nil {
		return nil, fmt.Errorf("failed to dial: %w", response.err)
	}
	return response.conn, nil
}
