package models

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
)

// stub is an environment whose PATH has a made-up agent, gem, running
// script.
func stub(t *testing.T, script string) []string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gem"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin"}
}

// lister is gem's manifest, a JSON-RPC-ish agent answering a models
// request, and a home for the test; stub gives it a script.
func lister(t *testing.T, script string) (*agent.Manifest, []string) {
	t.Helper()
	t.Setenv("TERMINATR_HOME", t.TempDir())
	env := stub(t, script)
	m, err := agent.ParseManifest([]byte(`manifest_version = 1
name = "gem"
[identify]
version_args = ["--version"]
[launch]
command = "gem"
model_args = ["--model", "{{.Model}}"]
[list_models]
args = ["models"]
send = ['{"id":1,"method":"list"}']
timeout_seconds = 2
[list_models.models]
reply = { id = "1" }
path = "result.models"
name = "id"
description = "about"
default = { main = "true" }
[list_models.account]
reply = { id = "1" }
path = "result.account"
fingerprint = ["plan"]
label = ["plan"]
logged_out = { "result.account.plan" = "!*" }
`))
	if err != nil {
		t.Fatal(err)
	}
	return m, env
}

// answer is a script that prints version 2.0 for --version and, for
// the models request, reply.
func answer(reply string) string {
	return `if [ "$1" = "--version" ]; then echo "gem 2.0"; exit 0; fi
read line
echo "noise, not json"
echo '` + reply + `'
sleep 5
`
}

func TestRefresh(t *testing.T) {
	m, env := lister(t, answer(`{"id":1,"result":{"account":{"plan":"gold"},"models":[{"id":"alpha","about":"big","main":true},{"id":"beta"}]}}`))
	c, err := Refresh(context.Background(), m, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusOK || c.Version != "2.0" || c.Account != "gold" || len(c.Models) != 2 || !c.Models[0].Default {
		t.Fatalf("%+v", c)
	}
	if got, ok := Load("gem"); !ok || got.Fingerprint != c.Fingerprint {
		t.Fatalf("saved %+v", got)
	}
	// A refusal lasts while the account and version are the same.
	if err := MarkRefused("gem", "beta", "no access"); err != nil {
		t.Fatal(err)
	}
	if c, _ := Refresh(context.Background(), m, env); c.Refused["beta"].Reason != "no access" {
		t.Fatalf("refusal lost: %+v", c.Refused)
	}
	cat := Get(m, nil, true)
	if !cat.Known || len(cat.Models) != 1 || len(cat.Refused) != 1 {
		t.Fatalf("catalog %+v", cat)
	}
	var me *Error
	if err := cat.Check("beta", nil); !errors.As(err, &me) || me.Code != "model-refused" || !strings.Contains(me.Msg, "no access") {
		t.Fatalf("refused: %v", err)
	}
	if err := Unrefuse("gem", "beta"); err != nil || Get(m, nil, true).Check("beta", nil) != nil {
		t.Fatalf("unrefuse: %v", err)
	}

	// Another account: the refusal is forgotten.
	MarkRefused("gem", "beta", "no access")
	env2 := stub(t, answer(`{"id":1,"result":{"account":{"plan":"silver"},"models":[{"id":"beta"}]}}`))
	if c, _ := Refresh(context.Background(), m, env2); len(c.Refused) != 0 || c.Account != "silver" {
		t.Fatalf("new account kept the refusal: %+v", c)
	}

	// Logged out: no models, a reason.
	env3 := stub(t, answer(`{"id":1,"result":{"account":{},"models":[{"id":"alpha"}]}}`))
	if c, _ := Refresh(context.Background(), m, env3); c.Status != StatusLoggedOut || len(c.Models) != 0 {
		t.Fatalf("logged out: %+v", c)
	}
	if cat := Get(m, nil, true); cat.Known || cat.Reason != "logged out of gem" {
		t.Fatalf("logged out catalog: %+v", cat)
	}
}

func TestRefreshFailures(t *testing.T) {
	good := `{"id":1,"result":{"account":{"plan":"gold"},"models":[{"id":"alpha"}]}}`
	m, env := lister(t, answer(good))
	if _, err := Refresh(context.Background(), m, env); err != nil {
		t.Fatal(err)
	}
	// A timeout with the same version keeps the last answer, noted.
	slow := stub(t, `if [ "$1" = "--version" ]; then echo "gem 2.0"; exit 0; fi
sleep 30`)
	c, err := Refresh(context.Background(), m, slow)
	if err != nil || c.Status != StatusOK || c.Note == "" || len(c.Models) != 1 {
		t.Fatalf("timeout: %v %+v", err, c)
	}
	// Garbage or a crash leaves none: it may be another login's.
	bad := stub(t, `if [ "$1" = "--version" ]; then echo "gem 3.0"; exit 0; fi
echo '{"id":1,"result":{"models":"nope"}}'; sleep 5`)
	c, err = Refresh(context.Background(), m, bad)
	if err == nil || c.Status != StatusFailed || len(c.Models) != 0 {
		t.Fatalf("garbage: %v %+v", err, c)
	}
	if cat := Get(m, nil, true); cat.Known || !strings.HasPrefix(cat.Reason, "probe failed: ") {
		t.Fatalf("garbage catalog %+v", cat)
	}
	dead := stub(t, `echo "boom" >&2; exit 3`)
	if _, err := Refresh(context.Background(), m, dead); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("crash: %v", err)
	}
	// Not installed, or no lister: nothing is asked.
	if _, err := Refresh(context.Background(), m, []string{"PATH=/nowhere"}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("not installed: %v", err)
	}
	m.ListModels = nil
	if _, err := Refresh(context.Background(), m, env); !errors.Is(err, ErrNoLister) {
		t.Fatalf("no lister: %v", err)
	}
	if cat := Get(m, nil, true); cat.Known || !strings.Contains(cat.Reason, "can't list its models") {
		t.Fatalf("no lister catalog %+v", cat)
	}
	if cat := Get(m, nil, false); cat.Known || cat.Reason != "gem not installed" {
		t.Fatalf("not installed catalog %+v", cat)
	}
}

// TestCatalog: the user's settings over the agent's answer, the
// project's scope, and the launch default.
func TestCatalog(t *testing.T) {
	m, env := lister(t, answer(`{"id":1,"result":{"account":{"plan":"gold"},"models":[{"id":"alpha"},{"id":"beta"},{"id":"gamma"}]}}`))
	if _, err := Refresh(context.Background(), m, env); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(os.Getenv("TERMINATR_HOME"), "config.toml"), []byte("[agents.gem]\nhide = [\"gamma\"]\nadd = [\"mine\", \"beta\"]\ndefault_model = \"beta\"\n"), 0o600)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	c := Get(m, cfg, true)
	if got := strings.Join(c.Names(), " "); got != "alpha beta mine" {
		t.Fatalf("names %q", got)
	}
	if x, _ := c.Find("mine"); !x.Yours {
		t.Fatal("mine isn't the user's")
	}
	if x, _ := c.Find("beta"); x.Yours {
		t.Fatal("beta, listed, counted as the user's")
	}
	code := func(err error) string {
		var me *Error
		if errors.As(err, &me) {
			return me.Code
		}
		return ""
	}
	for model, want := range map[string]string{"alpha": "", "gamma": "unknown-model", "zeta": "unknown-model", "": ""} {
		if got := code(c.Check(model, nil)); got != want {
			t.Errorf("%q: %q, want %q", model, got, want)
		}
	}
	if got := code(c.Check("beta", []string{"alpha"})); got != "model-not-allowed" {
		t.Errorf("out of scope: %q", got)
	}
	if c.LaunchModel(nil) != "beta" || c.LaunchModel([]string{"alpha"}) != "" {
		t.Errorf("launch model %q %q", c.LaunchModel(nil), c.LaunchModel([]string{"alpha"}))
	}
	if got := Get(m, cfg, false).Check("alpha", nil); code(got) != "models-unknown" {
		t.Errorf("not installed: %v", got)
	}
}

// TestResolve: the agent a project runs, for each set of installed
// agents and setting.
func TestResolve(t *testing.T) {
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		installed []string
		setting   string
		name      string
		auto      bool
		code      string
	}{
		{nil, "", "", false, CodeNoAgent},
		{[]string{"codex"}, "", "codex", true, ""},
		{[]string{"claude"}, "", "claude", true, ""},
		{[]string{"claude", "codex"}, "", "", false, CodeNotChosen},
		{[]string{"claude", "codex"}, "codex", "codex", false, ""},
		{[]string{"codex"}, "claude", "", false, CodeNotInstalled},
		{[]string{"codex"}, "pi", "", false, CodeUnknownAgent},
		{nil, "claude", "", false, CodeNotInstalled},
	} {
		name, auto, err := Resolve(reg, c.installed, c.setting, "thread agent")
		code := ""
		var me *Error
		if errors.As(err, &me) {
			code = me.Code
		}
		if name != c.name || auto != c.auto || code != c.code {
			t.Errorf("%v %q: %q %v %v", c.installed, c.setting, name, auto, err)
		}
	}
	if err := NoAgent(reg); !strings.Contains(err.Error(), "Claude Code (claude), Codex (codex)") {
		t.Errorf("no agent: %v", err)
	}
}
