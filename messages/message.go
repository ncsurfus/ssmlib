package messages

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrCreateTerminalResize = errors.New("failed to create terminal resize message")
	ErrCreateHandshake      = errors.New("failed to create handshake message")
	ErrUnmarshalAck         = errors.New("failed to unmarshal acknowledgement request")
	ErrInvalidAgentMessage  = errors.New("invalid agent message")
)

const agentMsgHeaderLen = 116 // the binary size of all AgentMessage fields except payloadLength and Payload

// AgentMessage is the structural representation of the binary format of an SSM agent message use for communication
// between local clients (like this), and remote agents installed on EC2 instances.
// This is the order the fields must appear as on the wire
// REF: https://github.com/aws/amazon-ssm-agent/blob/master/agent/session/contracts/agentmessage.go.
//
//nolint:maligned
type AgentMessage struct {
	headerLength   uint32
	MessageType    MessageType // this is a 32 byte space-padded string on the wire
	SchemaVersion  uint32
	CreatedDate    time.Time // wire format is milliseconds since unix epoch (uint64), value set to time.Now() in NewAgentMessage
	SequenceNumber int64
	Flags          AgentMessageFlag // REF: https://github.com/aws/amazon-ssm-agent/blob/master/agent/session/contracts/agentmessage.go
	MessageID      uuid.UUID        // 16 byte UUID, auto-generated in NewAgentMessage
	PayloadDigest  []byte           // SHA256 digest, value calculated in MarshalBinary
	PayloadType    PayloadType      // REF: https://github.com/aws/amazon-ssm-agent/blob/master/agent/session/contracts/model.go
	PayloadLength  uint32           // value calculated in MarshalBinary
	Payload        []byte
}

// NewAgentMessage creates an AgentMessage ready to load with payload.
func NewAgentMessage() *AgentMessage {
	return &AgentMessage{
		headerLength:  agentMsgHeaderLen,
		SchemaVersion: 1,
		CreatedDate:   time.Now(),
		MessageID:     uuid.New(),
	}
}

func NewStreamMessage(data []byte) *AgentMessage {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = Output
	msg.Payload = data
	return msg
}

func NewSizeMessage(width int, height int) (*AgentMessage, error) {
	input := map[string]int{"cols": width, "rows": height}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCreateTerminalResize, err)
	}

	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = Size
	msg.Payload = payload

	return msg, nil
}

func NewTerminateSessionMessage() *AgentMessage {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Fin
	msg.PayloadType = Flag

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(TerminateSession))
	msg.Payload = buf
	return msg
}

func NewAcknowledgementMessage(sequenceNumber int64, data []byte) *AgentMessage {
	msg := NewAgentMessage()
	msg.MessageType = Acknowledge
	msg.SequenceNumber = sequenceNumber
	msg.Flags = Ack
	msg.PayloadType = Undefined
	msg.Payload = data

	return msg
}

func NewHandshakeResponse(version string, actions []RequestedClientAction) (*AgentMessage, error) {
	// The version matters, because it's used to determine settings by the handlers.
	handshake := HandshakeResponsePayload{
		ClientVersion:          version,
		ProcessedClientActions: make([]ProcessedClientAction, len(actions)),
		Errors:                 []string{},
	}

	// TODO: We're going to hardcode this for now, but at some point we should make sure the handler
	// does in fact support the session type.
	// TODO Support KMS Encryption!
	for i, a := range actions {
		switch a.ActionType {
		case SessionType:
			handshake.ProcessedClientActions[i] = ProcessedClientAction{
				ActionType:   a.ActionType,
				ActionStatus: Success,
			}
		default:
			handshake.ProcessedClientActions[i] = ProcessedClientAction{
				ActionType:   a.ActionType,
				ActionResult: json.RawMessage([]byte(strconv.Itoa(int(Unsupported)))),
				Error:        fmt.Sprintf("Unsupported action %s", a.ActionType),
			}
			handshake.Errors = append(handshake.Errors, handshake.ProcessedClientActions[i].Error)
		}
	}

	payload, err := json.Marshal(handshake)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCreateHandshake, err)
	}

	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = HandshakeResponse
	msg.Payload = payload

	return msg, nil
}

func ParseAcknowledgment(msg *AgentMessage) (int64, error) {
	ack := struct {
		AcknowledgedMessageSequenceNumber int64 `json:"AcknowledgedMessageSequenceNumber"`
	}{}
	if err := json.Unmarshal(msg.Payload, &ack); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnmarshalAck, err)
	}

	return ack.AcknowledgedMessageSequenceNumber, nil
}

// ValidateMessage performs checks on the values of the AgentMessage to ensure they are sane.
func (m *AgentMessage) ValidateMessage() error {
	// close_channel message header is 112 bytes
	if m.headerLength > agentMsgHeaderLen || m.headerLength < agentMsgHeaderLen-4 {
		return fmt.Errorf("%w: invalid header length", ErrInvalidAgentMessage)
	}

	if m.SchemaVersion < 1 {
		return fmt.Errorf("%w: invalid schema version", ErrInvalidAgentMessage)
	}

	// this seems to be a good minimum number after checking the SSM agent source code
	if len(m.MessageType) < 10 {
		return fmt.Errorf("%w: invalid type", ErrInvalidAgentMessage)
	}

	if m.CreatedDate.IsZero() {
		return fmt.Errorf("%w: invalid date", ErrInvalidAgentMessage)
	}

	if len(m.MessageID[:]) != 16 {
		return fmt.Errorf("%w: invalid id", ErrInvalidAgentMessage)
	}

	if len(m.Payload) != int(m.PayloadLength) {
		return fmt.Errorf("%w: payload length mismatch (want %d, got %d)", ErrInvalidAgentMessage, m.PayloadLength, len(m.Payload))
	}

	if !bytes.Equal(m.sha256PayloadDigest(), m.PayloadDigest) {
		return fmt.Errorf("%w: payload digest mismatch", ErrInvalidAgentMessage)
	}

	return nil
}

// UnmarshalBinary reads the wire format data and updates the fields in the method receiver.  Satisfies the
// encoding.BinaryUnmarshaler interface.
func (m *AgentMessage) UnmarshalBinary(data []byte) error {
	m.headerLength = binary.BigEndian.Uint32(data)
	m.MessageType = parseMessageType(data[4:36])
	m.SchemaVersion = binary.BigEndian.Uint32(data[36:40])
	m.CreatedDate = parseTime(data[40:48])
	m.SequenceNumber = int64(binary.BigEndian.Uint64(data[48:56]))
	m.Flags = AgentMessageFlag(binary.BigEndian.Uint64(data[56:64]))
	m.MessageID = uuid.Must(uuid.FromBytes(formatUUIDBytes(data[64:80])))
	m.PayloadDigest = data[80 : 80+sha256.Size]

	// The channel_closed message has a header length of 112 bytes, assuming this is what's dropped
	if m.headerLength == agentMsgHeaderLen {
		m.PayloadType = PayloadType(binary.BigEndian.Uint32(data[112:m.headerLength]))
	}

	payloadLenEnd := m.headerLength + 4
	m.PayloadLength = binary.BigEndian.Uint32(data[m.headerLength:payloadLenEnd])
	m.Payload = data[payloadLenEnd : payloadLenEnd+m.PayloadLength]

	return m.ValidateMessage()
}

// MarshalBinary converts the fields in the method receiver to the expected wire format used by the websocket
// protocol with the SSM messaging service.  Satisfies the encoding.BinaryMarshaler interface.
func (m *AgentMessage) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)

	m.sha256PayloadDigest()
	m.PayloadLength = uint32(len(m.Payload))

	if err := m.ValidateMessage(); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, m.headerLength); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.convertMessageType()); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.SchemaVersion); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, time.Duration(m.CreatedDate.UnixNano()).Milliseconds()); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.SequenceNumber); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.Flags); err != nil {
		return nil, err
	}
	// []byte values are written directly (no endian-ness), but for consistency's sake ...
	if err := binary.Write(buf, binary.BigEndian, formatUUIDBytes(m.MessageID[:])); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.PayloadDigest[:sha256.Size]); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.PayloadType); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.PayloadLength); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, m.Payload); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (m *AgentMessage) String() string {
	sb := new(strings.Builder)
	sb.WriteString("AgentMessage{")
	sb.WriteString(fmt.Sprintf("TYPE: %s, ", m.MessageType))
	sb.WriteString(fmt.Sprintf("SCHEMA VERSION: %d, ", m.SchemaVersion))
	sb.WriteString(fmt.Sprintf("SEQUENCE: %d, ", m.SequenceNumber))
	sb.WriteString(fmt.Sprintf("MESSAGE ID: %s, ", m.MessageID))
	sb.WriteString(fmt.Sprintf("PAYLOAD TYPE: %d, ", m.PayloadType))
	sb.WriteString(fmt.Sprintf("PAYLOAD LENGTH: %d", m.PayloadLength))
	sb.WriteString(fmt.Sprintln("}"))
	return sb.String()
}

func (m *AgentMessage) convertMessageType() []byte {
	var msgTypeLen = 32 // per spec
	var msgType []byte

	if len(m.MessageType) >= msgTypeLen {
		msgType = []byte(m.MessageType)
	} else {
		msgType = []byte(m.MessageType)
		msgType = append(msgType, bytes.Repeat([]byte{0x20}, msgTypeLen-len(m.MessageType))...)
	}

	return msgType[:msgTypeLen]
}

func (m *AgentMessage) sha256PayloadDigest() []byte {
	digest := sha256.New()
	_, _ = digest.Write(m.Payload)
	m.PayloadDigest = digest.Sum(nil)
	return m.PayloadDigest
}

// channel_closed message type is nul padded, others are space padded.  Handle both.
func parseMessageType(data []byte) MessageType {
	return MessageType(bytes.TrimSpace(bytes.TrimRight(data, string(rune(0x00)))))
}

func parseTime(data []byte) time.Time {
	ts := binary.BigEndian.Uint64(data)
	d := time.Duration(ts) * time.Millisecond
	return time.Unix(0, d.Nanoseconds())
}

func formatUUIDBytes(data []byte) []byte {
	return append(data[8:], data[:8]...)
}
