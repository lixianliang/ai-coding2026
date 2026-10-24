package game

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lixianliang/ai-coding2026/server/internal/config"
)

const (
	doubaoPCMFormat              = "pcm"
	doubaoHeaderApp              = "X-Api-App-Key"
	doubaoHeaderAccess           = "X-Api-Access-Key"
	doubaoHeaderResource         = "X-Api-Resource-Id"
	doubaoHeaderConnect          = "X-Api-Connect-Id"
	doubaoFullRequest       byte = 0x10
	doubaoAudioRequest      byte = 0x20
	doubaoResponse          byte = 9
	doubaoError             byte = 15
	doubaoPacketInterval         = 200 * time.Millisecond
	doubaoPacketBytes            = AudioSampleRate * pcmSampleBytes / 5
	doubaoFinalizeSilenceMS      = 600
	doubaoFinalTimeout           = 10 * time.Second
	doubaoHandshakeTimeout       = 10 * time.Second
	doubaoWriteTimeout           = 5 * time.Second
	doubaoEventBuffer            = 64
	doubaoModel                  = "bigmodel"
	doubaoSSDVersion             = "200"
	doubaoVADWindowMS            = 800
	doubaoSampleBits             = 16
	doubaoReadLimit              = 4 << 20
)

// NewASR keeps provider selection shared by the server and cloud evaluations.
func NewASR(cfg config.ASRConfig) (ASR, error) {
	switch cfg.Provider {
	case config.ASRProviderQwen:
		return NewQwenASR(cfg.APIKey, cfg.URL, cfg.Model, cfg.Language)
	case config.ASRProviderDoubao:
		return NewDoubaoASR(cfg.APIKey, cfg.AppID, cfg.URL, cfg.ResourceID)
	default:
		return nil, fmt.Errorf("unsupported ASR provider %q", cfg.Provider)
	}
}

type DoubaoASR struct{ apiKey, appID, apiURL, resourceID string }

func NewDoubaoASR(apiKey, appID, rawURL, resourceID string) (*DoubaoASR, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "wss" && u.Scheme != "ws") {
		return nil, errors.New("doubao ASR requires a WebSocket URL")
	}
	if apiKey == "" || appID == "" || resourceID == "" {
		return nil, errors.New("doubao ASR requires api_key, app_id and resource_id")
	}
	return &DoubaoASR{apiKey: apiKey, appID: appID, apiURL: rawURL, resourceID: resourceID}, nil
}

func (a *DoubaoASR) Open(ctx context.Context, deviceID string) (ASRSession, error) {
	h := http.Header{}
	h.Set(doubaoHeaderApp, a.appID)
	h.Set(doubaoHeaderAccess, a.apiKey)
	h.Set(doubaoHeaderResource, a.resourceID)
	h.Set(doubaoHeaderConnect, newSessionID())
	d := websocket.Dialer{HandshakeTimeout: doubaoHandshakeTimeout}
	conn, response, err := d.DialContext(ctx, a.apiURL, h)
	if err != nil {
		if response != nil {
			if closeErr := response.Body.Close(); closeErr != nil {
				slog.Warn("close doubao handshake response", "error", closeErr)
			}
			return nil, fmt.Errorf("doubao ASR handshake: HTTP %d", response.StatusCode)
		}
		return nil, fmt.Errorf("dial doubao ASR: %w", err)
	}
	conn.SetReadLimit(doubaoReadLimit)
	s := &doubaoASRSession{conn: conn, events: make(chan ASREvent, doubaoEventBuffer), done: make(chan struct{})}
	// The connection owns anonymous speaker IDs; never reconnect between questions.
	request := struct {
		User struct {
			UID string `json:"uid"`
		} `json:"user"`
		Audio struct {
			Format  string `json:"format"`
			Rate    int    `json:"rate"`
			Bits    int    `json:"bits"`
			Channel int    `json:"channel"`
		} `json:"audio"`
		Request struct {
			Model      string `json:"model_name"`
			Nonstream  bool   `json:"enable_nonstream"`
			Speaker    bool   `json:"enable_speaker_info"`
			SSD        string `json:"ssd_version"`
			Utterances bool   `json:"show_utterances"`
			Window     int    `json:"end_window_size"`
		} `json:"request"`
	}{}
	request.User.UID = deviceID
	request.Audio.Format = doubaoPCMFormat
	request.Audio.Rate = AudioSampleRate
	request.Audio.Bits = doubaoSampleBits
	request.Audio.Channel = AudioChannels
	request.Request.Model = doubaoModel
	request.Request.Nonstream = true
	request.Request.Speaker = true
	request.Request.SSD = doubaoSSDVersion
	request.Request.Utterances = true
	request.Request.Window = doubaoVADWindowMS
	raw, err := json.Marshal(request)
	if err == nil {
		err = s.writePacket(doubaoFullRequest, raw)
	}
	if err != nil {
		if closeErr := s.Close(); closeErr != nil {
			slog.Warn("close doubao ASR", "error", closeErr)
		}
		return nil, err
	}
	go s.readLoop()
	go s.keepAlive(ctx)
	return s, nil
}

type doubaoASRSession struct {
	conn               *websocket.Conn
	events             chan ASREvent
	done               chan struct{}
	closeOnce          sync.Once
	mu                 sync.Mutex
	sentBytes          int64
	startMS, endMS     int64
	capturing, pending bool
	deadline           time.Time
}

func (s *doubaoASRSession) Push(ctx context.Context, pcm []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pcm) == 0 || len(pcm)%pcmSampleBytes != 0 {
		return errors.New("doubao PCM must contain complete samples")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending {
		return errors.New("doubao ASR is finalizing the previous question")
	}
	if !s.capturing {
		s.startMS = s.audioMS()
		s.capturing = true
	}
	for len(pcm) > 0 {
		n := min(len(pcm), doubaoPacketBytes)
		if err := s.sendAudio(pcm[:n]); err != nil {
			return err
		}
		pcm = pcm[n:]
	}
	return nil
}
func (s *doubaoASRSession) Finalize(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.capturing {
		return errors.New("doubao ASR has no captured audio")
	}
	s.capturing = false
	s.pending = true
	s.endMS = s.audioMS()
	s.deadline = time.Now().Add(doubaoFinalTimeout)
	return nil
}
func (s *doubaoASRSession) Events() <-chan ASREvent { return s.events }
func (s *doubaoASRSession) Close() error {
	var err error
	s.closeOnce.Do(func() { close(s.done); err = s.conn.Close() })
	return err
}
func (s *doubaoASRSession) audioMS() int64 {
	return s.sentBytes * 1000 / (AudioSampleRate * pcmSampleBytes)
}
func (s *doubaoASRSession) sendAudio(pcm []byte) error {
	if err := s.writePacket(doubaoAudioRequest, pcm); err != nil {
		return err
	}
	s.sentBytes += int64(len(pcm))
	return nil
}
func (s *doubaoASRSession) writePacket(kind byte, data []byte) error {
	packet := []byte{0x11, kind, 0, 0}
	if kind == doubaoFullRequest {
		packet[2] = 0x10
	}
	packet = binary.BigEndian.AppendUint32(packet, uint32(len(data)))
	packet = append(packet, data...)
	if err := s.conn.SetWriteDeadline(time.Now().Add(doubaoWriteTimeout)); err != nil {
		return fmt.Errorf("doubao write deadline: %w", err)
	}
	if err := s.conn.WriteMessage(websocket.BinaryMessage, packet); err != nil {
		return fmt.Errorf("doubao write: %w", err)
	}
	return nil
}
func (s *doubaoASRSession) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(doubaoPacketInterval)
	defer ticker.Stop()
	silence := make([]byte, doubaoPacketBytes)
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			if err := s.Close(); err != nil {
				slog.Warn("close doubao ASR", "error", err)
			}
			return
		case <-ticker.C:
			s.mu.Lock()
			var err error
			if !s.capturing {
				err = s.sendAudio(silence)
			}
			if s.pending && time.Now().After(s.deadline) {
				err = errors.New("doubao final transcript timeout")
			}
			s.mu.Unlock()
			if err != nil {
				slog.Warn("doubao ASR stream failed", "error", err)
				if closeErr := s.Close(); closeErr != nil {
					slog.Warn("close doubao ASR", "error", closeErr)
				}
				return
			}
		}
	}
}

type doubaoResult struct {
	AudioInfo struct {
		Duration int64 `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Utterances []struct {
			Start     int64  `json:"start_time"`
			End       int64  `json:"end_time"`
			Text      string `json:"text"`
			Definite  bool   `json:"definite"`
			Additions struct {
				Speaker string `json:"speaker_id"`
			} `json:"additions"`
		} `json:"utterances"`
	} `json:"result"`
}

func decodeDoubaoPacket(raw []byte) (doubaoResult, error) {
	var result doubaoResult
	if len(raw) < 4 {
		return result, errors.New("short doubao header")
	}
	offset := int(raw[0]&15) * 4
	kind := raw[1] >> 4
	if offset < 4 || raw[0]>>4 != 1 {
		return result, errors.New("invalid doubao header")
	}
	if raw[1]&1 != 0 || kind == doubaoError {
		offset += 4
	}
	if len(raw) < offset+4 {
		return result, errors.New("short doubao payload length")
	}
	size := int(binary.BigEndian.Uint32(raw[offset : offset+4]))
	offset += 4
	if size > len(raw)-offset {
		return result, errors.New("truncated doubao payload")
	}
	payload := raw[offset : offset+size]
	if raw[2]&15 == 1 {
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return result, fmt.Errorf("doubao gzip: %w", err)
		}
		payload, err = io.ReadAll(io.LimitReader(reader, doubaoReadLimit+1))
		closeErr := reader.Close()
		if err != nil {
			return result, fmt.Errorf("read doubao gzip: %w", err)
		}
		if closeErr != nil {
			return result, fmt.Errorf("close doubao gzip: %w", closeErr)
		}
		if len(payload) > doubaoReadLimit {
			return result, errors.New("doubao payload too large")
		}
	} else if raw[2]&15 != 0 {
		return result, errors.New("unsupported doubao compression")
	}
	if kind == doubaoError {
		return result, errors.New("doubao ASR rejected the request")
	}
	if kind != doubaoResponse {
		return result, errors.New("unexpected doubao message type")
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return result, fmt.Errorf("decode doubao result: %w", err)
	}
	return result, nil
}

func (s *doubaoASRSession) accept(result doubaoResult) (ASREvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.capturing && !s.pending {
		return ASREvent{}, false
	}
	var parts []string
	speaker := ""
	mixed := false
	definite := true
	for _, u := range result.Result.Utterances {
		if u.End <= s.startMS {
			continue
		}
		text := strings.TrimSpace(u.Text)
		if text == "" {
			continue
		}
		parts = append(parts, text)
		definite = definite && u.Definite
		if u.Additions.Speaker == "" {
			mixed = true
		} else if speaker == "" {
			speaker = u.Additions.Speaker
		} else if speaker != u.Additions.Speaker {
			mixed = true
		}
	}
	event := ASREvent{Kind: ASREventPartial, Text: strings.Join(parts, " ")}
	if s.pending && definite && result.AudioInfo.Duration >= s.endMS+doubaoFinalizeSilenceMS {
		s.pending = false
		event.Kind = ASREventFinal
		if !mixed {
			event.SpeakerID = speaker
		}
	}
	return event, true
}
func (s *doubaoASRSession) readLoop() {
	defer close(s.events)
	defer func() {
		if err := s.Close(); err != nil {
			slog.Warn("close doubao ASR", "error", err)
		}
	}()
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			slog.Warn("read doubao ASR", "error", err)
			return
		}
		result, err := decodeDoubaoPacket(raw)
		if err != nil {
			slog.Warn("decode doubao ASR", "error", err)
			return
		}
		if event, ok := s.accept(result); ok {
			select {
			case s.events <- event:
			case <-s.done:
				return
			}
		}
	}
}
