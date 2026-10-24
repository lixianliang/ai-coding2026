package game

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const debugTranscriptPrefix = "say:"

type ASREventKind uint8

const (
	ASREventPartial ASREventKind = iota + 1
	ASREventFinal
)

type ASREvent struct {
	Kind ASREventKind
	Text string
}

type ASRSession interface {
	Push(context.Context, []byte) error
	Finalize(context.Context) error
	Events() <-chan ASREvent
	Close() error
}

type ASR interface {
	Open(context.Context, string) (ASRSession, error)
}

// DebugASR accepts UTF-8 "say:<text>" frames from a passthrough codec. It is
// for local protocol tests and demos; the production provider is injected.
type DebugASR struct{}

func (DebugASR) Open(context.Context, string) (ASRSession, error) {
	return &debugASRSession{events: make(chan ASREvent, 8)}, nil
}

type debugASRSession struct {
	events    chan ASREvent
	closeOnce sync.Once
	audio     bytes.Buffer
}

func (s *debugASRSession) Push(_ context.Context, pcm []byte) error {
	if !utf8.Valid(pcm) || !strings.HasPrefix(string(pcm), debugTranscriptPrefix) {
		return nil
	}
	_, _ = s.audio.Write(pcm)
	return nil
}

func (s *debugASRSession) Finalize(context.Context) error {
	text := strings.TrimSpace(strings.TrimPrefix(s.audio.String(), debugTranscriptPrefix))
	if text == "" {
		return errors.New("debug transcript is empty")
	}
	select {
	case s.events <- ASREvent{Kind: ASREventFinal, Text: text}:
	default:
		return errors.New("debug ASR event queue is full")
	}
	return nil
}

func (s *debugASRSession) Events() <-chan ASREvent {
	return s.events
}

func (s *debugASRSession) Close() error {
	s.closeOnce.Do(func() { close(s.events) })
	return nil
}

type Verdict string

const (
	VerdictYes        Verdict = "yes"
	VerdictNo         Verdict = "no"
	VerdictClose      Verdict = "close"
	VerdictIrrelevant Verdict = "irrelevant"
	VerdictCorrect    Verdict = "correct"
)

// JudgeResult 中 Player 是玩家发言开头自报的名字，未自报时为空。
type JudgeResult struct {
	Verdict Verdict
	Player  string
}

type Judge interface {
	Judge(context.Context, string, string, string) (JudgeResult, error)
}

// RuleJudge is the no-key fallback. It only recognizes an explicit answer and
// returns a conservative response for everything else.
type RuleJudge struct{}

func (RuleJudge) Judge(_ context.Context, _ string, answer string, question string) (JudgeResult, error) {
	normalizedQuestion := normalizeText(question)
	normalizedAnswer := normalizeText(answer)
	if normalizedQuestion == "" {
		return JudgeResult{Verdict: VerdictIrrelevant}, nil
	}
	if strings.Contains(normalizedQuestion, normalizedAnswer) ||
		(utf8.RuneCountInString(normalizedQuestion) >= 6 && strings.Contains(normalizedAnswer, normalizedQuestion)) {
		return JudgeResult{Verdict: VerdictCorrect}, nil
	}
	if strings.Contains(normalizedQuestion, "接近") || strings.Contains(normalizedQuestion, "是不是") {
		return JudgeResult{Verdict: VerdictClose}, nil
	}
	return JudgeResult{Verdict: VerdictNo}, nil
}

func normalizeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

type TTS interface {
	Synthesize(context.Context, string) ([]byte, error)
}

// SilenceTTS keeps the complete Opus playback path testable without a cloud key.
type SilenceTTS struct{}

const silenceDurationMS = 600

func (SilenceTTS) Synthesize(context.Context, string) ([]byte, error) {
	samples := AudioSampleRate * silenceDurationMS / 1000
	return make([]byte, samples*pcmSampleBytes), nil
}

func VerdictSpeech(verdict Verdict) string {
	switch verdict {
	case VerdictYes:
		return "是。"
	case VerdictNo:
		return "不是。"
	case VerdictClose:
		return "很接近了。"
	case VerdictIrrelevant:
		return "这个问题和汤底关系不大，请换个问法。"
	default:
		return "请再说一次。"
	}
}
