package game

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAudioPacketRoundTrip(t *testing.T) {
	payload := []byte("opus-frame")
	for _, version := range []ProtocolVersion{ProtocolVersion1, ProtocolVersion2, ProtocolVersion3} {
		packet, err := EncodeAudioPacket(version, 1234, payload)
		require.NoError(t, err)
		decoded, err := ParseAudioPacket(version, packet)
		require.NoError(t, err)
		require.Equal(t, payload, decoded)
	}
}

func TestProtocolV2ReadsNetworkHeader(t *testing.T) {
	packet := make([]byte, binaryProtocol2HeaderSize+2)
	binary.BigEndian.PutUint16(packet[0:2], uint16(ProtocolVersion2))
	binary.BigEndian.PutUint16(packet[2:4], audioPacketTypeOpus)
	binary.BigEndian.PutUint32(packet[12:16], 2)
	copy(packet[binaryProtocol2HeaderSize:], "ok")
	decoded, err := ParseAudioPacket(ProtocolVersion2, packet)
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), decoded)
}

func TestOpusCodecRoundTrip(t *testing.T) {
	codec, err := NewOpusCodec()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, codec.Close()) })
	pcm := make([]byte, opusFrameBytes)
	packets, err := codec.EncodePCM(pcm)
	require.NoError(t, err)
	require.Len(t, packets, 1)
	decoded, err := codec.DecodeOpus(packets[0])
	require.NoError(t, err)
	require.NotEmpty(t, decoded)
}
