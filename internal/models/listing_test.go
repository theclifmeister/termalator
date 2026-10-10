package models

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
)

// listing runs the built-in agent's lister over a recorded reply
// (testdata: real replies, account names removed).
func listing(t *testing.T, agentName, file string) agent.Listing {
	t.Helper()
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get(agentName)
	st := agent.ManifestOf(a).ListModels.NewListState()
	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var obj map[string]any
		if json.Unmarshal(sc.Bytes(), &obj) == nil {
			st.Take(obj)
		}
	}
	if !st.Done() {
		t.Fatalf("%s: replies not all in", file)
	}
	l, err := st.Listing()
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return l
}

func names(l agent.Listing, older bool) string {
	var out []string
	for _, m := range l.Models {
		if m.Older == older {
			n := m.Name
			if m.Default {
				n += "*"
			}
			out = append(out, n)
		}
	}
	return strings.Join(out, " ")
}

// TestListModelsRecorded reads what Claude Code 2.1.296 and Codex
// 0.162.0 answered for each kind of login.
func TestListModelsRecorded(t *testing.T) {
	sub := listing(t, "claude", "claude-subscription.jsonl")
	if sub.LoggedOut || sub.Fingerprint == "" || sub.Account != "Claude Max, firstParty" {
		t.Errorf("subscription account: %+v", sub)
	}
	if got := names(sub, false); got != "opus fable sonnet haiku" {
		t.Errorf("subscription current: %q", got)
	}
	if got := names(sub, true); !strings.HasPrefix(got, "claude-haiku-4-5-20251001 claude-sonnet-5 ") {
		t.Errorf("subscription older: %q", got)
	}
	if strings.Contains(names(sub, false)+names(sub, true), "default") {
		t.Error("claude's default sentinel is offered")
	}
	if m := sub.Models[0]; m.About() != "Opus 5.5: For complex work and everyday tasks" {
		t.Errorf("about %q", m.About())
	}

	out := listing(t, "claude", "claude-logged-out.jsonl")
	if !out.LoggedOut {
		t.Errorf("claude logged out: %+v", out)
	}
	if key := listing(t, "claude", "claude-api-key.jsonl"); key.LoggedOut || key.Fingerprint == sub.Fingerprint {
		t.Errorf("claude api key: %+v", key)
	}
	bed := listing(t, "claude", "claude-bedrock.jsonl")
	if bed.LoggedOut || bed.Account != "bedrock" || !strings.Contains(names(bed, false)+names(bed, true), "us.anthropic.") {
		t.Errorf("claude bedrock: %+v", bed)
	}

	cx := listing(t, "codex", "codex-chatgpt.jsonl")
	if cx.LoggedOut || cx.Account != "chatgpt, plus" || !strings.Contains(names(cx, false), "*") {
		t.Errorf("codex chatgpt: %+v %q", cx, names(cx, false))
	}
	if strings.Contains(names(cx, false), "codex-auto-review") {
		t.Error("codex: a hidden model is offered")
	}
	if cxo := listing(t, "codex", "codex-logged-out.jsonl"); !cxo.LoggedOut || cxo.Fingerprint == cx.Fingerprint {
		t.Errorf("codex logged out: %+v", cxo)
	}
}
