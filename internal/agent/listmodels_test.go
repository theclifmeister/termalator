package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestListState: made-up replies for each rule of the reading.
func TestListState(t *testing.T) {
	l := &ModelLister{
		Models: ListModels{Reply: map[string]string{"id": "2"}, Path: "result.list", Name: "id", Display: "name",
			Description: "about", ResolvesTo: "to", Skip: map[string]string{"id": "auto"}, Hidden: map[string]string{"hidden": "true"},
			Default: map[string]string{"main": "true"}, Older: map[string]string{"old": "true"}, OlderPinned: true,
			Tags: map[string]map[string]string{"slow": {"fast": "false"}}},
		Account: ListAccount{Reply: map[string]string{"id": "1"}, Path: "result", Fingerprint: []string{"plan"}, Label: []string{"plan"},
			LoggedOut: map[string]string{"result.plan": "!*"}},
	}
	read := func(lines ...string) (Listing, error) {
		st := l.NewListState()
		for _, s := range lines {
			var obj map[string]any
			if err := json.Unmarshal([]byte(s), &obj); err != nil {
				t.Fatal(err)
			}
			st.Take(obj)
		}
		return st.Listing()
	}
	got, err := read(`{"id":1,"result":{"plan":"gold"}}`,
		`{"id":2,"result":{"list":[{"id":"auto","to":"alpha-2"},{"id":"alpha","to":"alpha-2","name":"Alpha","about":"big\nwork","main":true},`+
			`{"id":"alpha-2","to":"alpha-2"},{"id":"alpha-1","to":"alpha-1","fast":false},{"id":"beta","old":true},{"id":"gamma","hidden":true},`+
			`{"id":"two words"},{"id":"alpha"},7]}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha* Alpha: big work", "alpha-2", "alpha-1 older slow", "beta older"}
	var have []string
	for _, m := range got.Models {
		s := m.Name
		if m.Default {
			s += "*"
		}
		if a := m.About(); a != "" && !m.Older {
			s += " " + a
		}
		if m.Older {
			s += " older"
		}
		if len(m.Tags) > 0 {
			s += " " + strings.Join(m.Tags, ",")
		}
		have = append(have, s)
	}
	if strings.Join(have, "|") != strings.Join(want, "|") {
		t.Errorf("models %q, want %q", have, want)
	}
	if got.Account != "gold" || got.Fingerprint == "" || got.LoggedOut {
		t.Errorf("account %+v", got)
	}
	if out, _ := read(`{"id":1,"result":{}}`, `{"id":2,"result":{"list":[]}}`); !out.LoggedOut {
		t.Error("no plan isn't logged out")
	}
	if _, err := read(`{"id":1,"result":{}}`, `{"id":2,"result":{"list":"nope"}}`); err == nil {
		t.Error("a reply without a list reads")
	}
	if _, err := read(`{"id":1,"result":{}}`); err == nil {
		t.Error("no models reply reads")
	}
}

// TestSwitchText: the switch command with the model, or with the
// agent's word for its own default; none without switch_model (Codex).
func TestSwitchText(t *testing.T) {
	reg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := reg.Get("claude")
	codex, _ := reg.Get("codex")
	for _, c := range []struct {
		a     Agent
		model string
		want  string
	}{
		{claude, "alpha", "/model alpha"},
		{claude, "", "/model default"},
		{codex, "alpha", ""},
		{codex, "", ""},
	} {
		if got := ManifestOf(c.a).SwitchText(c.model); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.a.Name(), c.model, got, c.want)
		}
	}
	const base = "manifest_version = 1\nname = \"a\"\n[launch]\ncommand = \"a\"\n[inject]\n"
	for body, ok := range map[string]bool{
		"switch_model = \"/model {model}\"\n":                              true,
		"switch_model = \"/model\"\n":                                      false,
		"switch_model_default = \"default\"\n":                             false,
		"switch_model = \"/m {model}\"\nswitch_model_default = \"auto\"\n": true,
	} {
		if _, err := ParseManifest([]byte(base + body)); (err == nil) != ok {
			t.Errorf("%q: %v", body, err)
		}
	}
}
