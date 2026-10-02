package main

import (
	"encoding/json"
	"time"
)

type window struct {
	UsedPercent float64 `json:"usedPercent"`
	Duration    *int64  `json:"windowDurationMins"`
	ResetsAt    *int64  `json:"resetsAt"`
}
type credits struct {
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}
type bucket struct {
	ID        *string  `json:"limitId"`
	Name      *string  `json:"limitName"`
	Primary   *window  `json:"primary"`
	Secondary *window  `json:"secondary"`
	Credits   *credits `json:"credits"`
	Reached   *string  `json:"rateLimitReachedType"`
}
type limits struct {
	Legacy  *bucket           `json:"rateLimits"`
	Buckets map[string]bucket `json:"rateLimitsByLimitId"`
	Resets  *struct {
		AvailableCount int64 `json:"availableCount"`
	} `json:"rateLimitResetCredits"`
	AccountID *string `json:"accountId"`
}
type tokenGroup struct {
	Model         *string `json:"model"`
	Input         *int64  `json:"inputTokens"`
	Cached        *int64  `json:"cachedInputTokens"`
	Output        *int64  `json:"outputTokens"`
	Total         *int64  `json:"totalTokens"`
	CreditsMicros int64   `json:"estimatedUsageCreditsMicros"`
}
type threadUsage struct {
	ID            string       `json:"threadId"`
	CreditsMicros int64        `json:"estimatedUsageCreditsMicros"`
	USDMicros     *int64       `json:"estimatedUsageUsdMicros"`
	Groups        []tokenGroup `json:"groups"`
}
type usage struct {
	Summary struct {
		Lifetime  *int64 `json:"lifetimeTokens"`
		PeakDaily *int64 `json:"peakDailyTokens"`
	} `json:"summary"`
	Daily []struct {
		Date   string `json:"startDate"`
		Tokens int64  `json:"tokens"`
	} `json:"dailyUsageBuckets"`
	Thread *threadUsage `json:"threadUsage,omitempty"`
}
type state struct {
	Limits      *limits    `json:"limits"`
	Usage       *usage     `json:"usage"`
	Thread      *usage     `json:"thread,omitempty"`
	LimitsAt    *time.Time `json:"limitsUpdatedAt"`
	UsageAt     *time.Time `json:"usageUpdatedAt"`
	ThreadAt    *time.Time `json:"threadUpdatedAt,omitempty"`
	LimitsError string     `json:"limitsError,omitempty"`
	UsageError  string     `json:"usageError,omitempty"`
	ThreadError string     `json:"threadError,omitempty"`
	Connected   bool       `json:"connected"`
}

func (s *state) clearAccount() { *s = state{Connected: s.Connected} }

// A complete read replaces old fields, including nulls. Account identity changes
// invalidate token snapshots, even if the auth-change notification was missed.
func (s *state) replaceLimits(l limits, now time.Time) {
	if s.Limits != nil && s.Limits.AccountID != nil && l.AccountID != nil && *s.Limits.AccountID != *l.AccountID {
		s.clearAccount()
	}
	s.Limits = &l
	s.LimitsAt = &now
	s.LimitsError = ""
}

// Notifications are sparse. Preserve nullable fields unless the protocol says
// null is authoritative. A later full read resolves removals and cleared limits.
func mergeBucket(old, update bucket) bucket {
	if update.ID != nil {
		old.ID = update.ID
	}
	if update.Name != nil {
		old.Name = update.Name
	}
	if update.Primary != nil {
		old.Primary = update.Primary
	}
	if update.Secondary != nil {
		old.Secondary = update.Secondary
	}
	if update.Credits != nil {
		old.Credits = update.Credits
	}
	if update.Reached != nil {
		old.Reached = update.Reached
	}
	return old
}
func (s *state) notification(m rpcMessage) bool {
	if m.Method == "account/updated" {
		s.clearAccount()
		return true
	}
	if m.Method != "account/rateLimits/updated" {
		return false
	}
	var update struct {
		Limits bucket `json:"rateLimits"`
	}
	if json.Unmarshal(m.Params, &update) != nil || s.Limits == nil {
		return true
	}
	id := "codex"
	if update.Limits.ID != nil {
		id = *update.Limits.ID
	}
	if len(s.Limits.Buckets) > 0 {
		s.Limits.Buckets[id] = mergeBucket(s.Limits.Buckets[id], update.Limits)
	}
	if s.Limits.Legacy != nil && (s.Limits.Legacy.ID == nil || *s.Limits.Legacy.ID == id) {
		b := mergeBucket(*s.Limits.Legacy, update.Limits)
		s.Limits.Legacy = &b
	}
	// Do not refresh snapshot timestamps: a notification does not refresh every
	// credit field. Schedule a full read, with a minimum spacing in the watcher.
	return true
}
