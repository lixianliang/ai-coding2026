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
	"github.com/lixianliang/ai-coding2026/server/internal/game"
)

const (
	envAddress = "GAME_ADDR"
	envDBPath  = "GAME_DB"
	envToken   = "GAME_TOKEN"

	defaultAddress = ":8080"
	defaultDBPath  = "data/game.db"
	shutdownGrace  = 10 * time.Second
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
	dbPath := envOrDefault(envDBPath, defaultDBPath)
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
			return err
		}
	}
	store, err := game.OpenStore(dbPath)
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
	manager, err := game.NewManager(game.Deps{
		Store:  store,
		ASR:    game.DebugASR{},
		TTS:    game.SilenceTTS{},
		Judge:  game.RuleJudge{},
		Codec:  codec,
		Logger: logger,
	})
	if err != nil {
		return err
	}
	defer manager.Close()
	token := strings.TrimSpace(os.Getenv(envToken))
	if token == "" {
		logger.Warn("GAME_TOKEN is empty; websocket authentication is disabled")
	}
	router := api.NewRouter(manager, token, logger)
	address := envOrDefault(envAddress, defaultAddress)
	server := &http.Server{Addr: address, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("game server listening", "address", address)
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
