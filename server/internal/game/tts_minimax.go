package game

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	minimaxTTSURLPath       = "/ws/v1/t2a_v2"
	minimaxEventTaskStart   = "task_start"
	minimaxEventContinue    = "task_continue"
	minimaxEventFinish      = "task_finish"
	minimaxEventConnected   = "connected_success"
	minimaxEventStarted     = "task_started"
	minimaxEventContinued   = "task_continued"
	minimaxEventFinished    = "task_finished"
	minimaxEventFailed      = "task_failed"
	minimaxReadLimit        = 8 << 20
	minimaxDialTimeout      = 10 * time.Second
	minimaxSynthesisTimeout = 30 * time.Second
)

type MiniMaxTTS struct {
	apiKey  string
	wsURL   string
	model   string
	voiceID string
	speed   float64
	volume  float64
	pitch   int
	dialer  websocket.Dialer
}

func NewMiniMaxTTS(apiKey, rawURL, model, voiceID, groupID string, speed, volume float64, pitch int) (*MiniMaxTTS, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("minimax api key is required")
	}
	if strings.TrimSpace(voiceID) == "" {
		return nil, errors.New("minimax voice id is required")
	}
	wsURL, err := minimaxWebSocketURL(rawURL, strings.TrimSpace(groupID))
	if err != nil {
		return nil, err
	}
	if speed <= 0 {
		return nil, errors.New("minimax speed must be positive")
	}
	if volume <= 0 {
		return nil, errors.New("minimax volume must be positive")
	}
	return &MiniMaxTTS{
		apiKey:  strings.TrimSpace(apiKey),
		wsURL:   wsURL,
		model:   strings.TrimSpace(model),
		voiceID: strings.TrimSpace(voiceID),
		speed:   speed,
		volume:  volume,
		pitch:   pitch,
		dialer: websocket.Dialer{
			HandshakeTimeout: minimaxDialTimeout,
		},
	}, nil
}

func minimaxWebSocketURL(rawURL, groupID string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.Contains(rawURL, "://") {
		rawURL = "wss://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse minimax URL: %w", err)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return "", fmt.Errorf("minimax URL must use ws or wss, got %q", parsed.Scheme)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = minimaxTTSURLPath
	}
	if groupID != "" {
		query := parsed.Query()
		query.Set("GroupId", groupID)
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

func (t *MiniMaxTTS) Stream(ctx context.Context, text string, onAudio func([]byte) error) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("minimax text is required")
	}
	if onAudio == nil {
		return errors.New("minimax audio callback is required")
	}
	ctx, cancel := context.WithTimeout(ctx, minimaxSynthesisTimeout)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+t.apiKey)
	conn, response, err := t.dialer.DialContext(ctx, t.wsURL, header)
	if err != nil {
		if response != nil {
			return fmt.Errorf("dial minimax TTS: status %d: %w", response.StatusCode, err)
		}
		return fmt.Errorf("dial minimax TTS: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Debug("close minimax TTS websocket", "error", err)
		}
	}()
	conn.SetReadLimit(minimaxReadLimit)
	cancelWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if err := conn.Close(); err != nil {
				slog.Debug("close cancelled minimax TTS websocket", "error", err)
			}
		case <-cancelWatch:
		}
	}()
	defer close(cancelWatch)
	if err := conn.SetReadDeadline(time.Now().Add(minimaxSynthesisTimeout)); err != nil {
		return fmt.Errorf("set minimax read deadline: %w", err)
	}
	if err := minimaxExpectEvent(conn, minimaxEventConnected); err != nil {
		return err
	}
	start := map[string]any{
		"event":          minimaxEventTaskStart,
		"model":          t.model,
		"language_boost": "Chinese",
		"voice_setting": map[string]any{
			"voice_id": t.voiceID,
			"speed":    t.speed,
			"vol":      t.volume,
			"pitch":    t.pitch,
		},
		"audio_setting": map[string]any{
			"sample_rate": AudioSampleRate,
			"bitrate":     128000,
			"format":      "pcm",
			"channel":     AudioChannels,
		},
	}
	if err := conn.WriteJSON(start); err != nil {
		return fmt.Errorf("send minimax task_start: %w", err)
	}
	if err := minimaxExpectEvent(conn, minimaxEventStarted); err != nil {
		return err
	}
	if err := conn.WriteJSON(map[string]string{"event": minimaxEventContinue, "text": text}); err != nil {
		return fmt.Errorf("send minimax task_continue: %w", err)
	}
	receivedAudio := false
	for {
		var message minimaxResponse
		if err := conn.ReadJSON(&message); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read minimax TTS response: %w", err)
		}
		if err := message.err(); err != nil {
			return err
		}
		switch message.Event {
		case minimaxEventContinued:
			if message.Data.Audio != "" {
				chunk, err := hex.DecodeString(message.Data.Audio)
				if err != nil {
					return fmt.Errorf("decode minimax hex audio: %w", err)
				}
				if len(chunk) > 0 {
					receivedAudio = true
					if err := onAudio(chunk); err != nil {
						return fmt.Errorf("consume minimax audio: %w", err)
					}
				}
			}
			if message.IsFinal {
				if err := conn.WriteJSON(map[string]string{"event": minimaxEventFinish}); err != nil {
					return fmt.Errorf("send minimax task_finish: %w", err)
				}
				if !receivedAudio {
					return errors.New("minimax returned empty audio")
				}
				return nil
			}
		case minimaxEventFinished:
			if !receivedAudio {
				return errors.New("minimax returned empty audio")
			}
			return nil
		}
	}
}

func minimaxExpectEvent(conn *websocket.Conn, expected string) error {
	var message minimaxResponse
	if err := conn.ReadJSON(&message); err != nil {
		return fmt.Errorf("read minimax %s event: %w", expected, err)
	}
	if err := message.err(); err != nil {
		return err
	}
	if message.Event != expected {
		return fmt.Errorf("minimax expected event %q, got %q", expected, message.Event)
	}
	return nil
}

type minimaxResponse struct {
	Event   string `json:"event"`
	IsFinal bool   `json:"is_final"`
	Data    struct {
		Audio string `json:"audio"`
	} `json:"data"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func (r minimaxResponse) err() error {
	if r.Event == minimaxEventFailed || r.BaseResp.StatusCode != 0 {
		return fmt.Errorf("minimax event %q failed: %s (code %d)", r.Event, r.BaseResp.StatusMsg, r.BaseResp.StatusCode)
	}
	return nil
}
