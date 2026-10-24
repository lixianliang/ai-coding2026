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

// Judge 按“我是名字，问题”提取玩家，问题含 correct/yes 时返回对应判定。
func (j *testJudge) Judge(_ context.Context, _ string, _ string, question string) (game.JudgeResult, error) {
	j.calls.Add(1)
	player := ""
	if rest, ok := strings.CutPrefix(question, "我是"); ok {
		player, question, _ = strings.Cut(rest, "，")
	}
	switch {
	case strings.Contains(question, "correct"):
		return game.JudgeResult{Verdict: game.VerdictCorrect, Player: player}, nil
	case strings.Contains(question, "yes"):
		return game.JudgeResult{Verdict: game.VerdictYes, Player: player}, nil
	}
	return game.JudgeResult{Verdict: game.VerdictNo, Player: player}, nil
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

func TestLobbyWaitsForStartCommand(t *testing.T) {
	judge := &testJudge{}
	manager, _, _ := newTestManager(t, judge, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	readLobby(t, conn)

	writeListen(t, conn, "start")
	writeAudio(t, conn, "final:随便聊聊")
	writeListen(t, conn, "stop")
	require.Contains(t, readSpeech(t, conn), "开始游戏")
	require.Zero(t, judge.calls.Load())

	surface := startGame(t, conn)
	require.Contains(t, surface, "为什么")
}

func TestManualListenFinalizesOnlyOnStop(t *testing.T) {
	judge := &testJudge{}
	manager, _, asr := newTestManager(t, judge, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	enterGame(t, conn)

	writeListen(t, conn, "start")
	writeAudio(t, conn, "final:no")
	require.Never(t, func() bool { return judge.calls.Load() != 0 }, 100*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, int32(2), asr.pushes.Load())

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
	enterGame(t, conn)
	before := asr.pushes.Load()
	writeAudio(t, conn, "final:correct")

	writeListen(t, conn, "start")
	writeListen(t, conn, "stop")
	require.Equal(t, before, asr.pushes.Load())
	require.Zero(t, judge.calls.Load())

	writeListen(t, conn, "start")
	writeAudio(t, conn, "final:no")
	writeListen(t, conn, "stop")
	require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	require.Equal(t, "不是。", readSpeech(t, conn))
}

func TestCompleteGameLifecycle(t *testing.T) {
	judge := &testJudge{}
	manager, _, _ := newTestManager(t, judge, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	enterGame(t, conn)

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

	writeListen(t, conn, "start")
	writeAudio(t, conn, "游戏继续")
	writeListen(t, conn, "stop")
	next := readSpeech(t, conn)
	require.Contains(t, next, "为什么")
	require.Equal(t, int32(1), judge.calls.Load())

	writeListen(t, conn, "start")
	writeAudio(t, conn, "退出游戏")
	writeListen(t, conn, "stop")
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err)

	nextDevice := dialDevice(t, server.URL, "device-2", 1, game.ProtocolVersion1)
	readLobby(t, nextDevice)
}

func TestScoreboardRanksPlayersAndAnnouncesChampion(t *testing.T) {
	manager, _, _ := newTestManager(t, &testJudge{}, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	idle := fetchScoreboard(t, server.URL, 1)
	require.Equal(t, game.ScoreboardIdle, idle.Status)
	require.NotNil(t, idle.Players)

	conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	require.Contains(t, enterGame(t, conn), "我是小明")
	ask := func(text string) string {
		writeListen(t, conn, "start")
		writeAudio(t, conn, text)
		writeListen(t, conn, "stop")
		return readSpeech(t, conn)
	}
	ask("我是小明，yes")
	ask("我是小红，yes")
	ask("我是小红，yes")
	ask("yes")

	playing := fetchScoreboard(t, server.URL, 1)
	require.Equal(t, game.ScoreboardPlaying, playing.Status)
	require.Empty(t, playing.Answer)
	require.Equal(t, []game.PlayerScore{
		{Name: "小红", Score: 2, Questions: 2, Yes: 2},
		{Name: "小明", Score: 1, Questions: 1, Yes: 1},
	}, playing.Players)

	speech := ask("我是小明，correct")
	require.True(t, strings.HasPrefix(speech, "猜对了。本局冠军是小明，得6分。汤底是："), speech)
	require.True(t, strings.HasSuffix(speech, "本轮游戏结束。想开新一局请说“游戏继续”。"), speech)
	require.NotContains(t, speech, "小红")

	finished := fetchScoreboard(t, server.URL, 1)
	require.Equal(t, game.ScoreboardFinished, finished.Status)
	require.Equal(t, "小明", finished.Champion)
	require.NotEmpty(t, finished.Answer)
	require.Equal(t, []string{"小明", "小红"}, []string{finished.Players[0].Name, finished.Players[1].Name})
}

func fetchScoreboard(t *testing.T, serverURL string, roomID int) game.Scoreboard {
	t.Helper()
	resp, err := http.Get(serverURL + "/v1/rooms/" + strconv.Itoa(roomID) + "/scoreboard")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body struct {
		Code int             `json:"code"`
		Data game.Scoreboard `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, successCode, body.Code)
	return body.Data
}

func TestSecondDeviceIsRejected(t *testing.T) {
	manager, _, _ := newTestManager(t, &testJudge{}, time.Second, time.Minute)
	server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
	t.Cleanup(server.Close)

	first := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
	readLobby(t, first)
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
		enterGame(t, conn)
		writeListen(t, conn, "start")
		writeAudio(t, conn, "final:no")
		require.Eventually(t, func() bool { return judge.calls.Load() == 1 }, time.Second, 10*time.Millisecond)
	})

	t.Run("round timeout speaks answer", func(t *testing.T) {
		manager, _, _ := newTestManager(t, &testJudge{}, time.Second, 250*time.Millisecond)
		server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
		t.Cleanup(server.Close)

		conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
		enterGame(t, conn)
		speech := readSpeech(t, conn)
		require.Contains(t, speech, "时间到。本局汤底是：")
	})

	t.Run("round timeout during empty capture speaks answer", func(t *testing.T) {
		manager, _, _ := newTestManager(t, &testJudge{}, time.Second, 250*time.Millisecond)
		server := httptest.NewServer(NewRouter(manager, nil, "secret", testLogger()))
		t.Cleanup(server.Close)

		conn := dialDevice(t, server.URL, "device-1", 1, game.ProtocolVersion1)
		enterGame(t, conn)
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
	readLobby(t, first)
	writeListen(t, first, "start")
	writeAudio(t, first, "退出游戏")
	writeListen(t, first, "stop")

	require.NoError(t, first.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := first.ReadMessage()
	require.Error(t, err)

	second := dialDevice(t, server.URL, "device-2", 1, game.ProtocolVersion1)
	readLobby(t, second)
}

func newTestManager(t *testing.T, judge game.Judge, turnDuration, gameDuration time.Duration) (*game.Manager, *game.Store, *scriptedASR) {
	t.Helper()
	store, err := game.OpenStore(":memory:", nil)
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

func readLobby(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	speech := readSpeech(t, conn)
	require.Contains(t, speech, "开始游戏")
	require.Contains(t, speech, "游戏继续")
	require.Contains(t, speech, "退出游戏")
	return speech
}

func startGame(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	writeListen(t, conn, "start")
	writeAudio(t, conn, "开始游戏")
	writeListen(t, conn, "stop")
	return readSpeech(t, conn)
}

func enterGame(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	readLobby(t, conn)
	return startGame(t, conn)
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
