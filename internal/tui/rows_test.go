package tui

import "testing"

func TestPRRefAzure(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/o/r/pull/7":                     "#7",
		"https://dev.azure.com/o/p/_git/r/pullrequest/34":   "#34",
		"https://o.visualstudio.com/p/_git/r/pullrequest/2": "#2",
	} {
		if got := prRef(url); got != want {
			t.Errorf("prRef(%q) = %q, want %q", url, got, want)
		}
	}
}
