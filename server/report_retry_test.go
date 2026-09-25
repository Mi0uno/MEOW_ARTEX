package server

import "testing"

// TestIsTransientLLMError 瞬态错误分类:限流/超时/网络/5xx 应重试,鉴权/配置类必须放行快速失败。
func TestIsTransientLLMError(t *testing.T) {
	transient := []string{
		`anthropic: status 429: {"error":{"type":"rate_limit_error","message":"[1302][Rate limit reached for requests]"}}`,
		"post https://x/v1/messages: status 502",
		"status 503 service unavailable",
		"status 504 gateway timeout",
		"status 529 overloaded",
		"context deadline exceeded",
		"Client.Timeout exceeded while awaiting headers",
		"read tcp 127.0.0.1:1->127.0.0.1:2: connection reset",
		"dial tcp: connection refused",
		"unexpected EOF",
		"write: broken pipe",
		"dial tcp: lookup api.example.com: no such host",
		"LLM 限流,请稍后重试",
	}
	for _, s := range transient {
		if !isTransientLLMError(errOf(s)) {
			t.Errorf("应判定为瞬态: %q", s)
		}
	}
	permanent := []string{
		`anthropic: status 403: {"error":{"type":"forbidden","message":"Request not allowed"}}`,
		"status 401 unauthorized: invalid api key",
		"status 400: invalid request: max_tokens too large",
		"未配置可用的 LLM 模型",
		"",
	}
	for _, s := range permanent {
		if isTransientLLMError(errOf(s)) {
			t.Errorf("不应判定为瞬态: %q", s)
		}
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }

func errOf(s string) error {
	if s == "" {
		return nil
	}
	return strErr(s)
}
