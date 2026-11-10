package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sample = `data: {"id":"1","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"think "},"finish_reason":null}]}

data: {"id":"1","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}

data: {"id":"1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}

data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]},"finish_reason":null}]}

data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"x\"}"}}]},"finish_reason":null}]}

data: {"id":"1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}}}

data: [DONE]

`

func TestReadStream(t *testing.T) {
	var deltas []Delta
	comp, err := readStream(strings.NewReader(sample), func(d Delta) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if comp.Message.Content != "Hello" {
		t.Fatalf("content %q", comp.Message.Content)
	}
	if comp.Message.Reasoning != "think " {
		t.Fatalf("reasoning %q", comp.Message.Reasoning)
	}
	if len(comp.Message.ToolCalls) != 1 || comp.Message.ToolCalls[0].ID != "call_1" || comp.Message.ToolCalls[0].Function.Arguments != `{"path":"x"}` {
		t.Fatalf("tool calls %+v", comp.Message.ToolCalls)
	}
	if comp.FinishReason != "tool_calls" {
		t.Fatalf("finish %q", comp.FinishReason)
	}
	if comp.Usage.PromptTokens != 10 || comp.Usage.Cached() != 4 {
		t.Fatalf("usage %+v", comp.Usage)
	}
	if len(deltas) < 5 {
		t.Fatalf("expected deltas, got %d", len(deltas))
	}
}

func TestStreamErrorAndRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth header")
		}
		if calls == 1 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"internal error","type":"server_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	comp, err := c.Stream(context.Background(), Request{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || comp.Message.Content != "Hello" {
		t.Fatalf("calls=%d content=%q", calls, comp.Message.Content)
	}
}

// A rate limit comes back to the caller at once, with how long the server
// asked to wait: the agent waits it out where the person can see it.
func TestRateLimitIsTheCallersToWaitOut(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limited"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k").Stream(context.Background(), Request{Model: "m"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || calls != 1 || Busy(err) != RateLimited || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("calls=%d err=%v busy=%q", calls, err, Busy(err))
	}
}

func TestBusy(t *testing.T) {
	for err, want := range map[error]string{
		&APIError{Status: 429, Message: "Rate limit reached"}:                                          RateLimited,
		&APIError{Status: 503, Message: "busy"}:                                                        Overloaded,
		&APIError{Status: 529, Message: "Overloaded"}:                                                  Overloaded,
		&APIError{Message: "Rate limit reached for model", Type: "rate_limit_error"}:                   RateLimited,
		&APIError{Message: "The server is overloaded, try again later"}:                                Overloaded,
		&ConnError{Mid: true, Err: io.ErrUnexpectedEOF}:                                                ConnLost,
		&APIError{Status: 502, Type: "caveira", Code: "upstream_unreachable"}:                          ConnLost,
		&APIError{Status: 502, Type: "caveira", Code: "upstream_refused"}:                              "",
		&APIError{Status: 429, Type: "caveira", Code: "too_many_running"}:                              AccountBusy,
		&APIError{Status: 403, Type: "caveira", Code: "email_unverified"}:                              Unverified,
		&APIError{Status: 403, Type: "caveira", Code: "plan_required"}:                                 "",
		&APIError{Status: 402, Message: "Insufficient credits"}:                                        "",
		&APIError{Status: 429, Message: "You exceeded your current quota", Code: "insufficient_quota"}: "",
		&APIError{Status: 500, Message: "internal error"}:                                              "",
		&APIError{Status: 400, Message: "bad request"}:                                                 "",
		&APIError{Message: "something else went wrong"}:                                                "",
		errors.New("no"): "",
	} {
		if got := Busy(err); got != want {
			t.Errorf("Busy(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestRetryAfterHeaders(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	res := func(status int, kv ...string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}}
		for i := 0; i < len(kv); i += 2 {
			r.Header.Set(kv[i], kv[i+1])
		}
		return r
	}
	for i, tc := range []struct {
		res  *http.Response
		want time.Duration
	}{
		{res(429, "Retry-After", "23"), 23 * time.Second},
		{res(503, "Retry-After", "1.5"), 1500 * time.Millisecond},
		{res(429, "Retry-After", now.Add(40*time.Second).Format(http.TimeFormat)), 40 * time.Second},
		{res(429, "retry-after-ms", "250"), 250 * time.Millisecond},
		{res(429, "x-ratelimit-reset-requests", "1s", "x-ratelimit-reset-tokens", "6m0s"), 6 * time.Minute},
		{res(429, "x-ratelimit-reset", "12"), 12 * time.Second},
		{res(429, "x-ratelimit-reset", fmt.Sprint(now.Add(30*time.Second).Unix())), 30 * time.Second},
		{res(429, "x-ratelimit-reset", fmt.Sprint(now.Add(2*time.Second).UnixMilli())), 2 * time.Second},
		{res(200, "x-ratelimit-reset", "12"), 0},
		{res(429), 0},
	} {
		if got := retryAfter(tc.res, now); got != tc.want {
			t.Errorf("%d: retryAfter = %v, want %v (%v)", i, got, tc.want, tc.res.Header)
		}
	}
}

func TestStreamFatalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "bad").Stream(context.Background(), Request{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") || !strings.Contains(err.Error(), "check your API key") {
		t.Fatalf("err = %v", err)
	}
}

func TestOutOfMemory(t *testing.T) {
	for _, err := range []error{
		&APIError{Status: 500, Message: "llama-server process has terminated: exit status 1: cudaMalloc failed: out of memory"},
		errors.New("model requires more system memory (5.1 GiB) than is available (3.2 GiB)"),
		errors.New("ggml_vulkan: vk::Device::allocateMemory: ErrorOutOfDeviceMemory"),
	} {
		if !OutOfMemory(err) {
			t.Errorf("not seen as out of memory: %v", err)
		}
	}
	for _, err := range []error{nil, errors.New("connection refused"), errors.New("model 'x' not found")} {
		if OutOfMemory(err) {
			t.Errorf("seen as out of memory: %v", err)
		}
	}
}

// An out-of-memory 500 is not sent again: the model will not fit the
// second time either.
func TestOutOfMemoryIsNotRetried(t *testing.T) {
	if (&APIError{Status: 500, Message: "cudaMalloc failed: out of memory"}).Retryable() {
		t.Fatal("out of memory retried")
	}
	if !(&APIError{Status: 500, Message: "internal error"}).Retryable() {
		t.Fatal("500 not retried")
	}
}
