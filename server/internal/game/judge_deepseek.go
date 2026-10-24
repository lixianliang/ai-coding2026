package game

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	deepSeekChatPath     = "/chat/completions"
	deepSeekResponseSize = 1 << 20
	deepSeekHTTPTimeout  = 30 * time.Second
)

type DeepSeekJudge struct {
	apiKey      string
	endpoint    string
	model       string
	maxTokens   int
	temperature float64
	client      *http.Client
}

func NewDeepSeekJudge(apiKey, baseURL, model string, maxTokens int, temperature float64) (*DeepSeekJudge, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("deepseek api key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("deepseek base URL is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("deepseek model is required")
	}
	if maxTokens <= 0 {
		return nil, errors.New("deepseek max tokens must be positive")
	}
	return &DeepSeekJudge{
		apiKey:      strings.TrimSpace(apiKey),
		endpoint:    strings.TrimRight(strings.TrimSpace(baseURL), "/") + deepSeekChatPath,
		model:       strings.TrimSpace(model),
		maxTokens:   maxTokens,
		temperature: temperature,
		client:      &http.Client{Timeout: deepSeekHTTPTimeout},
	}, nil
}

func (j *DeepSeekJudge) Judge(ctx context.Context, surface, answer, question string) (JudgeResult, error) {
	payload := deepSeekRequest{
		Model: j.model,
		Messages: []deepSeekMessage{
			{Role: "system", Content: judgeSystemPrompt},
			{Role: "user", Content: fmt.Sprintf("汤面：%s\n汤底：%s\n玩家发言：%s", surface, answer, question)},
		},
		MaxTokens:      j.maxTokens,
		Temperature:    j.temperature,
		ResponseFormat: deepSeekResponseFormat{Type: "json_object"},
		Thinking:       deepSeekThinking{Type: "disabled"},
		Stream:         false,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return JudgeResult{}, fmt.Errorf("marshal deepseek request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, j.endpoint, bytes.NewReader(raw))
	if err != nil {
		return JudgeResult{}, fmt.Errorf("create deepseek request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+j.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := j.client.Do(request)
	if err != nil {
		return JudgeResult{}, fmt.Errorf("call deepseek: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, deepSeekResponseSize))
	if err != nil {
		return JudgeResult{}, fmt.Errorf("read deepseek response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return JudgeResult{}, fmt.Errorf("deepseek returned status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result deepSeekResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return JudgeResult{}, fmt.Errorf("decode deepseek response: %w", err)
	}
	if len(result.Choices) == 0 {
		return JudgeResult{}, errors.New("deepseek returned no choices")
	}
	content := strings.TrimSpace(result.Choices[0].Message.Content)
	if content == "" {
		return JudgeResult{}, errors.New("deepseek returned empty content")
	}
	return parseJudgeResult(content)
}

const judgeSystemPrompt = `你是海龟汤游戏裁判。根据完整汤底判断玩家最新发言，只输出 JSON：
{"verdict":"yes|no|close|irrelevant|correct","player":"玩家名字"}
player：玩家发言开头自报的名字，例如“我是小明，他认识厨师吗”中的“小明”；没有自报名字时输出空字符串。判定 verdict 时忽略自报名字的部分。
verdict 规则：
- correct：玩家已经完整说出核心真相，可以直接结束本局。
- yes：玩家提出的是/否问题，按汤底判断为是。
- no：玩家提出的是/否问题，按汤底判断为否。
- close：方向接近核心真相，但仍缺少关键事实。
- irrelevant：与汤底无关。
不要解释，不要输出其他字段。`

type deepSeekRequest struct {
	Model          string                 `json:"model"`
	Messages       []deepSeekMessage      `json:"messages"`
	MaxTokens      int                    `json:"max_tokens"`
	Temperature    float64                `json:"temperature"`
	ResponseFormat deepSeekResponseFormat `json:"response_format"`
	Thinking       deepSeekThinking       `json:"thinking"`
	Stream         bool                   `json:"stream"`
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekResponseFormat struct {
	Type string `json:"type"`
}

type deepSeekThinking struct {
	Type string `json:"type"`
}

type deepSeekResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func parseJudgeResult(content string) (JudgeResult, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return JudgeResult{}, fmt.Errorf("deepseek verdict is not JSON: %q", content)
	}
	var result struct {
		Verdict Verdict `json:"verdict"`
		Player  string  `json:"player"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &result); err != nil {
		return JudgeResult{}, fmt.Errorf("decode deepseek verdict: %w", err)
	}
	switch result.Verdict {
	case VerdictYes, VerdictNo, VerdictClose, VerdictIrrelevant, VerdictCorrect:
		return JudgeResult{Verdict: result.Verdict, Player: strings.TrimSpace(result.Player)}, nil
	default:
		return JudgeResult{}, fmt.Errorf("unsupported deepseek verdict %q", result.Verdict)
	}
}
