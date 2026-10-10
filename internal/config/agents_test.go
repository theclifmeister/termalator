package config

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestAgentSettingsLoad: [agents.<name>] hide, add and default_model
// load as set; the older models list still loads, its names counted as
// added; bad names fail Load as a bad safety setting does, and an
// unknown key is only listed in Unknown.
func TestAgentSettingsLoad(t *testing.T) {
	write(t, "[agents.claude]\nhide = [\"alpha-1\"]\nadd = [\"arn:aws:x/y\"]\ndefault_model = \"alpha\"\n\n[agents.codex]\nmodels = [{ name = \"beta\", about = \"older list\" }, { name = \"gamma\", about = \"x\" }]\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := AgentSettings{Hide: []string{"alpha-1"}, Add: []string{"arn:aws:x/y"}, DefaultModel: "alpha"}
	if got := c.Agent("claude"); !reflect.DeepEqual(got, want) {
		t.Fatalf("claude: %+v", got)
	}
	cx := c.Agent("codex")
	if !cx.HasLegacy || !reflect.DeepEqual(cx.Added(), []string{"beta", "gamma"}) || cx.Empty() {
		t.Fatalf("codex: %+v", cx)
	}
	if got := c.Agent("pi"); !got.Empty() {
		t.Fatalf("pi: %+v", got)
	}
	if got := c.AgentNames(); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("names: %v", got)
	}
	for _, bad := range []string{
		"[agents.claude]\nhide = [\"two words\"]\n",
		"[agents.claude]\nadd = [\"a\", \"a\"]\n",
		"[agents.claude]\nmodels = [{ name = \"two words\", about = \"x\" }]\n",
		"[agents.claude]\ndefault_model = \"two words\"\n",
	} {
		write(t, bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "agents.claude.") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	write(t, "[agents.claude]\nmodel = \"opus\"\nmodels = [{ name = \"a\", about = \"x\", default = true }]\n")
	c, err = Load()
	if err != nil || !reflect.DeepEqual(c.Unknown, []string{"agents.claude.model", "agents.claude.models.default"}) {
		t.Fatalf("unknown: %v %v", err, c.Unknown)
	}
}

// TestSetAgentModels: hide, add and default_model are written in one
// edit, the rest of the file kept, and read back as written; the older
// models list is removed; bad names are refused, and AgentSettings{}
// removes every key.
func TestSetAgentModels(t *testing.T) {
	write(t, "# mine\n[agents.claude]\n# keep\ndefault_model = \"alpha\" # note\nmodels = [{ name = \"beta\", about = \"x\" }]\n\n[projects.demo]\nyolo = true\n")
	s := AgentSettings{Hide: []string{"alpha-1"}, Add: []string{"beta"}, DefaultModel: "beta"}
	if err := SetAgentModels("claude", s); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	data, _ := os.ReadFile(path)
	if want := "# mine\n[agents.claude]\n# keep\ndefault_model = \"beta\" # note\nhide = [\"alpha-1\"]\nadd = [\"beta\"]\n\n[projects.demo]\nyolo = true\n"; string(data) != want {
		t.Fatalf("file:\n%s", data)
	}
	c, err := Load()
	if err != nil || !reflect.DeepEqual(c.Agent("claude"), s) {
		t.Fatalf("read back: %v %+v", err, c.Agent("claude"))
	}
	for _, bad := range []AgentSettings{
		{Hide: []string{"two words"}},
		{Add: []string{"a", "a"}},
		{DefaultModel: "a\nb"},
	} {
		if err := SetAgentModels("claude", bad); err == nil {
			t.Errorf("took %+v", bad)
		}
	}
	for _, name := range []string{"", "a.b", "two words"} {
		if err := SetAgentModels(name, AgentSettings{}); err == nil {
			t.Errorf("agent %q taken", name)
		}
	}
	if err := SetAgentModels("claude", AgentSettings{}); err != nil {
		t.Fatal(err)
	}
	c, _ = Load()
	if got := c.Agent("claude"); !got.Empty() {
		t.Fatalf("after reset: %+v", got)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "# keep\n\n[projects.demo]") {
		t.Fatalf("reset kept:\n%s", data)
	}
	// The array-of-tables form is the user's to edit by hand.
	write(t, "[[agents.claude.models]]\nname = \"opus\"\nabout = \"big\"\n")
	if err := SetAgentModels("claude", AgentSettings{Add: []string{"a"}}); !errors.Is(err, ErrForm) {
		t.Fatalf("array of tables: %v", err)
	}
}
