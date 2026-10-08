package config

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestAgentCatalogLoad: [agents.<name>] models and default_model load
// as set; unset keys stay unset; a bad catalog fails Load as a bad
// safety setting does, and an unknown key is only listed in Unknown.
func TestAgentCatalogLoad(t *testing.T) {
	write(t, "[agents.claude]\nmodels = [{ name = \"opus\", about = \"big\" }, { name = \"x-1.5\", about = \"small\" }]\ndefault_model = \"x-1.5\"\n\n[agents.codex]\ndefault_model = \"\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := AgentSettings{Models: []AgentModel{{"opus", "big"}, {"x-1.5", "small"}}, HasModels: true, DefaultModel: "x-1.5", HasDefault: true}
	if got := c.Agent("claude"); !reflect.DeepEqual(got, want) {
		t.Fatalf("claude: %+v", got)
	}
	if got := c.Agent("codex"); got.HasModels || !got.HasDefault || got.DefaultModel != "" {
		t.Fatalf("codex: %+v", got)
	}
	if got := c.Agent("pi"); got.HasModels || got.HasDefault {
		t.Fatalf("pi: %+v", got)
	}
	if got := c.AgentNames(); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("names: %v", got)
	}
	// Array-of-tables form reads the same.
	write(t, "[[agents.claude.models]]\nname = \"opus\"\nabout = \"big\"\n")
	if c, err := Load(); err != nil || len(c.Agent("claude").Models) != 1 {
		t.Fatalf("array of tables: %v %+v", err, c.Agent("claude"))
	}
	for _, bad := range []string{
		"[agents.claude]\nmodels = [{ name = \"two words\", about = \"x\" }]\n",
		"[agents.claude]\nmodels = [{ name = \"a\", about = \"\" }]\n",
		"[agents.claude]\nmodels = [{ name = \"a\", about = \"x\" }, { name = \"a\", about = \"y\" }]\n",
		"[agents.claude]\nmodels = [{ name = \"a\", about = \"x\" }]\ndefault_model = \"b\"\n",
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
	// An empty list is a catalog without models.
	write(t, "[agents.claude]\nmodels = []\n")
	if c, err := Load(); err != nil || !c.Agent("claude").HasModels || len(c.Agent("claude").Models) != 0 {
		t.Fatalf("empty: %v %+v", err, c.Agent("claude"))
	}
}

// TestSetAgentModels: the catalog and default are written in one edit,
// the rest of the file kept, and read back as written; a default the
// models leave out, a bad model or agent name is refused, and
// AgentSettings{} removes both keys.
func TestSetAgentModels(t *testing.T) {
	write(t, "# mine\n[agents.claude]\n# keep\ndefault_model = \"opus\" # note\n\n[projects.demo]\nyolo = true\n")
	s := AgentSettings{Models: []AgentModel{{"opus", `big "quoted"`}, {"sonnet", "balanced"}}, HasModels: true, DefaultModel: "sonnet", HasDefault: true}
	if err := SetAgentModels("claude", s); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	data, _ := os.ReadFile(path)
	if want := "# mine\n[agents.claude]\n# keep\ndefault_model = \"sonnet\" # note\nmodels = [{ name = \"opus\", about = \"big \\\"quoted\\\"\" }, { name = \"sonnet\", about = \"balanced\" }]\n\n[projects.demo]\nyolo = true\n"; string(data) != want {
		t.Fatalf("file:\n%s", data)
	}
	c, err := Load()
	if err != nil || !reflect.DeepEqual(c.Agent("claude"), s) {
		t.Fatalf("read back: %v %+v", err, c.Agent("claude"))
	}
	// A rename of the default in one write.
	s.Models[1].Name, s.DefaultModel = "sonnet-5", "sonnet-5"
	if err := SetAgentModels("claude", s); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []AgentSettings{
		{Models: []AgentModel{{"opus", "x"}}, HasModels: true, DefaultModel: "haiku", HasDefault: true},
		{Models: []AgentModel{{"two words", "x"}}, HasModels: true},
		{Models: []AgentModel{{"opus", strings.Repeat("x", MaxModelAbout+1)}}, HasModels: true},
		{Models: []AgentModel{{"opus", "a\nb"}}, HasModels: true},
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
	// No models and no default: the agent offers none, runs its own.
	if err := SetAgentModels("codex", AgentSettings{HasModels: true, HasDefault: true}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "[agents.codex]\nmodels = []\ndefault_model = \"\"\n") {
		t.Fatalf("codex:\n%s", data)
	}
	if err := SetAgentModels("claude", AgentSettings{}); err != nil {
		t.Fatal(err)
	}
	c, _ = Load()
	if got := c.Agent("claude"); got.HasModels || got.HasDefault {
		t.Fatalf("after reset: %+v", got)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "# keep\n\n[projects.demo]") {
		t.Fatalf("reset kept:\n%s", data)
	}
	// The array-of-tables form is the user's to edit by hand.
	write(t, "[[agents.claude.models]]\nname = \"opus\"\nabout = \"big\"\n")
	if err := SetAgentModels("claude", AgentSettings{Models: []AgentModel{{"a", "b"}}, HasModels: true}); !errors.Is(err, ErrForm) {
		t.Fatalf("array of tables: %v", err)
	}
}
