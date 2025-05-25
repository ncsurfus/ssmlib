# Acknowledgments

This project was developed with inspiration and reference from several existing AWS Systems Manager (SSM) implementations. I gratefully acknowledge the following projects and their contributors:

## Primary References

### AWS Session Manager Plugin
- **Repository**: [aws/session-manager-plugin](https://github.com/aws/session-manager-plugin)
- **License**: Apache License 2.0
- **Contribution**: Official AWS implementation providing the canonical reference for the SSM WebSocket protocol, message formats, and session management patterns.

### SSM Session Client
- **Repository**: [mmmorris1975/ssm-session-client](https://github.com/mmmorris1975/ssm-session-client)
- **License**: MIT License
- **Contribution**: Alternative Go implementation that provided valuable insights into Go-specific patterns for SSM session management.

**Specific areas of reference:**
- Go idioms for WebSocket connection handling
- Session lifecycle management patterns
- Error handling and context cancellation approaches
- Testing strategies for SSM functionality
- Copied code for message parsing (Thank You!).
