# SSMLib

A Go library for establishing and managing AWS Systems Manager (SSM) sessions programmatically. This library provides low-level primitives for building SSM-based applications, focusing on library functionality rather than end-user CLI tools.

## Features

- **Session Management**: Establish and manage SSM sessions with AWS EC2 instances
- **Multiple Handler Types**:
  - **Stream Handler**: Interactive shell sessions with terminal support
  - **MuxPortForward Handler**: Multiplexed port forwarding
- **Pluggable Architecture**: Implement custom handlers for specialized use cases
- **Terminal Support**: Automatic terminal size detection and updates
- **Version Compatibility**: Feature flag system for different SSM agent versions
- **Comprehensive Testing**: Unit tests for core functionality

## Installation

```bash
go get github.com/ncsurfus/ssmlib
```

## Quick Start

### Basic Interactive Session

```go
package main

import (
    "context"
    "log"
    
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/ssm"
    "github.com/ncsurfus/ssmlib"
    "github.com/ncsurfus/ssmlib/handler"
)

func main() {
    // Load AWS configuration
    cfg, err := config.LoadDefaultConfig(context.TODO())
    if err != nil {
        log.Fatal(err)
    }

    // Create SSM client
    client := ssm.NewFromConfig(cfg)

    // Start SSM session
    resp, err := client.StartSession(context.TODO(), &ssm.StartSessionInput{
        Target: aws.String("i-1234567890abcdef0"),
        DocumentName: aws.String("AWS-StartInteractiveCommand"),
        Parameters: map[string][]string{
            "command": {"bash"},
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    // Create and start session
    session := &ssmlib.Session{
        Handler: &handler.Stream{},
    }

    err = session.Start(context.Background(), *resp.StreamUrl, *resp.TokenValue)
    if err != nil {
        log.Fatal(err)
    }
    defer session.Stop()

    // Wait for session to complete
    err = session.Wait(context.Background())
    if err != nil {
        log.Printf("Session ended: %v", err)
    }
}
```

### Port Forwarding with Multiplexing

```go
// Create a multiplexed port forwarding handler
muxHandler := &handler.MuxPortForward{}

session := &ssmlib.Session{
    Handler: muxHandler,
}

err = session.Start(context.Background(), streamURL, tokenValue)
if err != nil {
    log.Fatal(err)
}

// Dial connections through the mux
conn, err := muxHandler.Dial(context.Background())
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

// Use conn as a regular net.Conn
```

## Architecture

### Core Components

- **Session**: Main session manager that handles WebSocket connections and message routing
- **Handler Interface**: Pluggable handlers for different session types
- **Messages**: Protocol message definitions and serialization
- **Version**: Feature flag system for SSM agent version compatibility

### Handler Types

#### Stream Handler
- Interactive terminal sessions
- Automatic terminal size detection
- Bidirectional data streaming
- Configurable input/output sources

#### MuxPortForward Handler
- Multiplexed connections using smux
- Version-aware feature detection
- Connection pooling and management
- Network-compatible interface

### Message Protocol

The library implements the AWS SSM WebSocket protocol with:
- Message acknowledgments and retransmission
- Sequence number management
- Flow control and backpressure handling
- Graceful connection termination

## Configuration

### Session Options

There isn't anything special that `Session` does with a `Handler` except lifecycle management by invoking `Start()`, `Stop()`, and `Wait()` on the handler when the equivalent methods are
called on the `Session`. This is purely for convienence.
If a `Handler` isn't provided, you can still interact with the `Session` with your own code or by managing lifecycle of a handler yourself. Logging is primarly useful for debugging, but
otherwise should probably be turned off.

```go
session := &ssmlib.Session{
    Handler: yourHandler,           // Default: NopHandler
    Dialer:  customWebSocketDialer, // Optional
    Log:     customLogger,          // Optional
}
```

### Stream Handler Options

```go
streamHandler := &handler.Stream{
    Reader:       customReader,     // Default: os.Stdin
    Writer:       customWriter,     // Default: os.Stdout
    TerminalSize: customSizeFunc,   // Optional
    Log:          customLogger,     // Optional
}
```

### MuxPortForward Handler Options

```go
muxHandler := &handler.MuxPortForward{
    Log: customLogger, // Optional
}
```

## Custom Handlers

Implement the `Handler` interface to create custom session handlers:

```go
type Handler interface {
    Start(ctx context.Context, session handler.SessionReaderWriter) error
    Stop()
    Wait(ctx context.Context) error
}
```

The `SessionReaderWriter` interface provides:
- `Read(ctx context.Context) (*messages.AgentMessage, error)`
- `Write(ctx context.Context, message *messages.AgentMessage) error`

## Testing

Run the test suite:

```bash
go test ./...
```

Run tests with verbose output:

```bash
go test -v ./...
```

Run tests with race detection and coverage:

```bash
go test -v -race -coverprofile=coverage.out ./...
```

## Linting

This project uses golangci-lint for code quality checks.

```bash
golangci-lint run
```

Fix auto-fixable issues:

```bash
golangci-lint run --fix
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure all tests pass
5. Submit a pull request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Related Projects

This library was inspired by and references:
- [aws/session-manager-plugin](https://github.com/aws/session-manager-plugin) - Official AWS Session Manager plugin
- [mmmorris1975/ssm-session-client](https://github.com/mmmorris1975/ssm-session-client) - Alternative SSM session client

See [ACKNOWLEDGMENTS.md](ACKNOWLEDGMENTS.md) for detailed attribution.
