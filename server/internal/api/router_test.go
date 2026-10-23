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

func (testTTS) Synthesize(context.Context, string) ([]byte, error) {
	return nil, nil
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
	router := NewRouter(manager, "secret", testLogger())
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

func TestRoomClaimsOneSpeakerAndStartsNextGame(t *testing.T) {
	judge := &testJudge{}
	manager, _ := newTestManager(t, judge)
	router := NewRouter(manager, "secret", testLogger())
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	first := dialDevice(t, server.URL, "device-1", game.ProtocolVersion1)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	readUntilTTS(t, first, game.Message{Type: "tts", State: "stop"})

	second := dialDevice(t, server.URL, "device-2", game.ProtocolVersion1)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	readUntilTTS(t, second, game.Message{Type: "tts", State: "start"})

	writeAudio(t, first, "start")
	time.Sleep(50 * time.Millisecond)
	writeAudio(t, second, "start")
	writeAudio(t, second, "final:correct")
	time.Sleep(100 * time.Millisecond)
	require.Zero(t, judge.calls.Load(), "a mid-game listener must not get the floor")

	writeAudio(t, first, "final:no")
	require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	readUntilTTS(t, first, game.Message{Type: "tts", State: "stop"})

	writeAudio(t, first, "start")
	writeAudio(t, first, "final:correct")
	require.Eventually(t, func() bool { return judge.calls.Load() == 2 }, 2*time.Second, 20*time.Millisecond)
	readUntilTTS(t, first, game.Message{Type: "tts", State: "stop"})
	readUntilTTS(t, first, game.Message{Type: "tts", State: "sentence_start"})
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
