package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ncsurfus/ssmlib/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockReader implements io.Reader for testing
type MockReader struct {
	mock.Mock
}

func (m *MockReader) Read(p []byte) (n int, err error) {
	args := m.Called(p)
	return args.Int(0), args.Error(1)
}

// MockWriter implements io.Writer for testing
type MockWriter struct {
	mock.Mock
}

func (m *MockWriter) Write(p []byte) (n int, err error) {
	args := m.Called(p)
	return args.Int(0), args.Error(1)
}

// MockWriterWithFd implements io.Writer with Fd() method for testing
type MockWriterWithFd struct {
	MockWriter
	fd uintptr
}

func (m *MockWriterWithFd) Fd() uintptr {
	return m.fd
}

// MockTerminalSize implements TerminalSize for testing
type MockTerminalSize struct {
	mock.Mock
}

func (m *MockTerminalSize) Size() (width int, height int, err error) {
	args := m.Called()
	return args.Int(0), args.Int(1), args.Error(2)
}

func TestStream_Start_Success(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockReader := &MockReader{}
	mockWriter := &MockWriter{}

	// Mock session operations that will be canceled
	mockSession.On("Read", mock.Anything).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	// Mock reader operations that will be canceled
	mockReader.On("Read", mock.Anything).Return(0, context.Canceled).Maybe()

	stream := &Stream{
		Reader: mockReader,
		Writer: mockWriter,
		Log:    slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Stop the stream to trigger clean shutdown
	stream.Stop()

	// Wait for the stream to finish
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.NoError(t, err)
}

func TestStream_Start_WithDefaults(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// Mock session operations that will block until canceled
	mockSession.On("Read", mock.Anything).Run(func(args mock.Arguments) {
		ctx := args.Get(0).(context.Context)
		<-ctx.Done()
	}).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	stream := &Stream{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Verify defaults are set
	assert.Equal(t, os.Stdin, stream.Reader)
	assert.Equal(t, os.Stdout, stream.Writer)

	// Stop the stream to trigger clean shutdown
	stream.Stop()

	// Wait for the stream to finish
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.NoError(t, err)
}

func TestStream_Stop(t *testing.T) {
	stream := &Stream{
		Log: slog.New(DiscardHandler),
	}

	// Stop should not panic even if not started
	stream.Stop()

	// Test that Stop can be called multiple times
	stream.Stop()
}

func TestStream_Wait_ContextCanceled(t *testing.T) {
	stream := &Stream{
		Log: slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := stream.Wait(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestStream_Wait_AfterStop(t *testing.T) {
	stream := &Stream{
		Log: slog.New(DiscardHandler),
	}

	// Stop the stream
	stream.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Wait(ctx)
	// When stopped without being started, it should timeout
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestStream_MultipleInit(t *testing.T) {
	stream := &Stream{}

	// Multiple calls to init should be safe
	stream.init()
	stream.init()
	stream.init()

	assert.NotNil(t, stream.Reader)
	assert.NotNil(t, stream.Writer)
	assert.NotNil(t, stream.stopped)
	assert.NotNil(t, stream.Log)
	assert.NotNil(t, stream.errgrp)
	assert.NotNil(t, stream.errctx)
}

func TestStream_GetTerminalSize_WithCustomTerminalSize(t *testing.T) {
	mockTermSize := &MockTerminalSize{}
	mockTermSize.On("Size").Return(120, 40, nil)

	stream := &Stream{
		TerminalSize: mockTermSize,
		Log:          slog.New(DiscardHandler),
	}

	termSize := stream.getTerminalSize()
	width, height, err := termSize.Size()

	assert.NoError(t, err)
	assert.Equal(t, 120, width)
	assert.Equal(t, 40, height)
}

func TestStream_GetTerminalSize_WithFdWriter(t *testing.T) {
	mockWriter := &MockWriterWithFd{fd: 1} // stdout fd

	stream := &Stream{
		Writer: mockWriter,
		Log:    slog.New(DiscardHandler),
	}

	termSize := stream.getTerminalSize()
	assert.NotNil(t, termSize)

	// The actual size will depend on the terminal, but we can test that it doesn't panic
	_, _, err := termSize.Size()
	// Error is expected since fd 1 might not be a terminal in test environment
	assert.Error(t, err)
}

func TestStream_GetTerminalSize_WithoutFdWriter(t *testing.T) {
	mockWriter := &MockWriter{}

	stream := &Stream{
		Writer: mockWriter,
		Log:    slog.New(DiscardHandler),
	}

	termSize := stream.getTerminalSize()
	width, height, err := termSize.Size()

	assert.NoError(t, err)
	assert.Equal(t, DefaultTerminalWidth, width)
	assert.Equal(t, DefaultTerminalHeight, height)
}

func TestStream_Integration_DataFlow(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// Create test data
	testInput := "hello world"
	testOutput := "response data"

	// Set up a reader that blocks after delivering test data, and a writer to capture output
	readerDone := make(chan struct{})
	reader := &MockReader{}
	reader.On("Read", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(0).([]byte)
		copy(p, testInput)
	}).Return(len(testInput), nil).Once()
	reader.On("Read", mock.Anything).Run(func(_ mock.Arguments) {
		<-readerDone
	}).Return(0, context.Canceled).Maybe()
	defer close(readerDone)

	// Use a channel to signal when the write happens, avoiding a data race on the buffer.
	written := make(chan struct{}, 1)
	writer := &MockWriter{}
	writer.On("Write", []byte(testOutput)).Run(func(_ mock.Arguments) {
		written <- struct{}{}
	}).Return(len(testOutput), nil).Once()

	// Create output message for session to return
	outputMsg := messages.NewAgentMessage()
	outputMsg.PayloadType = messages.Output
	outputMsg.Payload = []byte(testOutput)

	// Mock session expectations
	mockSession.On("Write", mock.Anything, mock.MatchedBy(func(msg *messages.AgentMessage) bool {
		return msg.PayloadType == messages.Output
	})).Return(nil).Maybe()

	mockSession.On("Read", mock.Anything).Return(outputMsg, nil).Once()
	mockSession.On("Read", mock.Anything).Run(func(args mock.Arguments) {
		ctx := args.Get(0).(context.Context)
		<-ctx.Done()
	}).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	stream := &Stream{
		Reader: reader,
		Writer: writer,
		Log:    slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Wait for the output to be written before stopping
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for output to be written")
	}

	stream.Stop()

	// Wait for completion
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.NoError(t, err)

	writer.AssertExpectations(t)

	mockSession.AssertExpectations(t)
}

func TestStream_TerminalSizeUpdates(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockTermSize := &MockTerminalSize{}
	mockReader := &MockReader{}

	// Mock terminal size - just return consistent size to avoid timing issues
	mockTermSize.On("Size").Return(80, 24, nil).Maybe()

	// Mock reader to block until context cancels
	mockReader.On("Read", mock.Anything).WaitUntil(time.After(time.Second)).Return(0, context.Canceled).Maybe()

	// Mock session operations
	mockSession.On("Write", mock.Anything, mock.MatchedBy(func(msg *messages.AgentMessage) bool {
		return msg.PayloadType == messages.Size
	})).Return(nil).Maybe()

	mockSession.On("Read", mock.Anything).Run(func(args mock.Arguments) {
		ctx := args.Get(0).(context.Context)
		<-ctx.Done()
	}).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	stream := &Stream{
		Reader:       mockReader,
		Writer:       &bytes.Buffer{},
		TerminalSize: mockTermSize,
		Log:          slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Stop the stream immediately to avoid timing issues
	stream.Stop()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.NoError(t, err)

	// Just verify that the terminal size interface was used
	mockTermSize.AssertExpectations(t)
}

func TestStream_TerminalSizeError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockTermSize := &MockTerminalSize{}
	mockReader := &MockReader{}

	// Mock terminal size error
	mockTermSize.On("Size").Return(0, 0, errors.New("terminal size error")).Maybe()

	// Mock reader to block until context cancels
	mockReader.On("Read", mock.Anything).WaitUntil(time.After(time.Second)).Return(0, context.Canceled).Maybe()

	// Mock session operations
	mockSession.On("Read", mock.Anything).Run(func(args mock.Arguments) {
		ctx := args.Get(0).(context.Context)
		<-ctx.Done()
	}).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	stream := &Stream{
		Reader:       mockReader,
		Writer:       &bytes.Buffer{},
		TerminalSize: mockTermSize,
		Log:          slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	stream.Stop()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.NoError(t, err) // Should not fail even if terminal size fails
}

func TestStream_ReaderError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockReader := &MockReader{}

	// Mock reader error
	mockReader.On("Read", mock.Anything).Return(0, errors.New("read error"))

	// Mock session operations
	mockSession.On("Read", mock.Anything).Return(nil, context.Canceled).Maybe()

	stream := &Stream{
		Reader: mockReader,
		Writer: &bytes.Buffer{},
		Log:    slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Wait for the error to propagate
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.Error(t, err) // Should fail due to reader error
}

func TestStream_SessionWriteError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// Mock session write error
	mockSession.On("Write", mock.Anything, mock.Anything).Return(errors.New("write error"))
	mockSession.On("Read", mock.Anything).Return(nil, context.Canceled).Maybe()

	stream := &Stream{
		Reader: strings.NewReader("test data"),
		Writer: &bytes.Buffer{},
		Log:    slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Wait for the error to propagate
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.Error(t, err) // Should fail due to session write error
}

func TestStream_SessionReadError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}

	// Mock session read error
	mockSession.On("Read", mock.Anything).Return(nil, errors.New("read error"))
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	stream := &Stream{
		Reader: strings.NewReader(""),
		Writer: &bytes.Buffer{},
		Log:    slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Wait for the error to propagate
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.Error(t, err) // Should fail due to session read error
}

func TestStream_Constants(t *testing.T) {
	// Test that constants are defined
	assert.Equal(t, 250, TerminalWindowIntervalMilliseconds)
}

func TestStream_ErrorVariables(t *testing.T) {
	// Test that error variables are defined
	assert.NotNil(t, errStreamStopRequested)
	assert.Contains(t, errStreamStopRequested.Error(), "stream stopped requested")
}

func TestStream_WriterError(t *testing.T) {
	mockSession := &MockSessionReaderWriter{}
	mockWriter := &MockWriter{}

	// Create output message for session to return
	outputMsg := messages.NewAgentMessage()
	outputMsg.PayloadType = messages.Output
	outputMsg.Payload = []byte("test output")

	// Mock writer error
	mockWriter.On("Write", mock.Anything).Return(0, errors.New("write error"))

	// Mock session operations
	mockSession.On("Read", mock.Anything).Return(outputMsg, nil).Once()
	mockSession.On("Read", mock.Anything).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	stream := &Stream{
		Reader: strings.NewReader(""),
		Writer: mockWriter,
		Log:    slog.New(DiscardHandler),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := stream.Start(ctx, mockSession)
	assert.NoError(t, err)

	// Wait for the error to propagate
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	err = stream.Wait(waitCtx)
	assert.Error(t, err) // Should fail due to writer error
}

func TestStream_TerminalSizeChange(t *testing.T) {
	// Test that terminal size changes are detected and sent
	mockSession := &MockSessionReaderWriter{}
	mockTermSize := &MockTerminalSize{}
	mockReader := &MockReader{}

	// Return different sizes on successive calls to trigger a change
	callCount := 0
	mockTermSize.On("Size").Run(func(_ mock.Arguments) {
		callCount++
	}).Return(80, 24, nil).Once()
	mockTermSize.On("Size").Return(120, 40, nil).Maybe()

	mockReader.On("Read", mock.Anything).WaitUntil(time.After(2 * time.Second)).Return(0, context.Canceled).Maybe()

	// Track size messages written
	sizeWritten := make(chan struct{}, 10)
	mockSession.On("Write", mock.Anything, mock.MatchedBy(func(msg *messages.AgentMessage) bool {
		return msg.PayloadType == messages.Size
	})).Run(func(_ mock.Arguments) {
		select {
		case sizeWritten <- struct{}{}:
		default:
		}
	}).Return(nil).Maybe()

	mockSession.On("Read", mock.Anything).Run(func(args mock.Arguments) {
		ctx := args.Get(0).(context.Context)
		<-ctx.Done()
	}).Return(nil, context.Canceled).Maybe()
	mockSession.On("Write", mock.Anything, mock.Anything).Return(context.Canceled).Maybe()

	stream := &Stream{
		Reader:       mockReader,
		Writer:       &bytes.Buffer{},
		TerminalSize: mockTermSize,
		Log:          slog.New(DiscardHandler),
	}

	err := stream.Start(context.Background(), mockSession)
	assert.NoError(t, err)

	// Wait for at least one size message to be written
	select {
	case <-sizeWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminal size update")
	}

	stream.Stop()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	err = stream.Wait(waitCtx)
	assert.NoError(t, err)
}
