// Package llm 是外部模型调用的唯一出口（F16 / I-093）。
//
// 只在人显式打开「视角」且环境变量齐备时才会被调用（AC-02 / AC-18）：
// 密钥只从环境变量读，不落配置、不进日志；请求体与响应体都不写日志。
//
// 接口按 OpenAI 兼容的 /chat/completions 写，默认指向 DeepSeek；换端点只改环境变量。

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// defaultBaseURL / defaultModel 是没设环境变量时的默认值。
	defaultBaseURL = "https://api.deepseek.com"
	defaultModel   = "deepseek-chat"

	// PromptVersion 是提示词的版本；改提示词就加一，缓存键跟着变（旧结论保留）。
	PromptVersion = "v1"

	// requestTimeout 是单次调用的上限；maxResponse 是响应体上限。
	requestTimeout = 90 * time.Second
	maxResponse    = 256 * 1024

	// envBaseURL / envModel / envAPIKey 是三个环境变量（AC-18：密钥只走环境变量）。
	envBaseURL = "AGENTMEMORY_LLM_BASE_URL"
	envModel   = "AGENTMEMORY_LLM_MODEL"
	envAPIKey  = "AGENTMEMORY_LLM_API_KEY"
)

// Client 是一个 OpenAI 兼容的对话补全客户端。
type Client struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

// FromEnv 按环境变量造一个客户端；没设的用默认值。
func FromEnv() Client {
	return Client{
		BaseURL: envOr(envBaseURL, defaultBaseURL),
		Model:   envOr(envModel, defaultModel),
		APIKey:  strings.TrimSpace(os.Getenv(envAPIKey)),
	}
}

// Configured 报告这个客户端能不能用；不能用时给出缺哪一项（可读中文）。
func (c Client) Configured() error {
	if strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("LLM 视角没配置：缺环境变量 %s（配好后重启 AgentMemory）", envAPIKey)
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("LLM 视角没配置：%s 是空的", envBaseURL)
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("LLM 视角没配置：%s 是空的", envModel)
	}
	return nil
}

// Complete 发一次对话补全，返回助手文本。
func (c Client) Complete(ctx context.Context, system, user string) (string, error) {
	if err := c.Configured(); err != nil {
		return "", err
	}
	payload := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.2,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("调模型失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return "", fmt.Errorf("读模型响应失败：%w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// 只回状态码与截断后的原文，避免把整段响应（可能含密钥回显）写进错误链。
		brief := strings.TrimSpace(string(body))
		if r := []rune(brief); len(r) > 200 {
			brief = string(r[:200]) + "…"
		}
		return "", fmt.Errorf("模型返回 %d：%s", resp.StatusCode, brief)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("模型响应看不懂：%w", err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("模型没给内容")
	}
	return out.Choices[0].Message.Content, nil
}

// ModelTag / PromptTag 是缓存键要用的两项（MCP 侧按它们拼缓存键）。
func (c Client) ModelTag() string  { return c.Model }
func (c Client) PromptTag() string { return PromptVersion }

// Interpret 按一个视角解读一段会话文本，返回模型给的 JSON 文本。
func (c Client) Interpret(ctx context.Context, perspective, transcript string) (string, error) {
	system, ok := prompts[perspective]
	if !ok {
		return "", fmt.Errorf("不认识的视角 %q", perspective)
	}
	return c.Complete(ctx, system, transcript)
}

// Perspective 是三个可选视角。
var PerspectiveNames = []string{"topic", "decision", "todo"}

var prompts = map[string]string{
	"topic": "你在帮人回顾本机 AI 编码工具的会话。读下面的会话，提炼 1-5 个在讨论的话题。" +
		"只输出 JSON：{\"items\":[{\"topic\":\"不超过 20 字\",\"summary\":\"一两句话说清\"}]}；不要输出任何别的文字。",
	"decision": "你在帮人回顾本机 AI 编码工具的会话。找出会话里已经做出的决定（定了什么、为什么）。" +
		"只输出 JSON：{\"items\":[{\"decision\":\"不超过 30 字\",\"reason\":\"简短理由或原话要点\"}]}；没有决定就输出 {\"items\":[]}；不要输出任何别的文字。",
	"todo": "你在帮人回顾本机 AI 编码工具的会话。找出还没做完、需要继续做的事。" +
		"只输出 JSON：{\"items\":[{\"todo\":\"不超过 30 字\",\"blocked_by\":\"卡在哪，没写就空串\"}]}；没有待办就输出 {\"items\":[]}；不要输出任何别的文字。",
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
