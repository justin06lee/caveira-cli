package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/justin06lee/caveira/core/llm"
)

// Patience is how long a turn waits out a busy model (llm.Busy): First is
// the first wait when the server does not say how long, doubling each try
// up to Most, until Budget has gone by since the first refusal.
type Patience struct {
	First, Most, Budget time.Duration
}

// DefaultPatience waits 5s, 10s, 20s… up to 2 minutes at a time, for up to
// 15 minutes.
var DefaultPatience = Patience{First: 5 * time.Second, Most: 2 * time.Minute, Budget: 15 * time.Minute}

// wait is how long to wait before try+1: what the server asked for, or
// the backoff with some jitter, and never more than Most.
func (p Patience) wait(try int, asked time.Duration) time.Duration {
	if asked > 0 {
		return min(asked, p.Most)
	}
	d := p.First << min(try-1, 20)
	d = time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
	return min(d, p.Most)
}

// GaveUpError ends a turn that waited out a busy model for all of its
// Patience. Continue takes the turn up again.
type GaveUpError struct {
	Reason string // llm.RateLimited, llm.Overloaded or llm.ConnLost
	For    time.Duration
	Tries  int
	Err    error // the last refusal
}

func (e *GaveUpError) Error() string {
	return fmt.Sprintf("still %s after %s and %d tries (%v)", strings.ToLower(e.Reason), waitText(e.For), e.Tries, e.Err)
}

func (e *GaveUpError) Unwrap() error { return e.Err }

// stream sends one request for the turn and streams its reply as events.
// A busy model is waited out and the same request sent again, the
// RetryEvent telling the clients to drop whatever of the reply had
// streamed; nothing from a failed try reaches the history.
func (a *Agent) stream(ctx context.Context, req llm.Request, emit func(Event)) (*llm.Completion, error) {
	p := a.Patience
	if p.Budget <= 0 {
		p = DefaultPatience
	}
	var first time.Time
	for try := 1; ; try++ {
		comp, err := a.Client.Stream(ctx, req, func(d llm.Delta) error {
			if d.Content != "" {
				emit(TextEvent{Delta: d.Content})
			}
			if d.Reasoning != "" {
				emit(ReasoningEvent{Delta: d.Reasoning})
			}
			return nil
		})
		if err == nil || ctx.Err() != nil {
			return comp, err
		}
		reason := llm.Busy(err)
		if reason == "" {
			return comp, err
		}
		if first.IsZero() {
			first = time.Now()
		}
		left := p.Budget - time.Since(first)
		if left <= 0 {
			return nil, &GaveUpError{Reason: reason, For: time.Since(first), Tries: try, Err: err}
		}
		var asked time.Duration
		var apiErr *llm.APIError
		if errors.As(err, &apiErr) {
			asked = apiErr.RetryAfter
		}
		wait := min(p.wait(try, asked), left)
		emit(RetryEvent{Reason: reason, Try: try, Wait: wait, Until: time.Now().Add(wait), Err: err})
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// Continue takes the last turn up again where it stopped, as when it gave
// up waiting out a busy model: the model is asked again with the history
// as it stands, and no new message. Its exchange record is the one the
// turn already had.
func (a *Agent) Continue(ctx context.Context, emit func(Event)) {
	a.backfill(a.Messages)
	a.turn, a.redo = a.lastTurn(), true
	defer a.save()
	defer a.endExchange()
	a.loop(ctx, emit)
}

// CanContinue says the last turn stopped before the model answered, so
// Continue has something to take up.
func (a *Agent) CanContinue() bool {
	if len(a.Messages) == 0 {
		return false
	}
	r := a.Messages[len(a.Messages)-1].Role
	return r == llm.RoleUser || r == llm.RoleTool
}

// lastTurn is the messages of the turn that ended last, its prompt first.
func (a *Agent) lastTurn() []llm.Message {
	for i := len(a.Messages) - 1; i >= 0; i-- {
		if a.Messages[i].Role == llm.RoleUser {
			return append([]llm.Message(nil), a.Messages[i:]...)
		}
	}
	return nil
}

// waitText is a wait as the clients say it: 23s, 2m, 1m 30s.
func waitText(d time.Duration) string {
	s := int((d + time.Second - 1) / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s%60 == 0:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}
