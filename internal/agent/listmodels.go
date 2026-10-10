package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/theclifmeister/terminatr/internal/config"
)

// ModelLister is a manifest's [list_models]: how tm asks the installed
// agent which models the user can use (docs/SPEC.md §8.2, Models). tm
// ships no model list: what the agent answers is the list, and an agent
// without a lister has none. The core (internal/models) runs
// launch.command with Args, writes Send to its stdin one line each, and
// hands every JSON line it prints to a ListState until the replies are
// in; this package only interprets them.
type ModelLister struct {
	Args []string `toml:"args"`
	Send []string `toml:"send"`
	// Timeout is how long the agent may take to answer, in seconds; 0
	// is DefaultListTimeout.
	Timeout int         `toml:"timeout_seconds"`
	Models  ListModels  `toml:"models"`
	Account ListAccount `toml:"account"`
}

// DefaultListTimeout is a lister's timeout_seconds when unset.
const DefaultListTimeout = 10

// ListModels says which line answers with the models and how to read
// them. The match tables are matches patterns over one entry; an empty
// one matches none.
type ListModels struct {
	Reply       map[string]string `toml:"reply"` // the line that answers
	Path        string            `toml:"path"`  // the list in it
	Name        string            `toml:"name"`  // each entry's: what model_args pass
	Display     string            `toml:"display"`
	Description string            `toml:"description"`
	// ResolvesTo is the model an entry stands for (an alias's target).
	ResolvesTo string `toml:"resolves_to"`
	// Skip: entries that are no model to pass (Claude's "default", the
	// user's own default). Hidden: entries the agent hides from its own
	// picker. Both are left out.
	Skip   map[string]string `toml:"skip"`
	Hidden map[string]string `toml:"hidden"`
	// Default: the agent's own default model.
	Default map[string]string `toml:"default"`
	// Older: entries of an older generation, offered but listed in one
	// line (tm context). OlderPinned also counts as older each pinned
	// entry (its name is what it resolves to) that nothing else
	// resolves to, when the list has an alias: the versions behind the
	// aliases.
	Older       map[string]string `toml:"older"`
	OlderPinned bool              `toml:"older_pinned"`
	// Tags are short notes shown after a model, each with the match
	// that gives it (e.g. "no auto mode" = { supportsAutoMode = "false" }).
	Tags map[string]map[string]string `toml:"tags"`
}

// ListAccount says which line names the account the models are for.
// Without a reply the agent has no account to tell.
type ListAccount struct {
	Reply map[string]string `toml:"reply"`
	Path  string            `toml:"path"` // the account object in it
	// Fingerprint fields (of the account object) tell one account,
	// plan or provider from another; Label fields name it for the user.
	Fingerprint []string `toml:"fingerprint"`
	Label       []string `toml:"label"`
	// LoggedOut matches the reply when no one is logged in: the agent
	// then can't say what the user may use.
	LoggedOut map[string]string `toml:"logged_out"`
}

func (l *ModelLister) validate() error {
	var errs []error
	if len(l.Models.Reply) == 0 || l.Models.Path == "" || l.Models.Name == "" {
		errs = append(errs, errors.New("models needs reply, path and name"))
	}
	if l.Timeout < 0 || l.Timeout > 120 {
		errs = append(errs, errors.New("timeout_seconds must be 0 to 120"))
	}
	if a := l.Account; len(a.Reply) == 0 && (a.Path != "" || len(a.Fingerprint) > 0 || len(a.LoggedOut) > 0) {
		errs = append(errs, errors.New("account needs reply"))
	}
	for tag := range l.Models.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 30 {
			errs = append(errs, fmt.Errorf("models.tags: %q is not a short tag", tag))
		}
	}
	return errors.Join(errs...)
}

// TimeoutSeconds is the lister's timeout.
func (l *ModelLister) TimeoutSeconds() int {
	if l.Timeout == 0 {
		return DefaultListTimeout
	}
	return l.Timeout
}

// ListedModel is one model the agent offers.
type ListedModel struct {
	Name        string   `json:"name"`
	Display     string   `json:"display,omitempty"`
	Description string   `json:"description,omitempty"`
	ResolvesTo  string   `json:"resolves_to,omitempty"`
	Default     bool     `json:"default,omitempty"`
	Older       bool     `json:"older,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// About is the model's line for the coordinator: the agent's own name
// and description, and its tags.
func (m ListedModel) About() string {
	parts := []string{}
	if m.Display != "" && m.Display != m.Name {
		parts = append(parts, m.Display)
	}
	if m.Description != "" {
		parts = append(parts, m.Description)
	}
	s := strings.Join(parts, ": ")
	if len(m.Tags) > 0 {
		s = strings.TrimPrefix(s+"; "+strings.Join(m.Tags, ", "), "; ")
	}
	return s
}

// Listing is what the agent answered.
type Listing struct {
	Models []ListedModel
	// LoggedOut: the agent said no one is logged in; Models is then not
	// what the user can use.
	LoggedOut bool
	// Account names the account for the user (e.g. "Claude Max,
	// firstParty"), and Fingerprint, a hash, tells it from another
	// without keeping who it is.
	Account     string
	Fingerprint string
}

// ListState collects the agent's replies.
type ListState struct {
	l       *ModelLister
	models  map[string]any
	account map[string]any
}

// NewListState starts reading l's replies.
func (l *ModelLister) NewListState() *ListState { return &ListState{l: l} }

// Take reads one line the agent printed, and reports whether every
// reply is in. Lines that aren't JSON objects are skipped.
func (s *ListState) Take(obj map[string]any) bool {
	if s.models == nil && matches(s.l.Models.Reply, obj) {
		s.models = obj
	}
	if a := s.l.Account; len(a.Reply) > 0 && s.account == nil && matches(a.Reply, obj) {
		s.account = obj
	}
	return s.Done()
}

// Done reports whether every reply is in.
func (s *ListState) Done() bool {
	return s.models != nil && (len(s.l.Account.Reply) == 0 || s.account != nil)
}

// Listing interprets the replies. It fails when the models reply is
// missing or its list isn't one.
func (s *ListState) Listing() (Listing, error) {
	var out Listing
	if a := s.l.Account; s.account != nil {
		out.LoggedOut = len(a.LoggedOut) > 0 && matches(a.LoggedOut, s.account)
		acc := s.account
		if a.Path != "" {
			v, _ := lookup(s.account, a.Path)
			acc, _ = v.(map[string]any)
		}
		var fp, label []string
		for _, f := range a.Fingerprint {
			v, _ := lookupString(acc, f)
			fp = append(fp, f+"="+v)
		}
		for _, f := range a.Label {
			if v, _ := lookupString(acc, f); v != "" {
				label = append(label, v)
			}
		}
		if len(fp) > 0 {
			h := sha256.Sum256([]byte(strings.Join(fp, "\n")))
			out.Fingerprint = hex.EncodeToString(h[:8])
		}
		out.Account = oneLine(strings.Join(label, ", "))
	}
	if s.models == nil {
		return out, errors.New("the agent didn't answer with its models")
	}
	v, ok := lookup(s.models, s.l.Models.Path)
	list, isList := v.([]any)
	if !ok || !isList {
		return out, fmt.Errorf("the agent's answer has no list at %s", s.l.Models.Path)
	}
	m := s.l.Models
	hit := func(want map[string]string, e map[string]any) bool { return len(want) > 0 && matches(want, e) }
	str := func(e map[string]any, path string) string {
		if path == "" {
			return ""
		}
		v, _ := lookupString(e, path)
		return oneLine(v)
	}
	resolved := map[string]bool{} // what an entry other than itself resolves to
	alias := false
	var entries []map[string]any
	for _, x := range list {
		e, ok := x.(map[string]any)
		if !ok {
			continue
		}
		name, to := str(e, m.Name), str(e, m.ResolvesTo)
		if to != "" && to != name {
			resolved[to] = true
			if !hit(m.Skip, e) {
				alias = true
			}
		}
		if hit(m.Skip, e) || hit(m.Hidden, e) || config.CheckModelName(name) != nil {
			continue
		}
		entries = append(entries, e)
	}
	for _, e := range entries {
		x := ListedModel{Name: str(e, m.Name), Display: str(e, m.Display), Description: str(e, m.Description),
			ResolvesTo: str(e, m.ResolvesTo), Default: hit(m.Default, e), Older: hit(m.Older, e)}
		if m.OlderPinned && alias && (x.ResolvesTo == "" || x.ResolvesTo == x.Name) && !resolved[x.Name] {
			x.Older = true
		}
		for tag, want := range m.Tags {
			if hit(want, e) {
				x.Tags = append(x.Tags, tag)
			}
		}
		slices.Sort(x.Tags)
		if slices.ContainsFunc(out.Models, func(y ListedModel) bool { return y.Name == x.Name }) {
			continue
		}
		out.Models = append(out.Models, x)
	}
	return out, nil
}

// oneLine is s on one line, at most config.MaxModelAbout characters.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > config.MaxModelAbout {
		s = string(r[:config.MaxModelAbout-1]) + "…"
	}
	return s
}
