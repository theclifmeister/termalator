package proto

import (
	"strings"
	"testing"
	"time"
)

func TestAgeWords(t *testing.T) {
	for d, want := range map[time.Duration]string{-time.Second: "0s", 40 * time.Second: "40s", 80 * time.Second: "1m20s",
		time.Minute: "1m", 125 * time.Minute: "2h5m", 2 * time.Hour: "2h"} {
		if got := AgeWords(d); got != want {
			t.Errorf("AgeWords(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestWatchTickerRows(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	join := func(rows []TickerRow) string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Label+"|"+r.Value)
		}
		return strings.Join(out, ";")
	}
	tk := WatchTicker{PRChecked: now.Add(-40 * time.Second), Synced: now.Add(-time.Minute), PRPollSeconds: 120}
	if got, want := join(tk.Rows(now)), "PRs|40s ago · next 1m20s;synced|1m ago;PR host|ok"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	tk.GHFailing = true
	tk.PRChecked = now.Add(-3 * time.Minute)
	rows := tk.Rows(now)
	if got, want := join(rows), "PRs|3m ago · due;synced|1m ago;PR host|failing"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	for _, r := range rows {
		if r.Bad != (r.Label == "PR host") {
			t.Errorf("%s: bad = %v", r.Label, r.Bad)
		}
	}
	if got := join(WatchTicker{Synced: now.Add(-time.Minute)}.Rows(now)); got != "synced|1m ago" {
		t.Errorf("no PR check yet: %q", got)
	}
	if got := join(WatchTicker{Synced: now.Add(-time.Minute), NoPRHost: true, GHFailing: true}.Rows(now)); got != "synced|1m ago;PR host|none" {
		t.Errorf("no PR host: %q", got)
	}
	if got := CheckedWords(time.Time{}, now); got != "" {
		t.Errorf("never checked: %q", got)
	}
	if got := CheckedWords(now.Add(-30*time.Second), now); got != "checked 30s ago" {
		t.Errorf("%q", got)
	}
}
