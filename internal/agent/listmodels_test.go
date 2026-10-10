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
