package game

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	RoomMin           = 1
	RoomMax           = 3
	MaxPlayersPerRoom = 1

	AudioSampleRate    = 16000
	AudioChannels      = 1
	AudioFrameDuration = 60

	websocketTransport = "websocket"
	audioFormatOpus    = "opus"

	messageTypeHello  = "hello"
	messageTypeError  = "error"
	messageTypeListen = "listen"
	messageTypeTTS    = "tts"

	messageStateStart         = "start"
	messageStateStop          = "stop"
	messageStateSentenceStart = "sentence_start"

	audioPacketTypeOpus = 0

	binaryProtocol2HeaderSize = 16
	binaryProtocol3HeaderSize = 4
)

// ProtocolVersion is the XiaoZhi WebSocket audio framing version.
type ProtocolVersion uint8

const (
	ProtocolVersion1 ProtocolVersion = 1
	ProtocolVersion2 ProtocolVersion = 2
	ProtocolVersion3 ProtocolVersion = 3
)

func ParseProtocolVersion(value int) (ProtocolVersion, error) {
	version := ProtocolVersion(value)
	switch version {
	case ProtocolVersion1, ProtocolVersion2, ProtocolVersion3:
		return version, nil
	default:
		return 0, fmt.Errorf("unsupported protocol version %d", value)
	}
}

type AudioParams struct {
	Format        string `json:"format"`
	SampleRate    int    `json:"sample_rate"`
	Channels      int    `json:"channels"`
	FrameDuration int    `json:"frame_duration"`
}

// Message is shared by incoming and outgoing WebSocket control messages.
type Message struct {
	Type        string       `json:"type"`
	State       string       `json:"state,omitempty"`
	Mode        string       `json:"mode,omitempty"`
	Text        string       `json:"text,omitempty"`
	Message     string       `json:"message,omitempty"`
	Version     int          `json:"version,omitempty"`
	Transport   string       `json:"transport,omitempty"`
	SessionID   string       `json:"session_id,omitempty"`
	AudioParams *AudioParams `json:"audio_params,omitempty"`
}

func ValidateHello(message Message) error {
	if message.Type != messageTypeHello {
		return errors.New("first message must be hello")
	}
	if message.Transport != websocketTransport {
		return fmt.Errorf("unsupported transport %q", message.Transport)
	}
	if message.AudioParams == nil {
		return errors.New("hello audio_params is required")
	}
	if message.AudioParams.Format != audioFormatOpus ||
		message.AudioParams.SampleRate != AudioSampleRate ||
		message.AudioParams.Channels != AudioChannels {
		return errors.New("device audio parameters do not match opus/16000/mono")
	}
	return nil
}

func ParseAudioPacket(version ProtocolVersion, data []byte) ([]byte, error) {
	switch version {
	case ProtocolVersion1:
		if len(data) == 0 {
			return nil, errors.New("empty opus packet")
		}
		return data, nil
	case ProtocolVersion2:
		if len(data) < binaryProtocol2HeaderSize {
			return nil, errors.New("short protocol v2 packet")
		}
		if binary.BigEndian.Uint16(data[0:2]) != uint16(ProtocolVersion2) {
			return nil, errors.New("protocol v2 packet version mismatch")
		}
		if binary.BigEndian.Uint16(data[2:4]) != audioPacketTypeOpus {
			return nil, errors.New("protocol v2 packet is not opus audio")
		}
		size := binary.BigEndian.Uint32(data[12:16])
		if uint64(size) != uint64(len(data)-binaryProtocol2HeaderSize) {
			return nil, errors.New("protocol v2 payload size mismatch")
		}
		return data[binaryProtocol2HeaderSize:], nil
	case ProtocolVersion3:
		if len(data) < binaryProtocol3HeaderSize {
			return nil, errors.New("short protocol v3 packet")
		}
		if data[0] != audioPacketTypeOpus {
			return nil, errors.New("protocol v3 packet is not opus audio")
		}
		size := binary.BigEndian.Uint16(data[2:4])
		if int(size) != len(data)-binaryProtocol3HeaderSize {
			return nil, errors.New("protocol v3 payload size mismatch")
		}
		return data[binaryProtocol3HeaderSize:], nil
	default:
		return nil, fmt.Errorf("unsupported protocol version %d", version)
	}
}

func EncodeAudioPacket(version ProtocolVersion, timestamp uint32, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty opus payload")
	}
	switch version {
	case ProtocolVersion1:
		return payload, nil
	case ProtocolVersion2:
		result := make([]byte, binaryProtocol2HeaderSize+len(payload))
		binary.BigEndian.PutUint16(result[0:2], uint16(ProtocolVersion2))
		binary.BigEndian.PutUint16(result[2:4], audioPacketTypeOpus)
		binary.BigEndian.PutUint32(result[8:12], timestamp)
		binary.BigEndian.PutUint32(result[12:16], uint32(len(payload)))
		copy(result[binaryProtocol2HeaderSize:], payload)
		return result, nil
	case ProtocolVersion3:
		if len(payload) > int(^uint16(0)) {
			return nil, errors.New("protocol v3 payload too large")
		}
		result := make([]byte, binaryProtocol3HeaderSize+len(payload))
		result[0] = audioPacketTypeOpus
		binary.BigEndian.PutUint16(result[2:4], uint16(len(payload)))
		copy(result[binaryProtocol3HeaderSize:], payload)
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported protocol version %d", version)
	}
}
