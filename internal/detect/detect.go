// Package detect is the agent-neutral screen-rule engine (docs/SPEC.md
// §8.4). Agents declare rules as data (agent.Rule); this package evaluates
// them against the emulator's text. It knows nothing about any agent.
package detect

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/theclifmeister/termalator/internal/agent"
)

// Screen is what rules look at: the window title and the visible rows,
// as shown and with dim cells blanked (for skip_dim rules).
type Screen struct {
	Title string
	Rows  []string
	// NoDim is Rows with every faint cell replaced by spaces. Nil means
	// the same as Rows.
	NoDim []string
}

// Match is the rule that decided a screen.
type Match struct {
	Rule   string      `json:"rule"`
	State  agent.State `json:"state"`
	Reason string      `json:"reason,omitempty"`
}

// Engine evaluates one agent's rules. Build it once per agent with New.
type Engine struct {
	rules []compiled
}

type compiled struct {
	agent.Rule
	re     *regexp.Regexp
	bottom int // region bottom:N; 0 for title and screen
	title  bool
}

// New compiles rules. Manifest loading already rejected bad regexes, so
// an error here means the rules didn't come from a validated manifest.
func New(rules []agent.Rule) (*Engine, error) {
	e := &Engine{}
	for _, r := range rules {
		c := compiled{Rule: r}
		switch {
		case r.Region == "title":
			c.title = true
		case r.Region == "screen" || r.Region == "":
		case strings.HasPrefix(r.Region, "bottom:"):
			n, err := strconv.Atoi(strings.TrimPrefix(r.Region, "bottom:"))
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("rule %s: bad region %q", r.ID, r.Region)
			}
			c.bottom = n
		default:
			return nil, fmt.Errorf("rule %s: bad region %q", r.ID, r.Region)
		}
		if r.Regex != "" {
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				return nil, fmt.Errorf("rule %s: %w", r.ID, err)
			}
			c.re = re
		}
		e.rules = append(e.rules, c)
	}
	return e, nil
}

// Eval returns the matching rule with the highest priority (the first
// one listed on a tie), and every rule that matched, for explain.
func (e *Engine) Eval(s Screen) (best *Match, all []Match) {
	bestPrio := 0
	for _, r := range e.rules {
		if !r.matches(s) {
			continue
		}
		m := Match{Rule: r.ID, State: r.State, Reason: r.Reason}
		all = append(all, m)
		if best == nil || r.Priority > bestPrio {
			mm := m
			best, bestPrio = &mm, r.Priority
		}
	}
	return best, all
}

func (r compiled) matches(s Screen) bool {
	text := r.text(s)
	for _, c := range r.Contains {
		if !strings.Contains(text, c) {
			return false
		}
	}
	for _, c := range r.Not {
		if strings.Contains(text, c) {
			return false
		}
	}
	if r.re != nil && !r.re.MatchString(text) {
		return false
	}
	return len(r.Contains) > 0 || r.re != nil
}

// text is the region the rule reads.
func (r compiled) text(s Screen) string {
	if r.title {
		return s.Title
	}
	rows := s.Rows
	if r.SkipDim && s.NoDim != nil {
		rows = s.NoDim
	}
	// Blank rows at the bottom of a screen are not content: a bottom:N
	// region counts from the last non-blank row.
	end := len(rows)
	for end > 0 && strings.TrimSpace(rows[end-1]) == "" {
		end--
	}
	start := 0
	if r.bottom > 0 && end-r.bottom > 0 {
		start = end - r.bottom
	}
	return strings.Join(rows[start:end], "\n")
}
