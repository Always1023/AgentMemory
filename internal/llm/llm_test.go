package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 默认值与环境变量：没设走 DeepSeek，设了按设的来；密钥只从环境变量读。
func TestFromEnvDefaultsAndOverrides(t *testing.T) {
	t.Setenv(envBaseURL, "")
	t.Setenv(envModel, "")
	t.Setenv(envAPIKey, "")
	got := FromEnv()
	if got.BaseURL != defaultBaseURL || got.Model != defaultModel {
		t.Fatalf("默认值不对: %+v", got)
	}
	if err := got.Configured(); err == nil || !strings.Contains(err.Error(), envAPIKey) {
		t.Fatalf("没有密钥时应当报缺 %s: %v", envAPIKey, err)
	}

	t.Setenv(envBaseURL, "https://example.test/v1")
	t.Setenv(envModel, "some-model")
	t.Setenv(envAPIKey, "test-key")
	got = FromEnv()
	if got.BaseURL != "https://example.test/v1" || got.Model != "some-model" || got.APIKey != "test-key" {
		t.Fatalf("环境变量没覆盖上: %+v", got)
	}
	if err := got.Configured(); err != nil {
		t.Fatalf("配齐后不该报错: %v", err)
	}
}

// 请求形态：Bearer 密钥、选中的模型，响应里取出 content。
func TestCompleteSendsRequestAndParsesReply(t *testing.T) {
	var auth, model, system, user string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体不是 JSON: %v", err)
		}
		model = body.Model
		if len(body.Messages) == 2 {
			system, user = body.Messages[0].Content, body.Messages[1].Content
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"items\":[]}"}}]}`))
	}))
	defer srv.Close()

	client := Client{BaseURL: srv.URL, Model: "m1", APIKey: "k1", HTTP: srv.Client()}
	out, err := client.Complete(context.Background(), "系统", "用户")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if out != `{"items":[]}` {
		t.Fatalf("返回内容不对: %q", out)
	}
	if auth != "Bearer k1" || model != "m1" || system != "系统" || user != "用户" {
		t.Fatalf("请求形态不对: auth=%q model=%q system=%q user=%q", auth, model, system, user)
	}
}

// 非 200 要报状态码，但错误里不能带密钥。
func TestCompleteReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	client := Client{BaseURL: srv.URL, Model: "m", APIKey: "secret-key", HTTP: srv.Client()}
	_, err := client.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("应当报 401: %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("错误里不该出现密钥: %v", err)
	}
}

// 三个视角都有提示词；不认识的视角直接拒绝，不发请求。
func TestInterpretRejectsUnknownPerspective(t *testing.T) {
	client := Client{BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "k"}
	if _, err := client.Interpret(context.Background(), "nope", "x"); err == nil {
		t.Fatal("不认识的视角应当报错")
	}
	for _, name := range PerspectiveNames {
		if _, ok := prompts[name]; !ok {
			t.Errorf("视角 %s 没有提示词", name)
		}
	}
}
