package game

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type captureCodec struct {
	chunks [][]byte
	closed atomic.Bool
}

func (c *captureCodec) DecodeOpus(payload []byte) ([]byte, error) { return payload, nil }

func (c *captureCodec) EncodePCM(pcm []byte) ([][]byte, error) {
	chunk := append([]byte(nil), pcm...)
	c.chunks = append(c.chunks, chunk)
	return [][]byte{chunk}, nil
}

func (c *captureCodec) Close() error { c.closed.Store(true); return nil }

func TestPCMStreamWriterPreservesUnalignedChunksAndTail(t *testing.T) {
	source := make([]byte, opusFrameBytes+500)
	for i := range source {
		source[i] = byte(i)
	}
	codec := &captureCodec{}
	client := &client{
		version: ProtocolVersion1,
		out:     make(chan outbound, 4),
		done:    make(chan struct{}),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	writer := newPCMStreamWriter(client, codec)
	defer writer.ticker.Stop()

	require.NoError(t, writer.Write(source[:1000]))
	require.NoError(t, writer.Write(source[1000:2000]))
	require.NoError(t, writer.Write(source[2000:]))
	require.NoError(t, writer.Flush())

	require.Equal(t, []int{opusFrameBytes, 500}, []int{len(codec.chunks[0]), len(codec.chunks[1])})
	var encoded []byte
	for _, chunk := range codec.chunks {
		encoded = append(encoded, chunk...)
	}
	require.Equal(t, source, encoded)
	require.Len(t, client.out, 2)
}

type gatedTTS struct {
	release chan struct{}
}

func (t *gatedTTS) Stream(ctx context.Context, _ string, onAudio func([]byte) error) error {
	first := make([]byte, opusFrameBytes*ttsPrebufferFrames)
	for i := range first {
		first[i] = 1
	}
	if err := onAudio(first); err != nil {
		return err
	}
	select {
	case <-t.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	last := make([]byte, opusFrameBytes)
	for i := range last {
		last[i] = 2
	}
	return onAudio(last)
}

func TestBeginSpeechStreamsAudioAndStopsAfterProviderCompletes(t *testing.T) {
	tts := &gatedTTS{release: make(chan struct{})}
	codec := &captureCodec{}
	client := &client{
		version: ProtocolVersion1,
		out:     make(chan outbound, 8),
		done:    make(chan struct{}),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	commands := make(chan roomCommand, 1)
	room := &room{
		id:       1,
		client:   client,
		commands: commands,
		deps: Deps{
			TTS:         tts,
			Codec:       &captureCodec{},
			SpeechCodec: func() (AudioCodec, error) { return codec, nil },
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
	room.beginSpeech("汤面", actionOpenQuestions, "")
	next := func() outbound {
		t.Helper()
		select {
		case message := <-client.out:
			return message
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for outbound message")
			return outbound{}
		}
	}

	start := next()
	var startMessage Message
	require.NoError(t, json.Unmarshal(start.data, &startMessage))
	require.Equal(t, messageStateStart, startMessage.State)
	sentence := next()
	var sentenceMessage Message
	require.NoError(t, json.Unmarshal(sentence.data, &sentenceMessage))
	require.Equal(t, messageStateSentenceStart, sentenceMessage.State)
	for range ttsPrebufferFrames {
		require.Len(t, next().data, opusFrameBytes)
	}
	select {
	case message := <-client.out:
		t.Fatalf("unexpected message before provider completion: %+v", message)
	case <-time.After(50 * time.Millisecond):
	}

	close(tts.release)
	require.Len(t, next().data, opusFrameBytes)
	stop := next()
	var stopMessage Message
	require.NoError(t, json.Unmarshal(stop.data, &stopMessage))
	require.Equal(t, messageStateStop, stopMessage.State)
	require.Equal(t, commandSpeechDone, (<-commands).kind)
	require.Eventually(t, codec.closed.Load, time.Second, time.Millisecond)
	require.Len(t, codec.chunks, ttsPrebufferFrames+1)
}

func TestReturnToIdleFromDiscussion(t *testing.T) {
	room := &room{phase: phaseListening, discussion: true, capturing: true, active: &client{}, current: &activeGame{}, partial: "partial"}

	room.returnToIdle()

	require.Equal(t, phaseFinished, room.phase)
	require.False(t, room.discussion)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
	require.Empty(t, room.partial)
}

func TestReturnToIdleDuringRound(t *testing.T) {
	room := &room{phase: phaseListening, capturing: true, active: &client{}, current: &activeGame{}}

	room.returnToIdle()

	require.Equal(t, phaseAwaitingQuestion, room.phase)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
}

func TestReturnToIdleFromLobby(t *testing.T) {
	room := &room{phase: phaseListening, capturing: true, active: &client{}}

	room.returnToIdle()

	require.Equal(t, phaseLobby, room.phase)
	require.False(t, room.capturing)
	require.Nil(t, room.active)
}

func TestPhraseMatching(t *testing.T) {
	require.True(t, isExitPhrase("那我先退出游戏吧"))
	require.True(t, isContinuePhrase("我们游戏继续"))
	require.False(t, isContinuePhrase("这局还没讨论完"))
	require.True(t, isStartPhrase("开始游戏"))
	require.True(t, isStartPhrase("那就开始新游戏吧"))
	require.False(t, isStartPhrase("还没准备好"))
	require.True(t, isRepeatSurfacePhrase("我忘了，再说一遍汤面"))
	require.False(t, isRepeatSurfacePhrase("请回答这个问题"))
	require.True(t, isRepeatSurfacePhrase("再说一遍，汤面。"))
	require.True(t, isRepeatSurfacePhrase("再讲一遍汤面"))
}

func TestPCMStreamWriterBuffersStartupAndFlushesShortSpeech(t *testing.T) {
	codec := &captureCodec{}
	writer := newPCMStreamWriter(nil, codec)
	defer writer.ticker.Stop()
	for range ttsPrebufferFrames - 1 {
		require.NoError(t, writer.Write(make([]byte, opusFrameBytes)))
		require.Empty(t, codec.chunks)
	}
	require.NoError(t, writer.Write(make([]byte, opusFrameBytes)))
	require.Len(t, codec.chunks, ttsPrebufferFrames)
	require.NoError(t, writer.Flush())
	shortCodec := &captureCodec{}
	short := newPCMStreamWriter(nil, shortCodec)
	defer short.ticker.Stop()
	require.NoError(t, short.Write(make([]byte, 100)))
	require.Empty(t, shortCodec.chunks)
	require.NoError(t, short.Flush())
	require.Len(t, shortCodec.chunks, 1)
	require.NoError(t, short.Flush())
	require.Len(t, shortCodec.chunks, 1)
}

func TestSpeechCodecStateIsNotShared(t *testing.T) {
	factories := 0
	codecs := []*captureCodec{}
	r := &room{commands: make(chan roomCommand, 1), deps: Deps{
		Codec: &captureCodec{}, TTS: SilenceTTS{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SpeechCodec: func() (AudioCodec, error) {
			c := &captureCodec{}
			codecs = append(codecs, c)
			factories++
			return c, nil
		},
	}}
	for range 2 {
		r.beginSpeech("欢迎", actionWaitStart, "")
		select {
		case <-r.commands:
		case <-time.After(time.Second):
			t.Fatal("speech timeout")
		}
		require.Eventually(t, codecs[len(codecs)-1].closed.Load, time.Second, time.Millisecond)
	}
	require.Equal(t, 2, factories)
	require.NotSame(t, codecs[0], codecs[1])
	require.Empty(t, r.deps.Codec.(*captureCodec).chunks)
}
