package tui

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
)

// The dashboard's layout: whether the details panel shows beside the
// list, and how wide the list is. It is the user's view preference, so it
// lives in ui.json next to config.toml, never in config.toml, which tm
// doesn't write (docs/SPEC.md §5.1, §11.2).

// Layout is ui.json.
type Layout struct {
	// Details shows the details panel beside the list in a window at
	// least splitMin columns wide.
	Details bool `json:"details"`
	// Split is the list's share of the window's width, between minSplit
	// and maxSplit.
	Split float64 `json:"split"`
}

const (
	splitMin     = 120  // narrower windows show details under the row
	defaultSplit = 0.6  // the list's share
	minSplit     = 0.3  // of the width, either way
	maxSplit     = 0.8  //
	splitStep    = 0.05 // < and > move the divider this much
)

// DefaultLayout is the layout without a ui.json.
var DefaultLayout = Layout{Details: true, Split: defaultSplit}

// LoadLayout reads path; a missing or unreadable file gives the default.
func LoadLayout(path string) Layout {
	l := DefaultLayout
	if path == "" {
		return l
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &l) != nil {
		return DefaultLayout
	}
	l.Split = clampSplit(l.Split)
	return l
}

// SaveLayout writes l to path, atomically.
func SaveLayout(path string, l Layout) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ui-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// clampSplit keeps a split in range, to two decimals.
func clampSplit(f float64) float64 {
	if f == 0 {
		return defaultSplit
	}
	return math.Round(min(max(f, minSplit), maxSplit)*100) / 100
}

// split says whether the dashboard shows the details panel, and the
// list's width when it does.
func (m *dash) split() (bool, int) {
	if !m.layout.Details || m.w < splitMin {
		return false, m.w
	}
	return true, int(float64(m.w)*m.layout.Split + 0.5)
}

// setLayout changes the layout and saves it.
func (m *dash) setLayout(l Layout) {
	l.Split = clampSplit(l.Split)
	if l != m.layout {
		m.layout = l
		m.saveLayout()
	}
}

// saveLayout writes ui.json; a failed save only costs the preference,
// so it shows in the footer and nothing else.
func (m *dash) saveLayout() {
	if m.uiFile == "" {
		return
	}
	if err := SaveLayout(m.uiFile, m.layout); err != nil {
		m.fail(err)
	}
}
