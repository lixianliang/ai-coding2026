package game

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDoubaoPacketAndTurnBoundaries(t *testing.T) {
	raw := []byte(`{"audio_info":{"duration":3000},"result":{"utterances":[{"end_time":500,"text":"old","definite":true,"additions":{"speaker_id":"9"}},{"start_time":1100,"end_time":2200,"text":"他认识厨师吗？","definite":true,"additions":{"speaker_id":"0"}}]}}`)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	_, err := z.Write(raw)
	require.NoError(t, err)
	require.NoError(t, z.Close())
	packet := []byte{0x11, 0x91, 0x11, 0}
	packet = binary.BigEndian.AppendUint32(packet, 1)
	packet = binary.BigEndian.AppendUint32(packet, uint32(compressed.Len()))
	packet = append(packet, compressed.Bytes()...)
	result, err := decodeDoubaoPacket(packet)
	require.NoError(t, err)
	s := &doubaoASRSession{startMS: 1000, endMS: 2300, capturing: true}
	event, ok := s.accept(result)
	require.True(t, ok)
	require.Equal(t, ASREventPartial, event.Kind)
	require.Equal(t, "他认识厨师吗？", event.Text)
	s.capturing = false
	s.pending = true
	event, ok = s.accept(result)
	require.True(t, ok)
	require.Equal(t, ASREventFinal, event.Kind)
	require.Equal(t, "0", event.SpeakerID)
	_, ok = s.accept(result)
	require.False(t, ok, "repeated full results must not judge twice")
	s.startMS = 3300
	s.capturing = true
	event, ok = s.accept(result)
	require.True(t, ok)
	require.Empty(t, event.Text, "old utterances must not leak into next question")
}

func TestDoubaoAmbiguousAndMissingSpeakers(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		want          string
	}{
		{"one", `[{"end_time":1000,"text":"question","definite":true,"additions":{"speaker_id":"0"}}]`, "0"},
		{"two sentences same speaker", `[{"end_time":500,"text":"one","definite":true,"additions":{"speaker_id":"0"}},{"end_time":1000,"text":"two","definite":true,"additions":{"speaker_id":"0"}}]`, "0"},
		{"multiple speakers", `[{"end_time":500,"text":"one","definite":true,"additions":{"speaker_id":"0"}},{"end_time":1000,"text":"two","definite":true,"additions":{"speaker_id":"1"}}]`, ""},
		{"unknown", `[{"end_time":1000,"text":"question","definite":true}]`, ""},
		{"silence", `[]`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var result doubaoResult
			require.NoError(t, json.Unmarshal([]byte(`{"audio_info":{"duration":2000},"result":{"utterances":`+test.payload+`}}`), &result))
			s := &doubaoASRSession{endMS: 1200, pending: true}
			event, ok := s.accept(result)
			require.True(t, ok)
			require.Equal(t, ASREventFinal, event.Kind)
			require.Equal(t, test.want, event.SpeakerID)
		})
	}
}

func TestDoubaoRejectsMalformedPackets(t *testing.T) {
	for _, raw := range [][]byte{nil, {0x11, 0x91, 0x10, 0}, {0x11, 0x90, 0x10, 0, 0, 0, 0, 255}, {0x10, 0x90, 0x10, 0, 0, 0, 0, 0}, {0x11, 0xf0, 0x10, 0, 0, 0, 0, 1, 0, 0, 0, 0}, {0x11, 0x90, 0x11, 0, 0, 0, 0, 1, 0}} {
		_, err := decodeDoubaoPacket(raw)
		require.Error(t, err)
	}
}

func TestAnonymousPlayerNumbers(t *testing.T) {
	g := &activeGame{}
	require.Empty(t, g.playerForSpeaker(""))
	require.Equal(t, "玩家 1", g.playerForSpeaker("7"))
	require.Equal(t, "玩家 2", g.playerForSpeaker("0"))
	require.Equal(t, "玩家 3", g.playerForSpeaker("10"))
	require.Equal(t, "玩家 1", g.playerForSpeaker("7"))
	require.Equal(t, "玩家 1", (&activeGame{}).playerForSpeaker("10"))
	result, err := parseJudgeResult(`{"verdict":"yes","player":"伪造名字"}`)
	require.NoError(t, err)
	require.Equal(t, JudgeResult{Verdict: VerdictYes}, result)
}

func TestDoubaoStreamingSessionKeepsConnectionAcrossTurns(t *testing.T) {
	upgraded := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(doubaoHeaderResource) != "resource" {
			http.Error(w, "missing resource", http.StatusBadRequest)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Log(err)
			}
		}()
		upgraded <- struct{}{}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var config struct {
			Request struct {
				Speaker   bool `json:"enable_speaker_info"`
				Nonstream bool `json:"enable_nonstream"`
			} `json:"request"`
		}
		if err := json.Unmarshal(raw[8:], &config); err != nil {
			t.Error(err)
			return
		}
		if !config.Request.Speaker || !config.Request.Nonstream {
			t.Error("speaker options missing")
			return
		}
		var sent int64
		var utterances []map[string]any
		for {
			_, packet, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if packet[1] != doubaoAudioRequest {
				t.Error("manual stop must not end provider stream")
				return
			}
			pcm := packet[8:]
			sent += int64(len(pcm))
			ms := sent * 1000 / (AudioSampleRate * pcmSampleBytes)
			if len(pcm) > 0 && pcm[0] != 0 {
				utterances = append(utterances, map[string]any{"start_time": ms - 200, "end_time": ms, "text": "问题", "definite": true, "additions": map[string]string{"speaker_id": string(pcm[0])}})
			}
			if len(pcm) == 0 || pcm[0] == 0 {
				continue
			} // Optimized ASR does not emit unchanged silence results.
			payload, err := json.Marshal(map[string]any{"audio_info": map[string]int64{"duration": ms}, "result": map[string]any{"utterances": utterances}})
			if err != nil {
				t.Error(err)
				return
			}
			frame := []byte{0x11, 0x90, 0x10, 0}
			frame = binary.BigEndian.AppendUint32(frame, uint32(len(payload)))
			frame = append(frame, payload...)
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	provider, err := NewDoubaoASR("token", "app", "ws"+strings.TrimPrefix(server.URL, "http"), "resource")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := provider.Open(ctx, "test")
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()
	for _, speaker := range []byte{'a', 'b', 'a'} {
		pcm := make([]byte, doubaoPacketBytes)
		pcm[0] = speaker
		require.NoError(t, session.Push(ctx, pcm))
		require.NoError(t, session.Finalize(ctx))
		found := false
		for !found {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case event, ok := <-session.Events():
				require.True(t, ok)
				if event.Kind == ASREventFinal {
					require.Equal(t, string(speaker), event.SpeakerID)
					require.Equal(t, "问题", event.Text)
					found = true
				}
			}
		}
	}
	require.Len(t, upgraded, 1)
}

func TestDoubaoFinalizesCachedDefiniteAfterLateManualStop(t *testing.T) {
	var result doubaoResult
	require.NoError(t, json.Unmarshal([]byte(`{"audio_info":{"duration":1800},"result":{"utterances":[{"end_time":1600,"text":"再说一遍汤面","definite":true,"additions":{"speaker_id":"0"}}]}}`), &result))
	s := &doubaoASRSession{capturing: true}
	event, ok := s.accept(result)
	require.True(t, ok)
	require.Equal(t, ASREventPartial, event.Kind)
	s.capturing = false
	s.pending = true
	s.endMS = 4000
	event, ok = s.resultEvent(s.latest, true)
	require.True(t, ok)
	require.Equal(t, ASREventFinal, event.Kind)
	require.Equal(t, "再说一遍汤面", event.Text)
	require.False(t, s.pending)
}
