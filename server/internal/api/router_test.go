package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lixianliang/ai-coding2026/server/internal/game"
	"github.com/stretchr/testify/require"
)

type scriptedASR struct{}

func (scriptedASR) Open(context.Context, string) (game.ASRSession, error) {
	return &scriptedSession{events: make(chan game.ASREvent, 8)}, nil
}

type scriptedSession struct {
	events    chan game.ASREvent
	closeOnce sync.Once
}

func (s *scriptedSession) Push(_ context.Context, pcm []byte) error {
	switch string(pcm) {
	case "start":
		s.events <- game.ASREvent{Kind: game.ASREventStarted}
	case "final:no":
		s.events <- game.ASREvent{Kind: game.ASREventFinal, Text: "不是这个"}
	case "final:correct":
		s.events <- game.ASREvent{Kind: game.ASREventFinal, Text: "答案 correct"}
	case "final:discuss":
		s.events <- game.ASREvent{Kind: game.ASREventFinal, Text: "我觉得还有一个可能"}
	case "final:continue":
		s.events <- game.ASREvent{Kind: game.ASREventFinal, Text: "游戏继续"}
	}
	return nil
}

func (s *scriptedSession) Events() <-chan game.ASREvent {
	return s.events
}

func (s *scriptedSession) Close() error {
	s.closeOnce.Do(func() { close(s.events) })
	return nil
}

type testTTS struct{}

func (testTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	return []byte("tts:" + text), nil
}

type testJudge struct {
	calls atomic.Int32
}

func (j *testJudge) Judge(_ context.Context, _ string, _ string, question string) (game.Verdict, error) {
	j.calls.Add(1)
	if strings.Contains(question, "correct") {
		return game.VerdictCorrect, nil
	}
	return game.VerdictNo, nil
}

type passthroughCodec struct{}

func (passthroughCodec) DecodeOpus(payload []byte) ([]byte, error) { return payload, nil }
func (passthroughCodec) EncodePCM(pcm []byte) ([][]byte, error)    { return [][]byte{pcm}, nil }
func (passthroughCodec) Close() error                              { return nil }

func TestHealthAndAuthResponses(t *testing.T) {
	manager, _ := newTestManager(t, &testJudge{})
	router := NewRouter(manager, nil, "secret", testLogger())
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + routeHealth)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(server.URL + routeGame + "?room_id=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"code":40100`)
}

func TestFinishedRoundWaitsForContinue(t *testing.T) {
	judge := &testJudge{}
	manager, _ := newTestManager(t, judge)
	router := NewRouter(manager, nil, "secret", testLogger())
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	first := dialDevice(t, server.URL, "device-1", game.ProtocolVersion1)
	readUntilTTS(t, first, game.Message{Type: "tts", State: "stop"})

	writeAudio(t, first, "start")
	writeAudio(t, first, "final:correct")
	require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	readUntilTTS(t, first, game.Message{Type: "tts", State: "stop"})

	require.NoError(t, first.SetReadDeadline(time.Now().Add(150*time.Millisecond)))
	_, data, err := first.ReadMessage()
	require.Error(t, err, "finished round must wait for 游戏继续, got %q", data)
}

func TestMultiplayerAudioRoutingFloorAndContinue(t *testing.T) {
	judge := &testJudge{}
	manager, _ := newTestManager(t, judge)
	router := NewRouter(manager, nil, "secret", testLogger())
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	first := dialDevice(t, server.URL, "device-1", game.ProtocolVersion1)
	readUntilTTS(t, first, game.Message{Type: "tts", State: "stop"})
	second := dialDevice(t, server.URL, "device-2", game.ProtocolVersion1)
	readUntilTTS(t, second, game.Message{Type: "tts", State: "start"})
	third := dialDevice(t, server.URL, "device-3", game.ProtocolVersion1)
	readUntilTTS(t, third, game.Message{Type: "tts", State: "start"})

	// A mid-round joiner can hear the floor owner but cannot take the microphone.
	writeAudio(t, first, "start")
	time.Sleep(50 * time.Millisecond)
	writeAudio(t, first, "late-listener-audio")
	for _, conn := range []*websocket.Conn{second, third} {
		readAudioMatching(t, conn, "late listener audio", func(payload string) bool {
			return payload == "late-listener-audio"
		})
	}
	writeAudio(t, second, "start")
	writeAudio(t, second, "final:correct")
	require.Never(t, func() bool { return judge.calls.Load() != 0 }, 100*time.Millisecond, 10*time.Millisecond)

	// End the first round so all three devices are present when the next one starts.
	writeAudio(t, first, "final:correct")
	require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	readUntilTTS(t, first, game.Message{Type: "tts", State: "stop"})
	for _, conn := range []*websocket.Conn{second, third} {
		readUntilTTS(t, conn, game.Message{Type: "tts", State: "sentence_start"})
		readAudioMatching(t, conn, "fixed answer", func(payload string) bool {
			return strings.HasPrefix(payload, "tts:猜对了。汤底是：")
		})
	}

	// The finished round stays in discussion. Spoken discussion is forwarded but not judged.
	writeAudio(t, first, "start")
	time.Sleep(50 * time.Millisecond)
	writeAudio(t, first, "discuss-audio")
	for _, conn := range []*websocket.Conn{second, third} {
		readAudioMatching(t, conn, "discussion audio", func(payload string) bool {
			return payload == "discuss-audio"
		})
	}
	writeAudio(t, first, "final:discuss")
	require.Equal(t, int32(1), judge.calls.Load())

	// Only an explicit continue phrase starts another round; everyone then becomes eligible.
	writeAudio(t, first, "start")
	writeAudio(t, first, "final:continue")
	for _, conn := range []*websocket.Conn{first, second, third} {
		readUntilTTS(t, conn, game.Message{Type: "tts", State: "sentence_start"})
		readAudioMatching(t, conn, "next question audio", func(payload string) bool {
			return strings.HasPrefix(payload, "tts:")
		})
		readUntilTTS(t, conn, game.Message{Type: "tts", State: "stop"})
	}
	require.Equal(t, int32(1), judge.calls.Load())

	// First speaker owns the floor; the other two devices cannot take it.
	writeAudio(t, first, "start")
	readUntilTTS(t, second, game.Message{Type: "tts", State: "start"})
	readUntilTTS(t, third, game.Message{Type: "tts", State: "start"})
	writeAudio(t, first, "voice-a")
	for _, conn := range []*websocket.Conn{second, third} {
		readAudioMatching(t, conn, "first speaker audio", func(payload string) bool {
			return payload == "voice-a"
		})
	}
	for conn, transcript := range map[*websocket.Conn]string{second: "voice-b", third: "voice-c"} {
		writeAudio(t, conn, "start")
		writeAudio(t, conn, transcript)
		writeAudio(t, conn, "final:correct")
	}
	require.Never(t, func() bool { return judge.calls.Load() != 1 }, 150*time.Millisecond, 10*time.Millisecond)

	writeAudio(t, first, "final:no")
	require.Eventually(t, func() bool { return judge.calls.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
	for _, conn := range []*websocket.Conn{first, second, third} {
		readAudioMatching(t, conn, "AI reply", func(payload string) bool {
			return payload == "tts:不是。"
		}, "voice-a", "voice-b", "voice-c")
	}
}

func newTestManager(t *testing.T, judge game.Judge) (*game.Manager, *game.Store) {
	t.Helper()
	store, err := game.OpenStore(":memory:")
	require.NoError(t, err)
	manager, err := game.NewManager(game.Deps{
		Store:        store,
		ASR:          scriptedASR{},
		TTS:          testTTS{},
		Judge:        judge,
		Codec:        passthroughCodec{},
		Logger:       testLogger(),
		TurnDuration: time.Second,
		GameDuration: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		manager.Close()
		require.NoError(t, store.Close())
	})
	return manager, store
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func dialDevice(t *testing.T, serverURL string, deviceID string, version game.ProtocolVersion) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + routeGame + "?room_id=1"
	header := http.Header{}
	header.Set(headerDeviceID, deviceID)
	header.Set(headerClientID, "client-"+deviceID)
	header.Set(headerProtocolVersion, strconv.Itoa(int(version)))
	header.Set(headerAuthorization, bearerPrefix+"secret")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	t.Cleanup(func() { conn.Close() })

	require.NoError(t, conn.WriteJSON(game.Message{
		Type:      "hello",
		Version:   int(version),
		Transport: "websocket",
		AudioParams: &game.AudioParams{
			Format: "opus", SampleRate: 16000, Channels: 1, FrameDuration: 60,
		},
	}))
	var hello game.Message
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	require.NoError(t, conn.ReadJSON(&hello))
	require.Equal(t, "hello", hello.Type)
	require.NotEmpty(t, hello.SessionID)
	return conn
}

func writeAudio(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, []byte(payload)))
}

func readUntilTTS(t *testing.T, conn *websocket.Conn, want game.Message) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	require.NoError(t, conn.SetReadDeadline(deadline))
	for {
		messageType, data, err := conn.ReadMessage()
		require.NoError(t, err)
		if messageType != websocket.TextMessage {
			continue
		}
		var message game.Message
		require.NoError(t, json.Unmarshal(data, &message))
		if message.Type == want.Type && message.State == want.State {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not receive %s/%s", want.Type, want.State)
		}
	}
}

func readAudioMatching(t *testing.T, conn *websocket.Conn, label string, match func(string) bool, forbidden ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	require.NoError(t, conn.SetReadDeadline(deadline))
	for {
		messageType, data, err := conn.ReadMessage()
		require.NoError(t, err)
		if messageType != websocket.BinaryMessage {
			continue
		}
		payload, err := game.ParseAudioPacket(game.ProtocolVersion1, data)
		require.NoError(t, err)
		for _, item := range forbidden {
			require.NotEqual(t, item, string(payload), "unexpected %s", item)
		}
		if match(string(payload)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not receive %s", label)
		}
	}
}
