package engine

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Claude reports two shared windows on every response, as
// anthropic-ratelimit-unified-5h-* and -7d-* headers with utilization as a
// fraction (0 to 1), a reset as a unix time and a status. The OAuth usage
// endpoint reports the same two windows as five_hour and seven_day, but with
// utilization in percent (0 to 100) and an RFC 3339 reset that is null until a
// window starts. Per-model and overage windows are not routed on.

const (
	claudeShortMinutes = 300
	claudeLongMinutes  = 7 * 24 * 60
)

// ParseClaudeQuotaHeaders extracts the 5-hour and 7-day windows from Anthropic
// response headers. Header names are matched case-insensitively.
func ParseClaudeQuotaHeaders(headers map[string][]string, observedAt time.Time) (Quota, bool) {
	if len(headers) == 0 {
		return Quota{}, false
	}
	const prefix = "anthropic-ratelimit-unified-"
	lower := make(map[string]string, 16)
	for k, v := range headers {
		if len(v) == 0 {
			continue
		}
		lk := strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(lk, prefix) {
			lower[strings.TrimPrefix(lk, prefix)] = strings.TrimSpace(v[len(v)-1])
		}
	}
	if len(lower) == 0 {
		return Quota{}, false
	}
	q := Quota{ObservedAt: observedAt, Source: "response"}
	read := func(name string, minutes int) *Window {
		status := strings.ToLower(lower[name+"-status"])
		used, okUsed := parseFloat(lower[name+"-utilization"])
		if !okUsed && status != "rejected" {
			return nil
		}
		w := &Window{UsedPercent: clampPct(used * 100), WindowMinutes: minutes, ObservedAt: observedAt}
		w.ResetAt = claudeResetHeader(lower[name+"-reset"])
		if status == "rejected" {
			// A rejection is the proof that the window is spent, whatever the
			// rounded utilization says.
			w.UsedPercent = 100
			q.LimitReached = true
		}
		return w
	}
	q.Short = read("5h", claudeShortMinutes)
	q.Long = read("7d", claudeLongMinutes)
	if !q.Known() && !q.LimitReached {
		return Quota{}, false
	}
	return q, true
}

func claudeResetHeader(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if v, ok := parseFloat(raw); ok && v > 1e9 {
		return resetTime(strconv.FormatFloat(v, 'f', -1, 64), "", time.Time{})
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}

// ParseClaudeUsageResponse parses the JSON from Claude's OAuth usage endpoint,
// the same one the panel reads.
func ParseClaudeUsageResponse(body []byte, observedAt time.Time) (Quota, bool) {
	var raw struct {
		FiveHour *claudeUsageWindowJS `json:"five_hour"`
		SevenDay *claudeUsageWindowJS `json:"seven_day"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Quota{}, false
	}
	q := Quota{ObservedAt: observedAt, Source: "poll"}
	q.Short = raw.FiveHour.window(claudeShortMinutes, observedAt)
	q.Long = raw.SevenDay.window(claudeLongMinutes, observedAt)
	if !q.Known() {
		return Quota{}, false
	}
	return q, true
}

type claudeUsageWindowJS struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

func (u *claudeUsageWindowJS) window(minutes int, observedAt time.Time) *Window {
	if u == nil || u.Utilization == nil {
		return nil
	}
	w := &Window{UsedPercent: clampPct(*u.Utilization), WindowMinutes: minutes, ObservedAt: observedAt}
	if u.ResetsAt != nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(*u.ResetsAt)); err == nil {
			w.ResetAt = t
		}
	}
	w.NotStarted = w.ResetAt.IsZero() && w.UsedPercent <= 0
	return w
}

// ParseClaudeQuotaError recognizes a Claude usage-limit failure from its body,
// for failures that arrived without rate-limit headers. A per-minute rate limit
// is not exhaustion: the proxy cools that down on its own.
func ParseClaudeQuotaError(status int, body string, now time.Time) (QuotaError, bool) {
	_ = now
	if status != 0 && status != 429 && status != 403 && status != 402 {
		return QuotaError{}, false
	}
	lb := strings.ToLower(body)
	if strings.Contains(lb, "per minute") {
		return QuotaError{}, false
	}
	if strings.Contains(lb, "usage limit") || strings.Contains(lb, "usage_limit") ||
		strings.Contains(lb, "exceed your account's rate limit") {
		return QuotaError{}, true
	}
	return QuotaError{}, false
}

// ParseClaudeProfilePlan returns the plan named by Claude's OAuth profile
// endpoint: max20, max5, max, pro, team or free. The organization's rate-limit
// tier is the only field that separates Max 5x from Max 20x.
func ParseClaudeProfilePlan(body []byte) string {
	var raw struct {
		Account struct {
			HasClaudeMax *bool `json:"has_claude_max"`
			HasClaudePro *bool `json:"has_claude_pro"`
		} `json:"account"`
		Organization struct {
			OrganizationType   string `json:"organization_type"`
			SubscriptionStatus string `json:"subscription_status"`
			RateLimitTier      string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	org := raw.Organization
	if strings.EqualFold(org.OrganizationType, "claude_team") && strings.EqualFold(org.SubscriptionStatus, "active") {
		return "team"
	}
	tier := strings.ToLower(org.RateLimitTier)
	switch {
	case strings.HasSuffix(tier, "claude_max_20x"):
		return "max20"
	case strings.HasSuffix(tier, "claude_max_5x"):
		return "max5"
	}
	maxFlag, proFlag := raw.Account.HasClaudeMax, raw.Account.HasClaudePro
	switch {
	case maxFlag != nil && *maxFlag:
		return "max"
	case proFlag != nil && *proFlag:
		return "pro"
	case maxFlag != nil && proFlag != nil:
		return "free"
	}
	return ""
}
