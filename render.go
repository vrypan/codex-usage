package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, s)
}
func paint(s string, color int, enabled bool) string {
	if !enabled {
		return s
	}
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", color, s)
}
func compact(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.FormatInt(n, 10)
}
func duration(mins int64) string {
	if mins >= 1440 && mins%1440 == 0 {
		return fmt.Sprintf("%dd", mins/1440)
	}
	if mins >= 60 && mins%60 == 0 {
		return fmt.Sprintf("%dh", mins/60)
	}
	return fmt.Sprintf("%dm", mins)
}
func countdown(at int64, now time.Time) string {
	seconds := at - now.Unix()
	if seconds <= 0 {
		return "refreshing"
	}
	mins := (seconds + 59) / 60
	if mins >= 1440 {
		return fmt.Sprintf("%dd%dh", mins/1440, (mins%1440)/60)
	}
	if mins >= 60 {
		return fmt.Sprintf("%dh%02dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}
func renderWindow(w *window, now time.Time, color, bars bool) string {
	if w == nil {
		return ""
	}
	label := "window"
	if w.Duration != nil {
		label = duration(*w.Duration)
	}
	level := 32
	if w.UsedPercent >= 90 {
		level = 31
	} else if w.UsedPercent >= 70 {
		level = 33
	}
	meter := ""
	if bars {
		n := int(math.Floor(math.Max(0, math.Min(100, w.UsedPercent)) / 10))
		meter = paint(strings.Repeat("━", n), level, color) + paint(strings.Repeat("─", 10-n), 90, color) + " "
	}
	text := paint(label, 35, color) + " " + meter + paint(fmt.Sprintf("%.0f%% used", w.UsedPercent), level, color)
	if w.ResetsAt != nil {
		text += " ↻" + countdown(*w.ResetsAt, now)
	}
	return text
}
func (l *limits) selectBucket(id string) (*bucket, string) {
	if l == nil {
		return nil, ""
	}
	if len(l.Buckets) > 0 {
		if id == "" {
			if _, ok := l.Buckets["codex"]; ok {
				id = "codex"
			} else {
				keys := make([]string, 0, len(l.Buckets))
				for k := range l.Buckets {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				id = keys[0]
			}
		}
		b, ok := l.Buckets[id]
		if !ok {
			return nil, id
		}
		return &b, id
	}
	if l.Legacy != nil {
		actual := "codex"
		if l.Legacy.ID != nil {
			actual = *l.Legacy.ID
		}
		if id == "" || id == actual {
			return l.Legacy, actual
		}
	}
	return nil, id
}
func creditText(c *credits) string {
	if c == nil {
		return "—"
	}
	if c.Unlimited {
		return "unlimited"
	}
	if c.Balance != nil {
		return clean(*c.Balance)
	}
	if !c.HasCredits {
		return "0"
	}
	return "available"
}
func stale(at *time.Time, failure bool, now time.Time, interval time.Duration) string {
	if at == nil {
		return ""
	}
	age := now.Sub(*at)
	if failure || age > 2*interval {
		if age < 0 {
			age = 0
		}
		return fmt.Sprintf("stale %dm", int(age.Minutes()))
	}
	return ""
}
func render(s state, opts options, now time.Time) string {
	b, id := s.Limits.selectBucket(opts.bucket)
	prefix := paint("codex", 34, opts.color)
	if id != "" && id != "codex" {
		prefix += "/" + clean(id)
	}
	parts := []string{prefix}
	if b == nil {
		parts = append(parts, "quota —", "credits —")
	} else {
		if b.Primary != nil {
			parts = append(parts, renderWindow(b.Primary, now, opts.color, opts.bars))
		}
		if b.Secondary != nil {
			parts = append(parts, renderWindow(b.Secondary, now, opts.color, opts.bars))
		}
		if b.Primary == nil && b.Secondary == nil {
			parts = append(parts, "quota —")
		}
		parts = append(parts, "credits "+creditText(b.Credits))
		if b.Reached != nil {
			parts = append(parts, paint("limit "+clean(*b.Reached), 31, opts.color))
		}
	}
	if s.Limits != nil && s.Limits.Resets != nil {
		parts = append(parts, fmt.Sprintf("resets %d", s.Limits.Resets.AvailableCount))
	}
	if msg := stale(s.LimitsAt, s.LimitsError != "", now, opts.interval); msg != "" {
		parts = append(parts, paint("quota "+msg, 33, opts.color))
	}
	if s.LimitsError != "" && s.LimitsAt == nil {
		parts = append(parts, paint("quota unavailable", 33, opts.color))
	}
	if !s.Connected {
		parts = append(parts, paint("disconnected", 33, opts.color))
	}
	// Token totals are cumulative activity, never context-window usage.
	tokens := "tokens lifetime —"
	if s.Usage != nil && s.Usage.Summary.Lifetime != nil {
		tokens = "tokens lifetime " + compact(*s.Usage.Summary.Lifetime)
	}
	if msg := stale(s.UsageAt, s.UsageError != "", now, opts.usageInterval); msg != "" {
		tokens += " (" + msg + ")"
	}
	if s.UsageError != "" && s.UsageAt == nil {
		tokens = "tokens unavailable"
	}
	if opts.thread != "" {
		tokens = "thread est. tokens —"
		if s.Thread != nil && s.Thread.Thread != nil {
			t := s.Thread.Thread
			var total int64
			known := len(t.Groups) > 0
			for _, g := range t.Groups {
				if g.Total == nil {
					known = false
				} else {
					total += *g.Total
				}
			}
			if known {
				tokens = "thread est. tokens " + compact(total)
			}
			tokens += fmt.Sprintf(" · est. credits %.3f", float64(t.CreditsMicros)/1e6)
		}
		if msg := stale(s.ThreadAt, s.ThreadError != "", now, opts.interval); msg != "" {
			tokens += " (" + msg + ")"
		}
		if s.ThreadError != "" && s.ThreadAt == nil {
			tokens = "thread usage unavailable"
		}
	}
	parts = append(parts, tokens)
	text := strings.Join(parts, "  ")
	// Use compact meters before clipping. Width is an explicit cell budget; our
	// numeric output and meter glyphs are single cell in common terminal fonts.
	if opts.width > 0 && visibleWidth(text) > opts.width && opts.bars {
		opts.bars = false
		return render(s, opts, now)
	}
	if opts.width > 0 {
		return clipANSI(text, opts.width)
	}
	return text
}
func visibleWidth(s string) int {
	return utf8.RuneCountInString(stripANSI(s))
}
func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 27 && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		out.WriteRune(r)
		i += n
	}
	return out.String()
}
func clipANSI(s string, width int) string {
	if visibleWidth(s) <= width {
		return s
	}
	if width <= 0 {
		return ""
	}
	var out strings.Builder
	count := 0
	for i := 0; i < len(s) && count < width-1; {
		if s[i] == 27 && i+1 < len(s) && s[i+1] == '[' {
			start := i
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++
			}
			out.WriteString(s[start:i])
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		out.WriteRune(r)
		i += n
		count++
	}
	out.WriteString("…")
	if strings.Contains(s, "\x1b[") {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}
