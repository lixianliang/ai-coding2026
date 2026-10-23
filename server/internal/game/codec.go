package game

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/hraban/opus"
)

const (
	pcmSampleBytes    = 2
	opusFrameSamples  = AudioSampleRate * AudioFrameDuration / 1000
	opusFrameBytes    = opusFrameSamples * pcmSampleBytes
	opusDecodeSamples = 5760
	opusBitrate       = 24000
)

// AudioCodec crosses the device Opus boundary and the server PCM boundary.
type AudioCodec interface {
	DecodeOpus([]byte) ([]byte, error)
	EncodePCM([]byte) ([][]byte, error)
	Close() error
}

type OpusCodec struct {
	mu      sync.Mutex
	encoder *opus.Encoder
	decoder *opus.Decoder
}

func NewOpusCodec() (*OpusCodec, error) {
	encoder, err := opus.NewEncoder(AudioSampleRate, AudioChannels, opus.AppAudio)
	if err != nil {
		return nil, fmt.Errorf("create opus encoder: %w", err)
	}
	if err := encoder.SetBitrate(opusBitrate); err != nil {
		return nil, fmt.Errorf("set opus bitrate: %w", err)
	}
	decoder, err := opus.NewDecoder(AudioSampleRate, AudioChannels)
	if err != nil {
		return nil, fmt.Errorf("create opus decoder: %w", err)
	}
	return &OpusCodec{encoder: encoder, decoder: decoder}, nil
}

func (c *OpusCodec) DecodeOpus(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("decode empty opus packet")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	samples := make([]int16, opusDecodeSamples)
	n, err := c.decoder.Decode(payload, samples)
	if err != nil {
		return nil, fmt.Errorf("decode opus: %w", err)
	}
	pcm := make([]byte, n*pcmSampleBytes)
	for i, sample := range samples[:n] {
		binary.LittleEndian.PutUint16(pcm[i*pcmSampleBytes:], uint16(sample))
	}
	return pcm, nil
}

func (c *OpusCodec) EncodePCM(pcm []byte) ([][]byte, error) {
	if len(pcm) == 0 {
		return [][]byte{}, nil
	}
	if len(pcm)%pcmSampleBytes != 0 {
		return nil, errors.New("pcm byte length must be divisible by 2")
	}
	frameData := make([]byte, opusFrameBytes)
	packets := make([][]byte, 0, (len(pcm)+opusFrameBytes-1)/opusFrameBytes)
	for offset := 0; offset < len(pcm); offset += opusFrameBytes {
		copy(frameData, pcm[offset:min(offset+opusFrameBytes, len(pcm))])
		if end := min(offset+opusFrameBytes, len(pcm)); end-offset < opusFrameBytes {
			clear(frameData[end-offset:])
		}
		samples := make([]int16, opusFrameSamples)
		for i := range samples {
			samples[i] = int16(binary.LittleEndian.Uint16(frameData[i*pcmSampleBytes:]))
		}
		output := make([]byte, 4000)
		c.mu.Lock()
		n, err := c.encoder.Encode(samples, output)
		c.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("encode opus: %w", err)
		}
		packets = append(packets, output[:n])
	}
	return packets, nil
}

func (c *OpusCodec) Close() error {
	return nil
}
