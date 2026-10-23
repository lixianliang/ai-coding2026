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

func TestMiniMaxSynthesizeDecodesHexAudio(t *testing.T) {
	handlerDone := make(chan error, 1)
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
			"event":    minimaxEventContinued,
			"is_final": true,
			"data":     map[string]string{"audio": "0102"},
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

	pcm, err := tts.Synthesize(context.Background(), "你好")

	require.NoError(t, err)
	require.Equal(t, []byte{0x01, 0x02}, pcm)
	require.NoError(t, <-handlerDone)
}
