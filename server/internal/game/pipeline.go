package game

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	pipelineChunkBytes = AudioSampleRate * AudioFrameDuration / 1000 * pcmSampleBytes
)

var TurtleSoupDemo = defaultQuestions[0]

type Pipeline struct {
	asr   ASR
	judge Judge
	tts   TTS
}

type TurnResult struct {
	Transcript string
	Verdict    Verdict
	Player     string
	Speech     string
	PCM        []byte
}

func NewPipeline(asr ASR, judge Judge, tts TTS) (*Pipeline, error) {
	if asr == nil || judge == nil || tts == nil {
		return nil, errors.New("pipeline ASR, judge, and TTS are required")
	}
	return &Pipeline{asr: asr, judge: judge, tts: tts}, nil
}

func (p *Pipeline) Turn(ctx context.Context, pcm []byte, surface, answer string) (TurnResult, error) {
	transcript, err := p.Transcribe(ctx, pcm)
	if err != nil {
		return TurnResult{}, err
	}
	judged, err := p.judge.Judge(ctx, surface, answer, transcript)
	if err != nil {
		return TurnResult{Transcript: transcript}, err
	}
	verdict := judged.Verdict
	speech := VerdictSpeech(verdict)
	if verdict == VerdictCorrect {
		speech = "猜对了。汤底是：" + answer
	}
	audio, err := p.tts.Synthesize(ctx, speech)
	if err != nil {
		return TurnResult{Transcript: transcript, Verdict: verdict, Player: judged.Player, Speech: speech}, err
	}
	return TurnResult{Transcript: transcript, Verdict: verdict, Player: judged.Player, Speech: speech, PCM: audio}, nil
}

func (p *Pipeline) Transcribe(ctx context.Context, pcm []byte) (string, error) {
	if len(pcm) == 0 {
		return "", errors.New("PCM audio is required")
	}
	if len(pcm)%pcmSampleBytes != 0 {
		return "", errors.New("PCM byte length must be divisible by 2")
	}
	session, err := p.asr.Open(ctx, "playground")
	if err != nil {
		return "", fmt.Errorf("open ASR: %w", err)
	}
	defer session.Close()

	for offset := 0; offset < len(pcm); offset += pipelineChunkBytes {
		end := min(offset+pipelineChunkBytes, len(pcm))
		if err := session.Push(ctx, pcm[offset:end]); err != nil {
			return "", fmt.Errorf("push ASR audio: %w", err)
		}
	}
	if err := session.Finalize(ctx); err != nil {
		return "", fmt.Errorf("finalize ASR audio: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return "", errors.New("ASR closed before final transcript")
			}
			if event.Kind == ASREventFinal {
				text := strings.TrimSpace(event.Text)
				if text == "" {
					return "", errors.New("ASR returned an empty transcript")
				}
				return text, nil
			}
		}
	}
}
