package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justin06lee/caveira/core/llm"
)

// fast waits milliseconds where a real turn waits seconds.
var fast = Patience{First: 20 * time.Millisecond, Most: 2 * time.Second, Budget: 10 * time.Second}

func refuse(status int, headers map[string]string, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

const rateLimited = `{"error":{"message":"Rate limit reached for requests","type":"rate_limit_error","code":"rate_limit_exceeded"}}`

func retries(events []Event) []RetryEvent {
	var out []RetryEvent
	for _, ev := range events {
		if r, ok := ev.(RetryEvent); ok {
			out = append(out, r)
		}
	}
	return out
}

func failure(events []Event) error {
	for _, ev := range events {
		if e, ok := ev.(ErrorEvent); ok {
			return e.Err
		}
	}
	return nil
}

func assistantTexts(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestRateLimitHonorsRetryAfter(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		refuse(429, map[string]string{"Retry-After": "1"}, rateLimited),
		writeSSE(sse("Hello.", nil)),
	}}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()
	ag := newAgent(t, srv.URL, t.TempDir(), false)
	ag.Patience = fast

	start := time.Now()
	events := collect(ag, context.Background(), "hi", nil)
	if err := failure(events); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	rs := retries(events)
	if len(rs) != 1 || rs[0].Reason != llm.RateLimited || rs[0].Wait != time.Second {
		t.Fatalf("retries %+v", rs)
	}
	if took := time.Since(start); took < time.Second {
		t.Fatalf("retried after %v, before the 1s Retry-After", took)
	}
	if len(s.bodies) != 2 || s.bodies[0] != s.bodies[1] {
		t.Fatalf("the retry was not the same request: %d bodies", len(s.bodies))
	}
	if got := assistantTexts(ag.Messages); len(got) != 1 || got[0] != "Hello." {
		t.Fatalf("history %q", got)
	}
}

func TestRateLimitBacksOffWithoutHeader(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		refuse(429, nil, rateLimited),
		refuse(503, nil, `{"error":{"message":"The model is overloaded"}}`),
		writeSSE(sse("Hello.", nil)),
	}}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()
	ag := newAgent(t, srv.URL, t.TempDir(), false)
	ag.Patience = fast

	events := collect(ag, context.Background(), "hi", nil)
	if err := failure(events); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	rs := retries(events)
	if len(rs) != 2 || rs[0].Reason != llm.RateLimited || rs[1].Reason != llm.Overloaded {
		t.Fatalf("retries %+v", rs)
	}
	// 20ms then 40ms, give or take a fifth.
	if rs[0].Wait < 16*time.Millisecond || rs[0].Wait > 24*time.Millisecond || rs[1].Wait < 32*time.Millisecond || rs[1].Wait > 48*time.Millisecond {
		t.Fatalf("waits %v, %v", rs[0].Wait, rs[1].Wait)
	}
	if got := assistantTexts(ag.Messages); len(got) != 1 || got[0] != "Hello." {
		t.Fatalf("history %q", got)
	}
}

// A reply that breaks off partway, with an error event or a dropped
// connection, is asked for again from the start: the clients are told to
// drop what streamed, the history has the reply once, and a tool call
// that was half sent never runs.
func TestMidStreamFailureRestartsTheRequest(t *testing.T) {
	partial := `data: {"choices":[{"index":0,"delta":{"content":"Partial ans"}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":"}}]}}]}` + "\n\n"
	for _, tc := range []struct {
		name   string
		reason string
		end    func(w http.ResponseWriter)
	}{
		{"error event", llm.RateLimited, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `data: {"error":{"message":"Rate limit reached for model","type":"rate_limit_error","code":429}}`+"\n\n")
		}},
		{"dropped connection", llm.ConnLost, func(w http.ResponseWriter) {
			panic(http.ErrAbortHandler)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &scripted{responses: []func(http.ResponseWriter){
				func(w http.ResponseWriter) {
					_, _ = io.WriteString(w, partial)
					w.(http.Flusher).Flush()
					tc.end(w)
				},
				writeSSE(sse("Full answer.", nil)),
			}}
			srv := httptest.NewServer(http.HandlerFunc(s.handler))
			defer srv.Close()
			ag := newAgent(t, srv.URL, t.TempDir(), false)
			ag.Patience = fast

			events := collect(ag, context.Background(), "hi", nil)
			if err := failure(events); err != nil {
				t.Fatalf("turn failed: %v", err)
			}
			var order []string
			for _, ev := range events {
				switch ev := ev.(type) {
				case TextEvent:
					order = append(order, "text:"+ev.Delta)
				case RetryEvent:
					order = append(order, "retry:"+ev.Reason)
				case AssistantDoneEvent:
					order = append(order, "done:"+ev.Message.Content)
				case ToolStartEvent:
					t.Fatalf("a half-sent tool call ran: %+v", ev)
				}
			}
			want := []string{"text:Partial ans", "retry:" + tc.reason, "text:Full answer.", "done:Full answer."}
			if strings.Join(order, "|") != strings.Join(want, "|") {
				t.Fatalf("events %q, want %q", order, want)
			}
			if got := assistantTexts(ag.Messages); len(got) != 1 || got[0] != "Full answer." {
				t.Fatalf("history %q", got)
			}
		})
	}
}

// Out of credits is not waited out: the turn ends at once and says so.
func TestOutOfCreditsIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		resp       func(http.ResponseWriter)
	}{
		{"402", "out of credits", refuse(402, nil, `{"error":{"message":"Insufficient credits","type":"payment_required"}}`)},
		{"429 insufficient_quota", "out of credits", refuse(429, map[string]string{"Retry-After": "1"},
			`{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`)},
		{"caveira usage", "used this month", refuse(402, nil,
			`{"error":{"message":"You've used this month's usage on Free.","type":"caveira","code":"usage_limit"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &scripted{responses: []func(http.ResponseWriter){tc.resp, writeSSE(sse("Hello.", nil))}}
			srv := httptest.NewServer(http.HandlerFunc(s.handler))
			defer srv.Close()
			ag := newAgent(t, srv.URL, t.TempDir(), false)
			ag.Patience = fast

			events := collect(ag, context.Background(), "hi", nil)
			err := failure(events)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want it to say %q", err, tc.want)
			}
			if rs := retries(events); len(rs) != 0 || len(s.bodies) != 1 {
				t.Fatalf("retried: %d retries, %d requests", len(rs), len(s.bodies))
			}
			if _, ok := events[len(events)-1].(DoneEvent); !ok {
				t.Fatalf("last event %#v", events[len(events)-1])
			}
		})
	}
}

func TestStopDuringTheWait(t *testing.T) {
	s := &scripted{responses: []func(http.ResponseWriter){
		refuse(429, map[string]string{"Retry-After": "30"}, rateLimited),
	}}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	defer srv.Close()
	ag := newAgent(t, srv.URL, t.TempDir(), false)
	ag.Patience = Patience{First: time.Second, Most: time.Minute, Budget: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	var events []Event
	ag.Run(ctx, "hi", func(ev Event) {
		events = append(events, ev)
		if r, ok := ev.(RetryEvent); ok {
			if r.Wait != 30*time.Second {
				t.Errorf("wait %v, want the 30s Retry-After", r.Wait)
			}
			cancel()
		}
	})
	if time.Since(start) > 5*time.Second {
		t.Fatal("Stop did not end the wait")
	}
	done, ok := events[len(events)-1].(DoneEvent)
	if !ok || !done.Interrupted {
		t.Fatalf("last event %#v", events[len(events)-1])
	}
	if len(ag.Messages) != 1 {
		t.Fatalf("history %+v", ag.Messages)
	}
}

// A turn that waits out its whole patience gives up with a GaveUpError,
// and Continue takes it up again: the same exchange, no second prompt.
func TestGiveUpThenContinue(t *testing.T) {
	var open atomic.Bool
	var requests atomic.Int32
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !open.Load() {
			refuse(429, nil, rateLimited)(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse("Back.", nil))
	}))
	defer srv.Close()
	ag := newAgent(t, srv.URL, t.TempDir(), false)
	ag.Patience = Patience{First: 20 * time.Millisecond, Most: time.Second, Budget: 100 * time.Millisecond}
	ag.NewSession()

	events := collect(ag, context.Background(), "hi", nil)
	var gave *GaveUpError
	if err := failure(events); !errors.As(err, &gave) || gave.Reason != llm.RateLimited {
		t.Fatalf("error %v, want a GaveUpError", err)
	}
	if int(requests.Load()) != gave.Tries || gave.Tries < 2 || len(retries(events)) != gave.Tries-1 {
		t.Fatalf("%d requests, %d tries, %d retries", requests.Load(), gave.Tries, len(retries(events)))
	}
	if !strings.HasPrefix(gave.Error(), "still rate limited after ") {
		t.Fatalf("error text %q", gave.Error())
	}

	open.Store(true)
	var again []Event
	ag.Continue(context.Background(), func(ev Event) { again = append(again, ev) })
	if err := failure(again); err != nil {
		t.Fatalf("continue failed: %v", err)
	}
	if len(ag.Messages) != 2 || ag.Messages[0].Content != "hi" || ag.Messages[1].Content != "Back." {
		t.Fatalf("history %+v", ag.Messages)
	}
	xs := loadIndex(ag.Session.Dir())
	if ag.Session.Exchanges != 1 || len(xs) != 1 || xs[0].Prompt != "hi" || xs[0].Reply != "Back." {
		t.Fatalf("exchanges %d, index %+v", ag.Session.Exchanges, xs)
	}
}

// The caveira site's own waits, its other requests running and its email
// not verified yet, are waited out like a busy model, and the line the
// clients show is the site's own words.
func TestAccountWaitsSayTheSitesWords(t *testing.T) {
	for _, tc := range []struct {
		name, reason, says string
		resp               func(http.ResponseWriter)
	}{
		{"three running", llm.AccountBusy, "3 requests already running on this account, wait a moment", refuse(429, map[string]string{"Retry-After": "0.05"},
			`{"error":{"message":"3 requests already running on this account, wait a moment","type":"caveira","code":"too_many_running"}}`)},
		{"unverified", llm.Unverified, "Verify your email first: check new@caveira.app for the link", refuse(403, map[string]string{"Retry-After": "0.05"},
			`{"error":{"message":"Verify your email first: check new@caveira.app for the link","type":"caveira","code":"email_unverified"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &scripted{responses: []func(http.ResponseWriter){tc.resp, writeSSE(sse("Hello.", nil))}}
			srv := httptest.NewServer(http.HandlerFunc(s.handler))
			defer srv.Close()
			ag := newAgent(t, srv.URL, t.TempDir(), false)
			ag.Patience = fast

			events := collect(ag, context.Background(), "hi", nil)
			if err := failure(events); err != nil {
				t.Fatalf("turn failed: %v", err)
			}
			rs := retries(events)
			if len(rs) != 1 || rs[0].Reason != tc.reason || len(s.bodies) != 2 {
				t.Fatalf("%d retries %+v, %d requests", len(rs), rs, len(s.bodies))
			}
			if got, want := rs[0].Text(5*time.Second), tc.says+" · retrying in 5s"; got != want {
				t.Errorf("Text = %q, want %q", got, want)
			}
			if got := assistantTexts(ag.Messages); len(got) != 1 || got[0] != "Hello." {
				t.Fatalf("history %q", got)
			}
		})
	}
}

func TestRetryText(t *testing.T) {
	e := RetryEvent{Reason: llm.RateLimited}
	for left, want := range map[time.Duration]string{
		23 * time.Second:                      "Rate limited · retrying in 23s",
		22*time.Second + 100*time.Millisecond: "Rate limited · retrying in 23s",
		2 * time.Minute:                       "Rate limited · retrying in 2m",
		90 * time.Second:                      "Rate limited · retrying in 1m 30s",
		0:                                     "Rate limited · retrying now…",
	} {
		if got := e.Text(left); got != want {
			t.Errorf("Text(%v) = %q, want %q", left, got, want)
		}
	}
}
