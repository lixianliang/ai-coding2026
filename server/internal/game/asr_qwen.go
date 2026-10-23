package game

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	qwenRealtimeEventSessionUpdate     = "session.update"
	qwenRealtimeEventAudioAppend       = "input_audio_buffer.append"
	qwenRealtimeEventAudioCommit       = "input_audio_buffer.commit"
	qwenRealtimeEventSessionUpdated    = "session.updated"
	qwenRealtimeEventTranscriptionText = "conversation.item.input_audio_transcription.text"
	qwenRealtimeEventTranscriptionDone = "conversation.item.input_audio_transcription.completed"
	qwenRealtimeEventTranscriptionFail = "conversation.item.input_audio_transcription.failed"
	qwenRealtimeEventError             = "error"
	qwenRealtimeEventFinished          = "session.finished"
	qwenRealtimeEventIDPrefix          = "game"
	qwenRealtimeReadyTimeout           = 10 * time.Second
	qwenRealtimeWriteTimeout           = 5 * time.Second
	qwenRealtimeReadLimit              = 1 << 20
	qwenRealtimeEventBuffer            = 64
)

type QwenASR struct {
	apiKey   string
	apiURL   string
	language string
	dialer   websocket.Dialer
}

func NewQwenASR(apiKey, rawURL, model, language string) (*QwenASR, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("qwen ASR api key is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse qwen ASR URL: %w", err)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return nil, fmt.Errorf("qwen ASR URL must use ws or wss, got %q", parsed.Scheme)
	}
	query := parsed.Query()
	query.Set("model", model)
	parsed.RawQuery = query.Encode()
	return &QwenASR{
		apiKey:   apiKey,
		apiURL:   parsed.String(),
		language: strings.TrimSpace(language),
		dialer: websocket.Dialer{
			HandshakeTimeout: qwenRealtimeReadyTimeout,
		},
	}, nil
}

func (a *QwenASR) Open(ctx context.Context, deviceID string) (ASRSession, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+a.apiKey)
	conn, response, err := a.dialer.DialContext(ctx, a.apiURL, header)
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("dial qwen ASR: status %d: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("dial qwen ASR: %w", err)
	}
	conn.SetReadLimit(qwenRealtimeReadLimit)
	session := &qwenASRSession{
		conn:   conn,
		events: make(chan ASREvent, qwenRealtimeEventBuffer),
		logger: slog.Default().With("component", "qwen_asr", "device_id", deviceID),
	}
	update := qwenClientEvent{
		EventID: session.nextEventID(),
		Type:    qwenRealtimeEventSessionUpdate,
		Session: &qwenSessionConfig{
			InputAudioFormat: "pcm",
			SampleRate:       AudioSampleRate,
		},
	}
	if a.language != "" {
		update.Session.InputAudioTranscription = &qwenTranscriptionConfig{Language: a.language}
	}
	if err := session.writeJSON(update); err != nil {
		session.close()
		return nil, err
	}
	deadline := time.Now().Add(qwenRealtimeReadyTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		session.close()
		return nil, fmt.Errorf("set qwen ASR ready deadline: %w", err)
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			session.close()
			return nil, fmt.Errorf("wait for qwen ASR session.updated: %w", err)
		}
		var event qwenServerEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			session.close()
			return nil, fmt.Errorf("decode qwen ASR ready event: %w", err)
		}
		switch event.Type {
		case qwenRealtimeEventSessionUpdated:
			if err := conn.SetReadDeadline(time.Time{}); err != nil {
				session.close()
				return nil, fmt.Errorf("clear qwen ASR ready deadline: %w", err)
			}
			go session.readLoop()
			return session, nil
		case qwenRealtimeEventError, qwenRealtimeEventTranscriptionFail:
			session.close()
			return nil, event.providerError()
		}
	}
}

type qwenASRSession struct {
	conn     *websocket.Conn
	events   chan ASREvent
	logger   *slog.Logger
	eventSeq atomic.Uint64
	writeMu  sync.Mutex
	eventMu  sync.Mutex
	closed   atomic.Bool
}

func (s *qwenASRSession) Push(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s.closed.Load() {
		return errors.New("qwen ASR session is closed")
	}
	request := qwenClientEvent{
		EventID: s.nextEventID(),
		Type:    qwenRealtimeEventAudioAppend,
		Audio:   base64.StdEncoding.EncodeToString(pcm),
	}
	return s.writeJSON(request)
}

func (s *qwenASRSession) Finalize(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s.closed.Load() {
		return errors.New("qwen ASR session is closed")
	}
	return s.writeJSON(qwenClientEvent{
		EventID: s.nextEventID(),
		Type:    qwenRealtimeEventAudioCommit,
	})
}

func (s *qwenASRSession) Events() <-chan ASREvent {
	return s.events
}

func (s *qwenASRSession) Close() error {
	s.close()
	return nil
}

func (s *qwenASRSession) nextEventID() string {
	return fmt.Sprintf("%s_%d", qwenRealtimeEventIDPrefix, s.eventSeq.Add(1))
}

func (s *qwenASRSession) writeJSON(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(qwenRealtimeWriteTimeout)); err != nil {
		return fmt.Errorf("set qwen ASR write deadline: %w", err)
	}
	if err := s.conn.WriteJSON(value); err != nil {
		s.close()
		return fmt.Errorf("write qwen ASR request: %w", err)
	}
	return nil
}

func (s *qwenASRSession) readLoop() {
	defer s.close()
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			if !s.closed.Load() {
				s.logger.Warn("qwen ASR read failed", "error", err)
			}
			return
		}
		var event qwenServerEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			s.logger.Warn("ignore malformed qwen ASR event", "error", err)
			continue
		}
		switch event.Type {
		case qwenRealtimeEventTranscriptionText:
			if text := strings.TrimSpace(event.Text + event.Stash); text != "" {
				s.emit(ASREvent{Kind: ASREventPartial, Text: text})
			}
		case qwenRealtimeEventTranscriptionDone:
			text := strings.TrimSpace(event.Transcript)
			if text == "" {
				text = strings.TrimSpace(event.Text + event.Stash)
			}
			s.emitFinal(text)
		case qwenRealtimeEventError, qwenRealtimeEventTranscriptionFail:
			s.logger.Warn("qwen ASR session failed", "error", event.providerError())
			return
		case qwenRealtimeEventFinished:
			return
		}
	}
}

func (s *qwenASRSession) emit(event ASREvent) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.closed.Load() {
		return
	}
	select {
	case s.events <- event:
	default:
	}
}

func (s *qwenASRSession) emitFinal(text string) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.closed.Load() {
		return
	}
	select {
	case s.events <- ASREvent{Kind: ASREventFinal, Text: text}:
	default:
		select {
		case <-s.events:
		default:
		}
		s.events <- ASREvent{Kind: ASREventFinal, Text: text}
	}
}

func (s *qwenASRSession) close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	if err := s.conn.Close(); err != nil {
		s.logger.Debug("close qwen ASR websocket", "error", err)
	}
	s.eventMu.Lock()
	close(s.events)
	s.eventMu.Unlock()
}

type qwenClientEvent struct {
	EventID string             `json:"event_id"`
	Type    string             `json:"type"`
	Session *qwenSessionConfig `json:"session,omitempty"`
	Audio   string             `json:"audio,omitempty"`
}

type qwenSessionConfig struct {
	InputAudioFormat        string                   `json:"input_audio_format"`
	SampleRate              int                      `json:"sample_rate"`
	InputAudioTranscription *qwenTranscriptionConfig `json:"input_audio_transcription,omitempty"`
	TurnDetection           any                      `json:"turn_detection"`
}

type qwenTranscriptionConfig struct {
	Language string `json:"language"`
}

type qwenServerEvent struct {
	Type       string        `json:"type"`
	EventID    string        `json:"event_id"`
	Text       string        `json:"text"`
	Stash      string        `json:"stash"`
	Transcript string        `json:"transcript"`
	Error      *qwenAPIError `json:"error"`
}

type qwenAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

func (e qwenServerEvent) providerError() error {
	if e.Error == nil {
		return fmt.Errorf("qwen ASR event %s failed", e.Type)
	}
	parts := []string{e.Type}
	if e.Error.Code != "" {
		parts = append(parts, e.Error.Code)
	}
	if e.Error.Message != "" {
		parts = append(parts, e.Error.Message)
	}
	if e.Error.Param != "" {
		parts = append(parts, e.Error.Param)
	}
	return errors.New(strings.Join(parts, ": "))
}
