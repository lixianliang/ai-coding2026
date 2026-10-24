package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	ASRProviderQwen            = "qwen"
	ASRProviderDoubao          = "doubao"
	defaultDoubaoURL           = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_async"
	defaultDoubaoResource      = "volc.seedasr.sauc.duration"
	defaultAddress             = ":8080"
	defaultDatabase            = "data/game.db"
	defaultTurnDurationSeconds = 60
	defaultGameDurationSeconds = 600
	defaultASRURL              = "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"
	defaultASRModel            = "qwen3-asr-flash-realtime"
	defaultASRLanguage         = "zh"
	defaultLLMBaseURL          = "https://api.deepseek.com"
	defaultLLMModel            = "deepseek-v4-flash"
	defaultLLMMaxTokens        = 512
	defaultLLMTemperature      = 0.1
	defaultTTSURL              = "api.minimaxi.com"
	defaultTTSModel            = "speech-2.8-turbo"
	defaultTTSSpeed            = 1.0
	defaultTTSVolume           = 1.0
)

type Config struct {
	Address             string           `json:"address"`
	Database            string           `json:"database"`
	DeviceToken         string           `json:"device_token"`
	TurnDurationSeconds int              `json:"turn_duration_seconds"`
	GameDurationSeconds int              `json:"game_duration_seconds"`
	ASR                 ASRConfig        `json:"asr"`
	LLM                 LLMConfig        `json:"llm"`
	TTS                 TTSConfig        `json:"tts"`
	Questions           []QuestionConfig `json:"questions"`
}

type QuestionConfig struct {
	ID      uint   `json:"id"`
	Surface string `json:"surface"`
	Answer  string `json:"answer"`
}

type ASRConfig struct {
	Provider   string `json:"provider"`
	AppID      string `json:"app_id"`
	ResourceID string `json:"resource_id"`
	APIKey     string `json:"api_key"`
	URL        string `json:"url"`
	Model      string `json:"model"`
	Language   string `json:"language"`
}

type LLMConfig struct {
	APIKey      string  `json:"api_key"`
	BaseURL     string  `json:"base_url"`
	Model       string  `json:"model"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
}

type TTSConfig struct {
	APIKey  string  `json:"api_key"`
	URL     string  `json:"url"`
	Model   string  `json:"model"`
	VoiceID string  `json:"voice_id"`
	GroupID string  `json:"group_id"`
	Speed   float64 `json:"speed"`
	Volume  float64 `json:"volume"`
	Pitch   int     `json:"pitch"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Address == "" {
		c.Address = defaultAddress
	}
	if c.Database == "" {
		c.Database = defaultDatabase
	}
	if c.TurnDurationSeconds <= 0 {
		c.TurnDurationSeconds = defaultTurnDurationSeconds
	}
	if c.GameDurationSeconds <= 0 {
		c.GameDurationSeconds = defaultGameDurationSeconds
	}
	if c.ASR.Provider == "" {
		c.ASR.Provider = ASRProviderQwen
	}
	if c.ASR.Provider == ASRProviderDoubao {
		if c.ASR.URL == "" {
			c.ASR.URL = defaultDoubaoURL
		}
		if c.ASR.ResourceID == "" {
			c.ASR.ResourceID = defaultDoubaoResource
		}
	}
	if c.ASR.URL == "" {
		c.ASR.URL = defaultASRURL
	}
	if c.ASR.Model == "" {
		c.ASR.Model = defaultASRModel
	}
	if c.ASR.Language == "" {
		c.ASR.Language = defaultASRLanguage
	}
	if c.LLM.BaseURL == "" {
		c.LLM.BaseURL = defaultLLMBaseURL
	}
	if c.LLM.Model == "" {
		c.LLM.Model = defaultLLMModel
	}
	if c.LLM.MaxTokens <= 0 {
		c.LLM.MaxTokens = defaultLLMMaxTokens
	}
	if c.LLM.Temperature < 0 {
		c.LLM.Temperature = defaultLLMTemperature
	}
	if c.TTS.URL == "" {
		c.TTS.URL = defaultTTSURL
	}
	if c.TTS.Model == "" {
		c.TTS.Model = defaultTTSModel
	}
	if c.TTS.Speed <= 0 {
		c.TTS.Speed = defaultTTSSpeed
	}
	if c.TTS.Volume <= 0 {
		c.TTS.Volume = defaultTTSVolume
	}
}

func (c Config) validate() error {
	seenQuestions := make(map[uint]struct{}, len(c.Questions))
	for _, question := range c.Questions {
		if question.ID == 0 || strings.TrimSpace(question.Surface) == "" || strings.TrimSpace(question.Answer) == "" {
			return errors.New("questions require id, surface, and answer")
		}
		if _, exists := seenQuestions[question.ID]; exists {
			return fmt.Errorf("duplicate question id %d", question.ID)
		}
		seenQuestions[question.ID] = struct{}{}
	}
	if c.ASR.Provider != ASRProviderQwen && c.ASR.Provider != ASRProviderDoubao {
		return fmt.Errorf("unsupported asr.provider %q", c.ASR.Provider)
	}
	if c.ASR.Provider == ASRProviderDoubao && c.ASR.AppID == "" {
		return errors.New("asr.app_id is required for doubao")
	}
	if c.ASR.APIKey == "" {
		return errors.New("asr.api_key is required")
	}
	if c.LLM.APIKey == "" {
		return errors.New("llm.api_key is required")
	}
	if c.TTS.APIKey == "" {
		return errors.New("tts.api_key is required")
	}
	if c.TTS.VoiceID == "" {
		return errors.New("tts.voice_id is required")
	}
	return nil
}
