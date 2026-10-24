package game

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestMiniMaxStreamsAudioBeforeTaskFinished(t *testing.T) {
	handlerDone := make(chan error, 1)
	firstAudio := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			handlerDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		write := func(value any) bool {
			if err := conn.WriteJSON(value); err != nil {
				handlerDone <- err
				return false
			}
			return true
		}
		read := func(want string) bool {
			var message struct {
				Event string `json:"event"`
			}
			if err := conn.ReadJSON(&message); err != nil {
				handlerDone <- err
				return false
			}
			if message.Event != want {
				handlerDone <- fmt.Errorf("expected %s, got %s", want, message.Event)
				return false
			}
			return true
		}

		if !write(map[string]string{"event": minimaxEventConnected}) ||
			!read(minimaxEventTaskStart) ||
			!write(map[string]string{"event": minimaxEventStarted}) ||
			!read(minimaxEventContinue) {
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"event": minimaxEventContinued,
			"data":  map[string]string{"audio": "0102"},
		}); err != nil {
			handlerDone <- err
			return
		}
		select {
		case <-firstAudio:
		case <-time.After(time.Second):
			handlerDone <- fmt.Errorf("first audio was not streamed before task completion")
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"event":    minimaxEventContinued,
			"is_final": true,
			"data":     map[string]string{"audio": "0304"},
		}); err != nil {
			handlerDone <- err
			return
		}
		if !read(minimaxEventFinish) {
			return
		}
		handlerDone <- nil
	}))
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + minimaxTTSURLPath
	tts, err := NewMiniMaxTTS("key", wsURL, "model", "voice", "", 1, 1, 0)
	require.NoError(t, err)

	var chunks [][]byte
	err = tts.Stream(context.Background(), "你好", func(chunk []byte) error {
		chunks = append(chunks, chunk)
		if len(chunks) == 1 {
			close(firstAudio)
		}
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, [][]byte{{0x01, 0x02}, {0x03, 0x04}}, chunks)
	require.NoError(t, <-handlerDone)
}

func TestMiniMaxStreamReportsConnectionFailureAfterPartialAudio(t *testing.T) {
	handlerDone := make(chan error, 1)
	firstAudio := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			handlerDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.WriteJSON(map[string]string{"event": minimaxEventConnected}); err != nil {
			handlerDone <- err
			return
		}
		var message struct {
			Event string `json:"event"`
		}
		if err := conn.ReadJSON(&message); err != nil || message.Event != minimaxEventTaskStart {
			handlerDone <- fmt.Errorf("read task_start: %v, event %q", err, message.Event)
			return
		}
		if err := conn.WriteJSON(map[string]string{"event": minimaxEventStarted}); err != nil {
			handlerDone <- err
			return
		}
		if err := conn.ReadJSON(&message); err != nil || message.Event != minimaxEventContinue {
			handlerDone <- fmt.Errorf("read task_continue: %v, event %q", err, message.Event)
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"event": minimaxEventContinued,
			"data":  map[string]string{"audio": "0102"},
		}); err != nil {
			handlerDone <- err
			return
		}
		select {
		case <-firstAudio:
			handlerDone <- nil
		case <-time.After(time.Second):
			handlerDone <- fmt.Errorf("partial audio was not streamed before disconnect")
		}
	}))
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + minimaxTTSURLPath
	tts, err := NewMiniMaxTTS("key", wsURL, "model", "voice", "", 1, 1, 0)
	require.NoError(t, err)

	var chunks [][]byte
	err = tts.Stream(context.Background(), "你好", func(chunk []byte) error {
		chunks = append(chunks, chunk)
		close(firstAudio)
		return nil
	})

	require.Error(t, err)
	require.Equal(t, [][]byte{{0x01, 0x02}}, chunks)
	require.NoError(t, <-handlerDone)
}
