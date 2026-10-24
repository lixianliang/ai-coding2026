package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lixianliang/ai-coding2026/server/internal/api"
	"github.com/lixianliang/ai-coding2026/server/internal/config"
	"github.com/lixianliang/ai-coding2026/server/internal/game"
)

const (
	envConfig     = "GAME_CONFIG"
	defaultConfig = "config.local.json"
	shutdownGrace = 10 * time.Second
)

func main() {
	gin.SetMode(gin.ReleaseMode)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := envOrDefault(envConfig, defaultConfig)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if cfg.Database != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(cfg.Database), 0o750); err != nil {
			return err
		}
	}
	questions := make([]game.Question, len(cfg.Questions))
	for index, question := range cfg.Questions {
		questions[index] = game.Question{ID: question.ID, Surface: question.Surface, Answer: question.Answer}
	}
	store, err := game.OpenStore(cfg.Database, questions)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close store", "error", err)
		}
	}()
	codec, err := game.NewOpusCodec()
	if err != nil {
		return err
	}
	defer func() {
		if err := codec.Close(); err != nil {
			logger.Error("close audio codec", "error", err)
		}
	}()
	asr, err := game.NewASR(cfg.ASR)
	if err != nil {
		return err
	}
	judge, err := game.NewDeepSeekJudge(cfg.LLM.APIKey, cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.MaxTokens, cfg.LLM.Temperature)
	if err != nil {
		return err
	}
	tts, err := game.NewMiniMaxTTS(cfg.TTS.APIKey, cfg.TTS.URL, cfg.TTS.Model, cfg.TTS.VoiceID, cfg.TTS.GroupID, cfg.TTS.Speed, cfg.TTS.Volume, cfg.TTS.Pitch)
	if err != nil {
		return err
	}
	pipeline, err := game.NewPipeline(asr, judge, tts)
	if err != nil {
		return err
	}
	manager, err := game.NewManager(game.Deps{
		Store:        store,
		ASR:          asr,
		TTS:          tts,
		Judge:        judge,
		Codec:        codec,
		Logger:       logger,
		TurnDuration: time.Duration(cfg.TurnDurationSeconds) * time.Second,
		GameDuration: time.Duration(cfg.GameDurationSeconds) * time.Second,
	})
	if err != nil {
		return err
	}
	defer manager.Close()
	if strings.TrimSpace(cfg.DeviceToken) == "" {
		logger.Warn("device_token is empty; websocket authentication is disabled")
	}
	router := api.NewRouter(manager, pipeline, cfg.DeviceToken, logger)
	server := &http.Server{Addr: cfg.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("game server listening", "address", cfg.Address)
		errCh <- server.ListenAndServe()
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-signals:
		manager.Close()
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return err
		}
	}
	return nil
}

func envOrDefault(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
