package proto

import (
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

func TestWatchTickerLine(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	tk := WatchTicker{PRChecked: now.Add(-40 * time.Second), Synced: now.Add(-time.Minute), PRPollSeconds: 120}
	if got, want := tk.Line(now), "ticker · PRs checked 40s ago, next 1m20s · synced 1m ago · gh ok"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	tk.GHFailing = true
	tk.PRChecked = now.Add(-3 * time.Minute)
	if got, want := tk.Line(now), "ticker · PRs checked 3m ago, due · synced 1m ago · gh failing"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if got := CheckedWords(time.Time{}, now); got != "" {
		t.Errorf("never checked: %q", got)
	}
	if got := CheckedWords(now.Add(-30*time.Second), now); got != "checked 30s ago" {
		t.Errorf("%q", got)
	}
}
