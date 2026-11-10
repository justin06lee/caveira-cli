package llm

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Reasons Busy gives for a request worth waiting out and sending again.
const (
	RateLimited = "Rate limited"
	Overloaded  = "Model overloaded"
	ConnLost    = "Connection lost"
	// The caveira account's own: its other requests still running, or its
	// email not verified yet. Each clears on its own, the second when the
	// person opens the link they were sent.
	AccountBusy = "Account busy"
	Unverified  = "Email not verified"
)

// ConnError is a connection that failed: before the server answered, or
// partway through its answer when Mid.
type ConnError struct {
	URL string
	Mid bool
	Err error
}

func (e *ConnError) Error() string {
	if e.Mid {
		return "the connection dropped mid-reply: " + e.Err.Error()
	}
	return "cannot reach " + e.URL + ": " + e.Err.Error()
}

func (e *ConnError) Unwrap() error { return e.Err }

// Busy says why err is worth waiting out and sending the same request
// again: RateLimited, Overloaded or ConnLost, or "" when it is not. A
// request the account cannot pay for is never busy, even on a 429: it
// would only be refused again.
func Busy(err error) string {
	if err == nil || OutOfCredits(err) || OutOfMemory(err) {
		return ""
	}
	var ce *ConnError
	if errors.As(err, &ce) {
		return ConnLost
	}
	var e *APIError
	if !errors.As(err, &e) {
		return ""
	}
	// The caveira site's own errors: failing to reach its models, the
	// account's other requests filling its places, and an email not yet
	// verified pass; the rest (sign-in, plan, usage, its provider refusing
	// it) would come back the same.
	if e.Type == "caveira" {
		switch e.Code {
		case "upstream_unreachable":
			return ConnLost
		case "too_many_running":
			return AccountBusy
		case "email_unverified":
			return Unverified
		}
		return ""
	}
	switch e.Status {
	case http.StatusTooManyRequests:
		return RateLimited
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return Overloaded
	case 0:
		// An error sent as an event in the middle of a stream has no
		// status; what it says is all there is to go on.
		m := strings.ToLower(e.Message + " " + e.Type + " " + e.Code)
		for _, w := range []string{"rate limit", "rate_limit", "too many requests", "429"} {
			if strings.Contains(m, w) {
				return RateLimited
			}
		}
		for _, w := range []string{"overloaded", "capacity", "unavailable", "503", "529", "try again later"} {
			if strings.Contains(m, w) {
				return Overloaded
			}
		}
	}
	return ""
}

// SiteSays is what the caveira site said about a request it turned away,
// "" when err is not its answer. Its words already say what to do, so a
// client shows them rather than a reason of its own.
func SiteSays(err error) string {
	var e *APIError
	if errors.As(err, &e) && e.Type == "caveira" {
		return e.Message
	}
	return ""
}

// OutOfCredits says err is the account or key having nothing left to
// spend: a 402, or a refusal that says so in words, as OpenAI's 429
// insufficient_quota does.
func OutOfCredits(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	if e.Status == http.StatusPaymentRequired {
		return true
	}
	// Not "billing" or a bare "quota exceeded": OpenAI's per-minute limits
	// link to the billing page, and Google's call themselves quotas.
	m := strings.ToLower(e.Message + " " + e.Type + " " + e.Code)
	for _, w := range []string{
		"insufficient_quota", "exceeded your current quota", "quota exhausted",
		"out of credits", "insufficient credit", "credit balance", "usage_limit",
	} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// retryAfter is how long the response asks to be left alone: Retry-After
// (seconds or a date) or retry-after-ms, and on a 429 the soonest the
// rate-limit headers say it resets; 0 when none of them do.
func retryAfter(res *http.Response, now time.Time) time.Duration {
	if res == nil {
		return 0
	}
	h := res.Header
	if v := h.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil {
			return seconds(secs)
		}
		if at, err := http.ParseTime(v); err == nil {
			return max(at.Sub(now), 0)
		}
	}
	if res.StatusCode != http.StatusTooManyRequests {
		return 0
	}
	// x-ratelimit-reset is a time or a wait depending on who sends it; the
	// -requests and -tokens ones are OpenAI's ("6m0s"). Whichever limit
	// was hit, waiting for the later of them clears both.
	var wait time.Duration
	for _, k := range []string{"x-ratelimit-reset", "x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"} {
		if d := resetIn(h.Get(k), now); d > wait {
			wait = d
		}
	}
	return wait
}

func resetIn(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil {
		return max(d, 0)
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		if at, err := time.Parse(time.RFC3339, v); err == nil {
			return max(at.Sub(now), 0)
		}
		return 0
	}
	switch {
	case n > 1e12: // a Unix time in milliseconds
		return max(time.UnixMilli(int64(n)).Sub(now), 0)
	case n > 1e9: // a Unix time in seconds
		return max(time.Unix(int64(n), 0).Sub(now), 0)
	}
	return seconds(n)
}

func seconds(n float64) time.Duration {
	if n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return time.Duration(n * float64(time.Second))
}
