package game

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	pipelineChunkBytes        = AudioSampleRate * AudioFrameDuration / 1000 * pcmSampleBytes
	pipelineTrailingSilenceMS = 2000
)

var TurtleSoupDemo = Question{
	Surface: "一个人走进餐厅，点了一碗海龟汤，喝了一口就哭了。为什么？",
	Answer:  "汤的味道让他想起已经去世的母亲，他因为思念母亲而哭。",
}

type Pipeline struct {
	asr   ASR
	judge Judge
	tts   TTS
}

type TurnResult struct {
	Transcript string
	Verdict    Verdict
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
	verdict, err := p.judge.Judge(ctx, surface, answer, transcript)
	if err != nil {
		return TurnResult{Transcript: transcript}, err
	}
	speech := VerdictSpeech(verdict)
	if verdict == VerdictCorrect {
		speech = "猜对了。汤底是：" + answer
	}
	audio, err := p.tts.Synthesize(ctx, speech)
	if err != nil {
		return TurnResult{Transcript: transcript, Verdict: verdict, Speech: speech}, err
	}
	return TurnResult{Transcript: transcript, Verdict: verdict, Speech: speech, PCM: audio}, nil
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

	audio := make([]byte, len(pcm)+AudioSampleRate*pcmSampleBytes*pipelineTrailingSilenceMS/1000)
	copy(audio, pcm)
	for offset := 0; offset < len(audio); offset += pipelineChunkBytes {
		end := min(offset+pipelineChunkBytes, len(audio))
		if err := session.Push(ctx, audio[offset:end]); err != nil {
			return "", fmt.Errorf("push ASR audio: %w", err)
		}
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
