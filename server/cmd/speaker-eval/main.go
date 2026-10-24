// speaker-eval replays labeled PCM utterances over one real ASR connection.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lixianliang/ai-coding2026/server/internal/config"
	"github.com/lixianliang/ai-coding2026/server/internal/game"
)

const (
	sampleBytes    = 2
	bytesPerSecond = game.AudioSampleRate * sampleBytes
	chunkBytes     = bytesPerSecond / 5
)

func main() {
	configPath := flag.String("config", "config.local.json", "local configuration")
	files := flag.String("pcm", "", "comma-separated 16 kHz mono s16le PCM files")
	labels := flag.String("speakers", "A,B,A", "expected speaker labels in file order")
	pause := flag.Duration("pause", 2*time.Second, "discussion pause between questions")
	flag.Parse()
	if err := run(*configPath, strings.Split(*files, ","), strings.Split(*labels, ","), *pause); err != nil {
		slog.Error("speaker evaluation failed", "error", err)
		os.Exit(1)
	}
}
func run(path string, files, labels []string, pause time.Duration) error {
	if len(files) != len(labels) || len(files) < 3 {
		return errors.New("provide at least three PCM files and matching speaker labels, including a returning speaker")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	asr, err := game.NewASR(cfg.ASR)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, err := asr.Open(ctx, "speaker-eval")
	if err != nil {
		return err
	}
	defer func() {
		if err := session.Close(); err != nil {
			slog.Warn("close evaluation ASR", "error", err)
		}
	}()
	expected := map[string]string{}
	owners := map[string]string{}
	for index, file := range files {
		pcm, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read PCM %d: %w", index+1, err)
		}
		if len(pcm) == 0 || len(pcm)%sampleBytes != 0 {
			return errors.New("PCM must contain complete s16le samples")
		}
		for len(pcm) > 0 {
			n := min(len(pcm), chunkBytes)
			if err := session.Push(ctx, pcm[:n]); err != nil {
				return err
			}
			pcm = pcm[n:]
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(n) * time.Second / bytesPerSecond):
			}
		}
		started := time.Now()
		if err := session.Finalize(ctx); err != nil {
			return err
		}
		event, err := finalEvent(ctx, session)
		if err != nil {
			return err
		}
		label := labels[index]
		if event.SpeakerID == "" {
			return fmt.Errorf("turn %d: no unique speaker", index+1)
		}
		if id, ok := expected[label]; ok && id != event.SpeakerID {
			return fmt.Errorf("turn %d: returning speaker changed from %s to %s", index+1, id, event.SpeakerID)
		}
		if owner, ok := owners[event.SpeakerID]; ok && owner != label {
			return fmt.Errorf("turn %d: different speakers merged", index+1)
		}
		expected[label] = event.SpeakerID
		owners[event.SpeakerID] = label
		slog.Info("speaker case passed", "turn", index+1, "expected", label, "speaker_id", event.SpeakerID, "transcript", event.Text, "final_latency", time.Since(started))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
	if len(expected) < 2 || len(expected) == len(files) {
		return errors.New("evaluation needs different speakers and at least one returning speaker")
	}
	slog.Info("speaker evaluation passed", "turns", len(files), "speakers", len(expected))
	return nil
}
func finalEvent(ctx context.Context, session game.ASRSession) (game.ASREvent, error) {
	for {
		select {
		case <-ctx.Done():
			return game.ASREvent{}, ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return game.ASREvent{}, errors.New("ASR closed before final")
			}
			if event.Kind == game.ASREventFinal {
				if strings.TrimSpace(event.Text) == "" {
					return event, errors.New("empty transcript")
				}
				return event, nil
			}
		}
	}
}
