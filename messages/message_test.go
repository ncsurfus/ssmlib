package messages

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAgentMessage(t *testing.T) {
	msg := NewAgentMessage()

	assert.NotNil(t, msg)
	assert.Equal(t, uint32(agentMsgHeaderLen), msg.headerLength)
	assert.Equal(t, uint32(1), msg.SchemaVersion)
	assert.False(t, msg.CreatedDate.IsZero())
	assert.NotEqual(t, uuid.Nil, msg.MessageID)
	assert.WithinDuration(t, time.Now(), msg.CreatedDate, time.Second)
}

func TestNewStreamMessage(t *testing.T) {
	testData := []byte("test stream data")
	msg := NewStreamMessage(testData)

	assert.NotNil(t, msg)
	assert.Equal(t, InputStreamData, msg.MessageType)
	assert.Equal(t, Data, msg.Flags)
	assert.Equal(t, Output, msg.PayloadType)
	assert.Equal(t, testData, msg.Payload)
	assert.Equal(t, uint32(agentMsgHeaderLen), msg.headerLength)
	assert.Equal(t, uint32(1), msg.SchemaVersion)
	assert.False(t, msg.CreatedDate.IsZero())
	assert.NotEqual(t, uuid.Nil, msg.MessageID)
}

func TestNewSizeMessage_Success(t *testing.T) {
	width, height := 120, 40
	msg, err := NewSizeMessage(width, height)

	assert.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Equal(t, InputStreamData, msg.MessageType)
	assert.Equal(t, Data, msg.Flags)
	assert.Equal(t, Size, msg.PayloadType)

	// Verify payload contains the correct size data
	var sizeData map[string]int
	err = json.Unmarshal(msg.Payload, &sizeData)
	assert.NoError(t, err)
	assert.Equal(t, width, sizeData["cols"])
	assert.Equal(t, height, sizeData["rows"])
}

func TestNewSizeMessage_ZeroValues(t *testing.T) {
	msg, err := NewSizeMessage(0, 0)

	assert.NoError(t, err)
	assert.NotNil(t, msg)

	var sizeData map[string]int
	err = json.Unmarshal(msg.Payload, &sizeData)
	assert.NoError(t, err)
	assert.Equal(t, 0, sizeData["cols"])
	assert.Equal(t, 0, sizeData["rows"])
}

func TestNewSizeMessage_LargeValues(t *testing.T) {
	width, height := 9999, 9999
	msg, err := NewSizeMessage(width, height)

	assert.NoError(t, err)
	assert.NotNil(t, msg)

	var sizeData map[string]int
	err = json.Unmarshal(msg.Payload, &sizeData)
	assert.NoError(t, err)
	assert.Equal(t, width, sizeData["cols"])
	assert.Equal(t, height, sizeData["rows"])
}

func TestNewTerminateSessionMessage(t *testing.T) {
	msg := NewTerminateSessionMessage()

	assert.NotNil(t, msg)
	assert.Equal(t, InputStreamData, msg.MessageType)
	assert.Equal(t, Fin, msg.Flags)
	assert.Equal(t, Flag, msg.PayloadType)
	assert.Len(t, msg.Payload, 4)

	// Verify the payload contains the terminate session flag
	assert.Equal(t, []byte{0, 0, 0, 2}, msg.Payload) // TerminateSession = 2 in big endian
}

func TestNewAcknowledgementMessage(t *testing.T) {
	sequenceNumber := int64(12345)
	testData := []byte("ack data")
	msg := NewAcknowledgementMessage(sequenceNumber, testData)

	assert.NotNil(t, msg)
	assert.Equal(t, Acknowledge, msg.MessageType)
	assert.Equal(t, sequenceNumber, msg.SequenceNumber)
	assert.Equal(t, Ack, msg.Flags)
	assert.Equal(t, Undefined, msg.PayloadType)
	assert.Equal(t, testData, msg.Payload)
}

func TestNewAcknowledgementMessage_EmptyData(t *testing.T) {
	sequenceNumber := int64(0)
	msg := NewAcknowledgementMessage(sequenceNumber, nil)

	assert.NotNil(t, msg)
	assert.Equal(t, Acknowledge, msg.MessageType)
	assert.Equal(t, sequenceNumber, msg.SequenceNumber)
	assert.Equal(t, Ack, msg.Flags)
	assert.Equal(t, Undefined, msg.PayloadType)
	assert.Nil(t, msg.Payload)
}

func TestNewHandshakeResponse_Success(t *testing.T) {
	version := "1.0.0"
	actions := []RequestedClientAction{
		{ActionType: SessionType},
	}

	msg, err := NewHandshakeResponse(version, actions)

	assert.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Equal(t, InputStreamData, msg.MessageType)
	assert.Equal(t, Data, msg.Flags)
	assert.Equal(t, HandshakeResponse, msg.PayloadType)

	// Verify payload structure
	var response HandshakeResponsePayload
	err = json.Unmarshal(msg.Payload, &response)
	assert.NoError(t, err)
	assert.Equal(t, version, response.ClientVersion)
	assert.Len(t, response.ProcessedClientActions, 1)
	assert.Equal(t, SessionType, response.ProcessedClientActions[0].ActionType)
	assert.Equal(t, Success, response.ProcessedClientActions[0].ActionStatus)
	assert.Empty(t, response.Errors)
}

func TestNewHandshakeResponse_UnsupportedAction(t *testing.T) {
	version := "1.0.0"
	actions := []RequestedClientAction{
		{ActionType: KMSEncryption},
	}

	msg, err := NewHandshakeResponse(version, actions)

	assert.NoError(t, err)
	assert.NotNil(t, msg)

	var response HandshakeResponsePayload
	err = json.Unmarshal(msg.Payload, &response)
	assert.NoError(t, err)
	assert.Equal(t, version, response.ClientVersion)
	assert.Len(t, response.ProcessedClientActions, 1)
	assert.Equal(t, KMSEncryption, response.ProcessedClientActions[0].ActionType)
	assert.NotEmpty(t, response.ProcessedClientActions[0].Error)
	assert.Len(t, response.Errors, 1)
}

func TestNewHandshakeResponse_MultipleActions(t *testing.T) {
	version := "1.0.0"
	actions := []RequestedClientAction{
		{ActionType: SessionType},
		{ActionType: KMSEncryption},
	}

	msg, err := NewHandshakeResponse(version, actions)

	assert.NoError(t, err)
	assert.NotNil(t, msg)

	var response HandshakeResponsePayload
	err = json.Unmarshal(msg.Payload, &response)
	assert.NoError(t, err)
	assert.Equal(t, version, response.ClientVersion)
	assert.Len(t, response.ProcessedClientActions, 2)

	// First action should be successful
	assert.Equal(t, SessionType, response.ProcessedClientActions[0].ActionType)
	assert.Equal(t, Success, response.ProcessedClientActions[0].ActionStatus)

	// Second action should be unsupported
	assert.Equal(t, KMSEncryption, response.ProcessedClientActions[1].ActionType)
	assert.NotEmpty(t, response.ProcessedClientActions[1].Error)
	assert.Len(t, response.Errors, 1)
}

func TestNewHandshakeResponse_EmptyActions(t *testing.T) {
	version := "1.0.0"
	actions := []RequestedClientAction{}

	msg, err := NewHandshakeResponse(version, actions)

	assert.NoError(t, err)
	assert.NotNil(t, msg)

	var response HandshakeResponsePayload
	err = json.Unmarshal(msg.Payload, &response)
	assert.NoError(t, err)
	assert.Equal(t, version, response.ClientVersion)
	assert.Empty(t, response.ProcessedClientActions)
	assert.Empty(t, response.Errors)
}

func TestParseAcknowledgment_Success(t *testing.T) {
	sequenceNumber := int64(12345)
	ackData := struct {
		AcknowledgedMessageSequenceNumber int64 `json:"AcknowledgedMessageSequenceNumber"`
	}{
		AcknowledgedMessageSequenceNumber: sequenceNumber,
	}

	payload, err := json.Marshal(ackData)
	require.NoError(t, err)

	msg := &AgentMessage{
		Payload: payload,
	}

	result, err := ParseAcknowledgment(msg)
	assert.NoError(t, err)
	assert.Equal(t, sequenceNumber, result)
}

func TestParseAcknowledgment_InvalidJSON(t *testing.T) {
	msg := &AgentMessage{
		Payload: []byte("invalid json"),
	}

	result, err := ParseAcknowledgment(msg)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnmarshalAck)
	assert.Equal(t, int64(0), result)
}

func TestParseAcknowledgment_EmptyPayload(t *testing.T) {
	msg := &AgentMessage{
		Payload: []byte{},
	}

	result, err := ParseAcknowledgment(msg)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrUnmarshalAck)
	assert.Equal(t, int64(0), result)
}

func TestAgentMessage_ValidateMessage_Success(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.NoError(t, err)
}

func TestAgentMessage_ValidateMessage_InvalidHeaderLength(t *testing.T) {
	msg := NewAgentMessage()
	msg.headerLength = 50 // Too small
	msg.MessageType = InputStreamData
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "invalid header length")
}

func TestAgentMessage_ValidateMessage_InvalidSchemaVersion(t *testing.T) {
	msg := NewAgentMessage()
	msg.SchemaVersion = 0
	msg.MessageType = InputStreamData
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "invalid schema version")
}

func TestAgentMessage_ValidateMessage_InvalidMessageType(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = "short" // Too short
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "invalid type")
}

func TestAgentMessage_ValidateMessage_InvalidDate(t *testing.T) {
	msg := NewAgentMessage()
	msg.CreatedDate = time.Time{} // Zero time
	msg.MessageType = InputStreamData
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "invalid date")
}

func TestAgentMessage_ValidateMessage_PayloadLengthMismatch(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Payload = []byte("test")
	msg.PayloadLength = 10 // Wrong length
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "payload length mismatch")
}

func TestAgentMessage_ValidateMessage_PayloadDigestMismatch(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	// Corrupt the digest
	msg.PayloadDigest[0] ^= 0xff

	err := msg.ValidateMessage()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "payload digest mismatch")
}

func TestFormatUUIDBytes_DoesNotCorruptAdjacentMemory(t *testing.T) {
	// Simulate the layout in a marshaled message: UUID at [64:80], digest at [80:112]
	buf := make([]byte, 120)
	// Fill digest area with known pattern
	for i := 80; i < 112; i++ {
		buf[i] = 0xAA
	}
	// Put some UUID bytes in
	copy(buf[64:80], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})

	formatUUIDBytes(buf[64:80])

	// Verify digest area was NOT corrupted
	for i := 80; i < 112; i++ {
		assert.Equal(t, byte(0xAA), buf[i], "byte at offset %d was corrupted", i)
	}
}

func TestAgentMessage_MarshalBinary_Success(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = Output
	msg.Payload = []byte("test payload")
	msg.SequenceNumber = 123

	data, err := msg.MarshalBinary()
	assert.NoError(t, err)
	assert.NotEmpty(t, data)

	// Verify we can unmarshal it back
	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(data)
	assert.NoError(t, err)
	assert.Equal(t, msg.MessageType, newMsg.MessageType)
	assert.Equal(t, msg.Flags, newMsg.Flags)
	assert.Equal(t, msg.PayloadType, newMsg.PayloadType)
	assert.Equal(t, msg.Payload, newMsg.Payload)
	assert.Equal(t, msg.SequenceNumber, newMsg.SequenceNumber)
}

func TestAgentMessage_MarshalBinary_InvalidMessage(t *testing.T) {
	msg := &AgentMessage{
		headerLength:  50, // Invalid
		MessageType:   "short",
		SchemaVersion: 0,
	}

	data, err := msg.MarshalBinary()
	assert.Error(t, err)
	assert.Nil(t, data)
}

func TestAgentMessage_UnmarshalBinary_Success(t *testing.T) {
	// Create a valid message and marshal it
	originalMsg := NewAgentMessage()
	originalMsg.MessageType = InputStreamData
	originalMsg.Flags = Data
	originalMsg.PayloadType = Output
	originalMsg.Payload = []byte("test payload")
	originalMsg.SequenceNumber = 456

	data, err := originalMsg.MarshalBinary()
	require.NoError(t, err)

	// Unmarshal into a new message
	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(data)
	assert.NoError(t, err)
	assert.Equal(t, originalMsg.MessageType, newMsg.MessageType)
	assert.Equal(t, originalMsg.Flags, newMsg.Flags)
	assert.Equal(t, originalMsg.PayloadType, newMsg.PayloadType)
	assert.Equal(t, originalMsg.Payload, newMsg.Payload)
	assert.Equal(t, originalMsg.SequenceNumber, newMsg.SequenceNumber)
	assert.Equal(t, originalMsg.SchemaVersion, newMsg.SchemaVersion)
}

func TestAgentMessage_UnmarshalBinary_InvalidData(t *testing.T) {
	msg := &AgentMessage{}
	err := msg.UnmarshalBinary([]byte("invalid data"))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "invalid header length")
}

func TestAgentMessage_UnmarshalBinary_TooShort(t *testing.T) {
	msg := &AgentMessage{}
	err := msg.UnmarshalBinary([]byte{1, 2, 3, 4}) // Too short
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "invalid header length")
}

func TestAgentMessage_String(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.SequenceNumber = 789
	msg.PayloadType = Output
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))

	str := msg.String()
	assert.Contains(t, str, "AgentMessage{")
	assert.Contains(t, str, "TYPE: input_stream_data")
	assert.Contains(t, str, "SEQUENCE: 789")
	assert.Contains(t, str, "PAYLOAD TYPE: 1")
	assert.Contains(t, str, "PAYLOAD LENGTH: 4")
	assert.Contains(t, str, "}")
}

func TestAgentMessage_RoundTrip(t *testing.T) {
	// Test various message types for round-trip marshal/unmarshal
	testCases := []struct {
		name        string
		messageType MessageType
		flags       AgentMessageFlag
		payloadType PayloadType
		payload     []byte
	}{
		{
			name:        "InputStreamData",
			messageType: InputStreamData,
			flags:       Data,
			payloadType: Output,
			payload:     []byte("hello world"),
		},
		{
			name:        "Acknowledge",
			messageType: Acknowledge,
			flags:       Ack,
			payloadType: Undefined,
			payload:     []byte(`{"AcknowledgedMessageSequenceNumber": 123}`),
		},
		{
			name:        "ChannelClosed",
			messageType: ChannelClosed,
			flags:       Fin,
			payloadType: Flag,
			payload:     []byte{0, 0, 0, 2},
		},
		{
			name:        "EmptyPayload",
			messageType: InputStreamData,
			flags:       Data,
			payloadType: Output,
			payload:     []byte{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			original := NewAgentMessage()
			original.MessageType = tc.messageType
			original.Flags = tc.flags
			original.PayloadType = tc.payloadType
			original.Payload = tc.payload
			original.SequenceNumber = 12345

			// Marshal
			data, err := original.MarshalBinary()
			assert.NoError(t, err)
			assert.NotEmpty(t, data)

			// Unmarshal
			restored := &AgentMessage{}
			err = restored.UnmarshalBinary(data)
			assert.NoError(t, err)

			// Compare
			assert.Equal(t, original.MessageType, restored.MessageType)
			assert.Equal(t, original.Flags, restored.Flags)
			assert.Equal(t, original.PayloadType, restored.PayloadType)
			assert.Equal(t, original.Payload, restored.Payload)
			assert.Equal(t, original.SequenceNumber, restored.SequenceNumber)
			assert.Equal(t, original.SchemaVersion, restored.SchemaVersion)
			assert.True(t, bytes.Equal(original.PayloadDigest, restored.PayloadDigest))
		})
	}
}

func TestErrorVariables(t *testing.T) {
	// Test that all error variables are defined and have meaningful messages
	assert.NotNil(t, ErrCreateTerminalResize)
	assert.Contains(t, ErrCreateTerminalResize.Error(), "terminal resize")

	assert.NotNil(t, ErrCreateHandshake)
	assert.Contains(t, ErrCreateHandshake.Error(), "handshake")

	assert.NotNil(t, ErrUnmarshalAck)
	assert.Contains(t, ErrUnmarshalAck.Error(), "acknowledgement")

	assert.NotNil(t, ErrInvalidAgentMessage)
	assert.Contains(t, ErrInvalidAgentMessage.Error(), "invalid agent message")
}

func TestConstants(t *testing.T) {
	// Test that the header length constant is defined
	assert.Equal(t, 116, agentMsgHeaderLen)
}

// Test edge cases and boundary conditions
func TestAgentMessage_LargePayload(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = Output
	// Create a large payload (1MB)
	msg.Payload = bytes.Repeat([]byte("A"), 1024*1024)

	data, err := msg.MarshalBinary()
	assert.NoError(t, err)
	assert.NotEmpty(t, data)

	// Unmarshal and verify
	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(data)
	assert.NoError(t, err)
	assert.Equal(t, len(msg.Payload), len(newMsg.Payload))
	assert.True(t, bytes.Equal(msg.Payload, newMsg.Payload))
}

func TestAgentMessage_SpecialCharactersInPayload(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = Output
	// Payload with special characters, unicode, etc.
	msg.Payload = []byte("Hello 世界! \x00\x01\x02\xff")

	data, err := msg.MarshalBinary()
	assert.NoError(t, err)

	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(data)
	assert.NoError(t, err)
	assert.True(t, bytes.Equal(msg.Payload, newMsg.Payload))
}

func TestAgentMessage_ValidateMessage_HeaderLength112(t *testing.T) {
	// channel_closed messages use a 112-byte header
	msg := NewAgentMessage()
	msg.headerLength = 112
	msg.MessageType = ChannelClosed
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.NoError(t, err)
}

func TestAgentMessage_ValidateMessage_HeaderLengthTooLarge(t *testing.T) {
	msg := NewAgentMessage()
	msg.headerLength = 200
	msg.MessageType = InputStreamData
	msg.Payload = []byte("test")
	msg.PayloadLength = uint32(len(msg.Payload))
	msg.sha256PayloadDigest()

	err := msg.ValidateMessage()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
}

func TestAgentMessage_MarshalBinary_NilPayload(t *testing.T) {
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = Output
	msg.Payload = nil

	data, err := msg.MarshalBinary()
	assert.NoError(t, err)
	assert.NotEmpty(t, data)

	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(data)
	assert.NoError(t, err)
	assert.Empty(t, newMsg.Payload)
}

func TestAgentMessage_MarshalBinary_ChannelClosedHeader(t *testing.T) {
	// channel_closed uses 112-byte header (no PayloadType field)
	msg := NewAgentMessage()
	msg.headerLength = 112
	msg.MessageType = ChannelClosed
	msg.Flags = Fin
	msg.Payload = []byte(`{"Output":"session closed"}`)

	data, err := msg.MarshalBinary()
	// This will fail validation because headerLength 112 means PayloadType
	// is not written, but MarshalBinary always writes it. Let's verify the behavior.
	// Actually, looking at the code, MarshalBinary always writes PayloadType regardless
	// of headerLength. The 112-byte header is only handled in UnmarshalBinary.
	// This means MarshalBinary will produce a 116-byte header even for channel_closed.
	// That's fine - we only receive channel_closed from the agent, never send it.
	assert.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestAgentMessage_UnmarshalBinary_112ByteHeader(t *testing.T) {
	// Build a raw message with 112-byte header (channel_closed format)
	// This skips the PayloadType field
	msg := NewAgentMessage()
	msg.MessageType = ChannelClosed
	msg.Flags = Fin
	msg.Payload = []byte(`{"Output":"closed"}`)
	msg.sha256PayloadDigest()
	msg.PayloadLength = uint32(len(msg.Payload))

	// Marshal normally first to get the wire bytes
	data, err := msg.MarshalBinary()
	require.NoError(t, err)

	// Now manually construct a 112-byte header version by removing the PayloadType field
	// Header: [0:4] headerLen, [4:36] msgType, [36:40] schema, [40:48] time,
	// [48:56] seq, [56:64] flags, [64:80] uuid, [80:112] digest
	// At offset 112: PayloadType (4 bytes) in 116-byte header
	// At offset 116: PayloadLength (4 bytes)
	// At offset 120: Payload

	// Replace header length with 112
	data[0], data[1], data[2], data[3] = 0, 0, 0, 112

	// Remove the PayloadType field (bytes 112-115)
	newData := make([]byte, 0, len(data)-4)
	newData = append(newData, data[:112]...)
	newData = append(newData, data[116:]...) // skip PayloadType, keep PayloadLength + Payload

	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(newData)
	assert.NoError(t, err)
	assert.Equal(t, ChannelClosed, newMsg.MessageType)
	assert.Equal(t, PayloadType(0), newMsg.PayloadType) // PayloadType not present in 112-byte header
	assert.Equal(t, msg.Payload, newMsg.Payload)
}

func TestAgentMessage_UnmarshalBinary_TooShortForBasicHeader(t *testing.T) {
	// Valid header length but not enough data for basic header fields
	data := make([]byte, 50)
	data[0], data[1], data[2], data[3] = 0, 0, 0, 116 // valid header length

	msg := &AgentMessage{}
	err := msg.UnmarshalBinary(data)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "data too short for basic header")
}

func TestAgentMessage_UnmarshalBinary_TooShortForFullHeader(t *testing.T) {
	// Enough for basic header but not full 116-byte header
	data := make([]byte, 113)
	data[0], data[1], data[2], data[3] = 0, 0, 0, 116 // header length = 116

	// Fill in enough to pass basic header check (need valid message type, schema, etc.)
	// But it will fail at "data too short for full header"
	msg := &AgentMessage{}
	err := msg.UnmarshalBinary(data)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "data too short for full header")
}

func TestAgentMessage_UnmarshalBinary_TooShortForPayloadLength(t *testing.T) {
	// Build a valid 112-byte header message but truncate before payload length
	msg := NewAgentMessage()
	msg.headerLength = 112
	msg.MessageType = ChannelClosed
	msg.Flags = Fin
	msg.Payload = []byte("test")
	msg.sha256PayloadDigest()
	msg.PayloadLength = uint32(len(msg.Payload))

	data, err := msg.MarshalBinary()
	require.NoError(t, err)

	// Rewrite as 112-byte header and truncate before payload length field
	data[0], data[1], data[2], data[3] = 0, 0, 0, 112
	truncated := data[:112] // no PayloadType, no PayloadLength

	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(truncated)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "data too short for payload length")
}

func TestAgentMessage_UnmarshalBinary_TooShortForPayload(t *testing.T) {
	// Build a valid message then truncate the payload
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData
	msg.Flags = Data
	msg.PayloadType = Output
	msg.Payload = []byte("hello world test data")

	data, err := msg.MarshalBinary()
	require.NoError(t, err)

	// Truncate: remove last 10 bytes of payload
	truncated := data[:len(data)-10]

	newMsg := &AgentMessage{}
	err = newMsg.UnmarshalBinary(truncated)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidAgentMessage)
	assert.Contains(t, err.Error(), "data too short for payload")
}

func TestAgentMessage_ConvertMessageType_LongType(t *testing.T) {
	// Message type >= 32 chars should be truncated
	msg := NewAgentMessage()
	msg.MessageType = MessageType("this_is_a_very_long_message_type_that_exceeds_32_chars")

	result := msg.convertMessageType()
	assert.Len(t, result, 32)
	assert.Equal(t, []byte("this_is_a_very_long_message_type"), result)
}

func TestAgentMessage_ConvertMessageType_ExactLength(t *testing.T) {
	// Message type exactly 32 chars
	msg := NewAgentMessage()
	msg.MessageType = MessageType("12345678901234567890123456789012") // exactly 32

	result := msg.convertMessageType()
	assert.Len(t, result, 32)
	assert.Equal(t, []byte("12345678901234567890123456789012"), result)
}

func TestAgentMessage_ConvertMessageType_ShortPadded(t *testing.T) {
	// Short message type should be space-padded
	msg := NewAgentMessage()
	msg.MessageType = InputStreamData

	result := msg.convertMessageType()
	assert.Len(t, result, 32)
	// Should start with the message type and be padded with spaces
	assert.True(t, bytes.HasPrefix(result, []byte(InputStreamData)))
	for i := len(InputStreamData); i < 32; i++ {
		assert.Equal(t, byte(0x20), result[i], "byte at offset %d should be space", i)
	}
}
