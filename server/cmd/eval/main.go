package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lixianliang/ai-coding2026/server/internal/config"
	"github.com/lixianliang/ai-coding2026/server/internal/game"
)

type evalCase struct {
	name      string
	utterance string
	want      game.Verdict
}

var cases = []evalCase{
	{name: "yes", utterance: "杯子里的水是冰吗？", want: game.VerdictYes},
	{name: "no", utterance: "火柴是被风吹灭的吗？", want: game.VerdictNo},
	{name: "close", utterance: "火柴落到了某种能托住它的东西上，对吗？", want: game.VerdictClose},
	{name: "irrelevant", utterance: "这家餐厅今天几点关门？", want: game.VerdictIrrelevant},
	{name: "correct", utterance: "杯子里的水冻成冰了，火柴掉在冰面上，所以没有熄灭。", want: game.VerdictCorrect},
}

func main() {
	configPath := flag.String("config", "config.local.json", "configuration JSON path")
	e2e := flag.Bool("e2e", false, "synthesize each utterance, then run ASR → judge → TTS")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	judge, err := game.NewDeepSeekJudge(cfg.LLM.APIKey, cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.MaxTokens, cfg.LLM.Temperature)
	if err != nil {
		fail(err)
	}
	tts, err := game.NewMiniMaxTTS(cfg.TTS.APIKey, cfg.TTS.URL, cfg.TTS.Model, cfg.TTS.VoiceID, cfg.TTS.GroupID, cfg.TTS.Speed, cfg.TTS.Volume, cfg.TTS.Pitch)
	if err != nil {
		fail(err)
	}
	var pipeline *game.Pipeline
	if *e2e {
		asr, err := game.NewQwenASR(cfg.ASR.APIKey, cfg.ASR.URL, cfg.ASR.Model, cfg.ASR.Language)
		if err != nil {
			fail(err)
		}
		pipeline, err = game.NewPipeline(asr, judge, tts)
		if err != nil {
			fail(err)
		}
	}

	passed := 0
	started := time.Now()
	for _, test := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		caseStarted := time.Now()
		transcript := test.utterance
		var got game.Verdict
		if pipeline == nil {
			got, err = judge.Judge(ctx, game.TurtleSoupDemo.Surface, game.TurtleSoupDemo.Answer, transcript)
		} else {
			audio := make([]byte, 0)
			err = tts.Stream(ctx, test.utterance, func(chunk []byte) error {
				audio = append(audio, chunk...)
				return nil
			})
			if err == nil {
				var result game.TurnResult
				result, err = pipeline.Turn(ctx, audio, game.TurtleSoupDemo.Surface, game.TurtleSoupDemo.Answer)
				got, transcript = result.Verdict, result.Transcript
			}
		}
		cancel()
		ok := err == nil && got == test.want
		status := "PASS"
		if !ok {
			status = "FAIL"
		} else {
			passed++
		}
		fmt.Printf("%s\t%s\twant=%s got=%s latency=%s transcript=%q", status, test.name, test.want, got, time.Since(caseStarted).Round(100*time.Millisecond), transcript)
		if err != nil {
			fmt.Printf(" error=%q", err)
		}
		fmt.Println()
	}
	fmt.Printf("\n%d/%d passed in %s\n", passed, len(cases), time.Since(started).Round(100*time.Millisecond))
	if passed != len(cases) {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
