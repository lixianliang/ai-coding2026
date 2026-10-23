package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type scriptedASR struct {
	pushes    atomic.Int32
	finalizes atomic.Int32
}

func (a *scriptedASR) Open(context.Context, string) (game.ASRSession, error) {
	return &scriptedSession{events: make(chan game.ASREvent, 8), pushes: &a.pushes, finalizes: &a.finalizes}, nil
}

type scriptedSession struct {
	events    chan game.ASREvent
	pushes    *atomic.Int32
	finalizes *atomic.Int32
	mu        sync.Mutex
	audio     bytes.Buffer
	closeOnce sync.Once
	eventsMu  sync.RWMutex
}

func (s *scriptedSession) Push(_ context.Context, pcm []byte) error {
	s.pushes.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.audio.Write(pcm)
	return nil
}

func (s *scriptedSession) Finalize(context.Context) error {
	s.mu.Lock()
	text := s.audio.String()
	s.audio.Reset()
	s.mu.Unlock()
	if text == "" {
		return errors.New("empty transcript")
	}
	s.eventsMu.RLock()
	s.events <- game.ASREvent{Kind: game.ASREventFinal, Text: text}
	s.eventsMu.RUnlock()
	s.finalizes.Add(1)
	return nil
}

func (s *scriptedSession) Events() <-chan game.ASREvent { return s.events }

func (s *scriptedSession) Close() error {
	s.closeOnce.Do(func() {
		s.eventsMu.Lock()
		close(s.events)
		s.eventsMu.Unlock()
	})
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
	manager, _, _ := newTestManager(t, &testJudge{}, time.Second, time.Minute)
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

func TestManualListenFinalizesOnlyOnStop(t *testing.T) {
	judge := &testJudge{}
	manager, _, asr := newTestManager(t, judge, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	readSpeech(t, conn)

	writeListen(t, conn, "start")
	writeAudio(t, conn, "final:no")
	require.Never(t, func() bool { return judge.calls.Load() != 0 }, 100*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, int32(1), asr.pushes.Load())

	writeListen(t, conn, "stop")
	require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	require.Equal(t, "不是。", readSpeech(t, conn, "final:no"))
}

func TestAudioBeforeListenStartIsIgnored(t *testing.T) {
	judge := &testJudge{}
	manager, _, asr := newTestManager(t, judge, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	readSpeech(t, conn)
	writeAudio(t, conn, "final:correct")

	writeListen(t, conn, "start")
	writeListen(t, conn, "stop")
	require.Zero(t, asr.pushes.Load())
	require.Zero(t, judge.calls.Load())

	writeListen(t, conn, "start")
	writeAudio(t, conn, "final:no")
	writeListen(t, conn, "stop")
	require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	require.Equal(t, "不是。", readSpeech(t, conn))
}

func TestCompleteGameLifecycle(t *testing.T) {
	judge := &testJudge{}
	manager, _, asr := newTestManager(t, judge, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	readSpeech(t, conn)

	writeListen(t, conn, "start")
	writeAudio(t, conn, "final:correct")
	writeListen(t, conn, "stop")
	require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	speech := readSpeech(t, conn, "final:correct")
	require.Contains(t, speech, "猜对了。汤底是：")
	require.NotContains(t, speech, "。。")
	require.Contains(t, speech, "游戏继续")

	writeListen(t, conn, "start")
	writeAudio(t, conn, "final:discuss")
	writeListen(t, conn, "stop")
	require.Never(t, func() bool { return judge.calls.Load() != 1 }, 100*time.Millisecond, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		if asr.finalizes.Load() >= 3 {
			return true
		}
		writeListen(t, conn, "start")
		writeAudio(t, conn, "游戏继续")
		writeListen(t, conn, "stop")
		return false
	}, time.Second, 20*time.Millisecond)
	next := readSpeech(t, conn)
	require.NotContains(t, next, "猜对了。汤底是：")
	require.Equal(t, int32(1), judge.calls.Load())

	writeListen(t, conn, "start")
	writeAudio(t, conn, "退出游戏")
	writeListen(t, conn, "stop")
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err)

	nextDevice := dialDevice(t, server.URL, "device-2", 1, game.ProtocolVersion1)
	readSpeech(t, nextDevice)
}

func TestSecondDeviceIsRejected(t *testing.T) {
	manager, _, _ := newTestManager(t, &testJudge{}, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	first := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	readSpeech(t, first)
	second := dialDevice(t, server.URL, "device-2", 1, game.ProtocolVersion1)

	require.NoError(t, second.SetReadDeadline(time.Now().Add(time.Second)))
	_, data, err := second.ReadMessage()
	require.NoError(t, err)
	require.Contains(t, string(data), `"message":"room is full"`)
}

func TestTurnAndGameTimeouts(t *testing.T) {
	t.Run("question timeout finalizes capture", func(t *testing.T) {
		judge := &testJudge{}
		manager, _, _ := newTestManager(t, judge, 100*time.Millisecond, time.Minute)
		server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
		t.Cleanup(server.Close)

		conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
		readSpeech(t, conn)
		writeListen(t, conn, "start")
		writeAudio(t, conn, "final:no")
		require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	})

	t.Run("round timeout speaks answer", func(t *testing.T) {
		manager, _, _ := newTestManager(t, &testJudge{}, time.Second, 250*time.Millisecond)
		server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
		t.Cleanup(server.Close)

		conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
		readSpeech(t, conn)
		speech := readSpeech(t, conn)
		require.Contains(t, speech, "时间到。本局汤底是：")
	})

	t.Run("round timeout during empty capture speaks answer", func(t *testing.T) {
		manager, _, _ := newTestManager(t, &testJudge{}, time.Second, 250*time.Millisecond)
		server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
		t.Cleanup(server.Close)

		conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
		readSpeech(t, conn)
		writeListen(t, conn, "start")
		speech := readSpeech(t, conn)
		require.Contains(t, speech, "时间到。本局汤底是：")
	})
}

func TestExitGameReleasesRoom(t *testing.T) {
	manager, _, _ := newTestManager(t, &testJudge{}, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	first := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	readSpeech(t, first)
	writeListen(t, first, "start")
	writeAudio(t, first, "退出游戏")
	writeListen(t, first, "stop")

	require.NoError(t, first.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := first.ReadMessage()
	require.Error(t, err)

	second := dialDevice(t, server.URL, "device-2", 1, game.ProtocolVersion1)
	readSpeech(t, second)
}

func newTestManager(t *testing.T, judge game.Judge, turnDuration, gameDuration time.Duration) (*game.Manager, *game.Store, *scriptedASR) {
	t.Helper()
	store, err := game.OpenStore(":memory:")
	require.NoError(t, err)
	asr := &scriptedASR{}
	manager, err := game.NewManager(game.Deps{
		Store:        store,
		ASR:          asr,
		TTS:          testTTS{},
		Judge:        judge,
		Codec:        passthroughCodec{},
		Logger:       testLogger(),
		TurnDuration: turnDuration,
		GameDuration: gameDuration,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		manager.Close()
		require.NoError(t, store.Close())
	})
	return manager, store, asr
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func dialDevice(t *testing.T, serverURL, deviceID string, roomID int, version game.ProtocolVersion) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + routeGame + "?room_id=" + strconv.Itoa(roomID)
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

func writeListen(t *testing.T, conn *websocket.Conn, state string) {
	t.Helper()
	require.NoError(t, conn.WriteJSON(game.Message{Type: "listen", State: state, Mode: "manual"}))
}

func writeAudio(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, []byte(payload)))
}

func readSpeech(t *testing.T, conn *websocket.Conn, forbidden ...string) string {
	t.Helper()
	sentence := readUntilTTS(t, conn, "tts", "sentence_start")
	if sentence.Text != "" {
		readAudioMatching(t, conn, "AI speech audio", func(payload string) bool {
			return strings.HasPrefix(payload, "tts:")
		}, forbidden...)
	}
	readUntilTTS(t, conn, "tts", "stop")
	return sentence.Text
}

func readUntilTTS(t *testing.T, conn *websocket.Conn, wantType, wantState string) game.Message {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	for {
		messageType, data, err := conn.ReadMessage()
		require.NoError(t, err)
		if messageType != websocket.TextMessage {
			continue
		}
		var message game.Message
		require.NoError(t, json.Unmarshal(data, &message))
		if message.Type == wantType && message.State == wantState {
			return message
		}
	}
}

func readAudioMatching(t *testing.T, conn *websocket.Conn, label string, match func(string) bool, forbidden ...string) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
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
	}
}
