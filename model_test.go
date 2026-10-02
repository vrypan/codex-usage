package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }
func sample(t *testing.T) state {
	t.Helper()
	var l limits
	err := json.Unmarshal([]byte(`{"accountId":"a","rateLimitsByLimitId":{"codex":{"limitId":"codex","primary":{"usedPercent":32,"windowDurationMins":300,"resetsAt":36000},"secondary":{"usedPercent":61,"windowDurationMins":10080,"resetsAt":90000},"credits":{"hasCredits":true,"unlimited":false,"balance":"120.50"}}},"rateLimitResetCredits":{"availableCount":2}}`), &l)
	if err != nil {
		t.Fatal(err)
	}
	s := state{Connected: true}
	s.replaceLimits(l, time.Unix(30000, 0))
	s.Usage = &usage{}
	s.Usage.Summary.Lifetime = ptr(int64(124000))
	s.UsageAt = ptr(time.Unix(30000, 0))
	return s
}
func TestSparseUpdatePreservesCreditsAndSecondary(t *testing.T) {
	s := sample(t)
	s.notification(rpcMessage{Method: "account/rateLimits/updated", Params: json.RawMessage(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":45,"windowDurationMins":300,"resetsAt":36000},"credits":null,"secondary":null}}`)})
	b, _ := s.Limits.selectBucket("")
	if b.Primary.UsedPercent != 45 || b.Secondary.UsedPercent != 61 || *b.Credits.Balance != "120.50" {
		t.Fatalf("bad merge: %+v", b)
	}
	if s.LimitsAt.Unix() != 30000 {
		t.Fatal("partial update refreshed entire snapshot")
	}
}
func TestAccountSwitchClearsAllUsage(t *testing.T) {
	s := sample(t)
	s.Thread = &usage{}
	s.replaceLimits(limits{AccountID: ptr("b")}, time.Now())
	if s.Usage != nil || s.Thread != nil || s.UsageAt != nil {
		t.Fatal("old account usage survived")
	}
	s = sample(t)
	s.notification(rpcMessage{Method: "account/updated"})
	if s.Limits != nil || s.Usage != nil {
		t.Fatal("auth notification retained data")
	}
}
func TestFullReadClearsUnavailableCredits(t *testing.T) {
	s := sample(t)
	s.replaceLimits(limits{Buckets: map[string]bucket{"codex": {}}}, time.Now())
	b, _ := s.Limits.selectBucket("")
	if b.Credits != nil {
		t.Fatal("full response should replace null")
	}
}
func TestRenderMeaningAndStaleness(t *testing.T) {
	s := sample(t)
	o := options{bars: true, interval: time.Minute, usageInterval: 5 * time.Minute}
	text := render(s, o, time.Unix(30000, 0))
	for _, want := range []string{"5h", "32% used", "7d", "61% used", "credits 120.50", "resets 2", "tokens lifetime 124.0k"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	s.LimitsError = "offline"
	text = render(s, o, time.Unix(30180, 0))
	if !strings.Contains(text, "quota stale 3m") {
		t.Fatal(text)
	}
	if !strings.Contains(render(s, o, time.Unix(100000, 0)), "refreshing") {
		t.Fatal("expired reset was not labeled")
	}
}
func TestNamedLineOmitsDuplicateName(t *testing.T) {
	s := sample(t)
	o := options{embedded: true, interval: time.Minute, usageInterval: 5 * time.Minute}
	got := render(s, o, time.Unix(30000, 0))
	if strings.HasPrefix(got, "codex") || !strings.HasPrefix(got, "5h ") {
		t.Fatal(got)
	}
	o.embedded = false
	if got := render(s, o, time.Unix(30000, 0)); !strings.HasPrefix(got, "codex  5h ") {
		t.Fatal(got)
	}
}
func TestCreditsAndMissingValues(t *testing.T) {
	for _, tc := range []struct {
		c    *credits
		want string
	}{{nil, "—"}, {&credits{}, "0"}, {&credits{HasCredits: true}, "available"}, {&credits{Unlimited: true}, "unlimited"}, {&credits{Balance: ptr("0")}, "0"}} {
		if got := creditText(tc.c); got != tc.want {
			t.Fatalf("%s != %s", got, tc.want)
		}
	}
	s := state{Connected: true}
	got := render(s, options{}, time.Now())
	if strings.Contains(got, "0%") || !strings.Contains(got, "tokens lifetime —") {
		t.Fatal(got)
	}
}
func TestThreadTokensAreCompleteEstimates(t *testing.T) {
	s := sample(t)
	o := options{thread: "t", interval: time.Minute}
	s.Thread = &usage{Thread: &threadUsage{ID: "t", CreditsMicros: 1234567, Groups: []tokenGroup{{Total: ptr(int64(42))}, {Total: ptr(int64(8))}}}}
	text := render(s, o, time.Unix(30000, 0))
	if !strings.Contains(text, "thread est. tokens 50") || !strings.Contains(text, "est. credits 1.235") {
		t.Fatal(text)
	}
	s.Thread.Thread.Groups[1].Total = nil
	if got := render(s, o, time.Unix(30000, 0)); !strings.Contains(got, "thread est. tokens —") {
		t.Fatal(got)
	}
}
func TestClippingAndControlSanitization(t *testing.T) {
	s := sample(t)
	o := options{width: 65, color: true, bars: true}
	got := render(s, o, time.Unix(30000, 0))
	if visibleWidth(got) > 65 || !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatal(got)
	}
	if clean("abc\n\x1b\x07\u202edef") != "abcdef" {
		t.Fatal("controls survived")
	}
}
