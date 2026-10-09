package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// vectors are the shared test vectors, which hooks/guard.ts passes too.
type vectors struct {
	Tools map[string]Tool  `json:"tools"`
	Rules map[string]Rules `json:"rules"`
	Cases []struct {
		Rules   string         `json:"rules"`
		Tool    string         `json:"tool"`
		Input   map[string]any `json:"input"`
		Rule    *string        `json:"rule"`
		Summary string         `json:"summary"`
		Ask     bool           `json:"ask"`
	} `json:"cases"`
	Messages map[string]map[string]string `json:"messages"`
}

// loadVectors reads guard-vectors.ts: a TS module whose default export
// is JSON.
func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("../agent/claude/mod/tests/guard-vectors.ts")
	if err != nil {
		t.Fatal(err)
	}
	const mark = "\nexport default "
	i := bytes.Index(b, []byte(mark))
	if i < 0 {
		t.Fatalf("guard-vectors.ts: no %q", mark)
	}
	var v vectors
	if err := json.Unmarshal(b[i+len(mark):], &v); err != nil {
		t.Fatalf("guard-vectors.ts: %v", err)
	}
	if len(v.Cases) < 100 {
		t.Fatalf("guard-vectors.ts: only %d cases", len(v.Cases))
	}
	// The tools go with every rule set.
	for name, r := range v.Rules {
		r.Tools = v.Tools
		v.Rules[name] = r
	}
	return v
}

// TestVectors: the Go port refuses (or asks about) what guard.ts does,
// with the same rule, summary and sentence.
func TestVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Cases {
		r, ok := v.Rules[c.Rules]
		if !ok {
			t.Fatalf("no rules %q", c.Rules)
		}
		d := r.Judge(c.Tool, c.Input)
		var got, want string
		if d != nil {
			got = fmt.Sprintf("%s / %s / ask %t", d.Rule, d.Summary, d.Ask)
		}
		if c.Rule != nil {
			want = fmt.Sprintf("%s / %s / ask %t", *c.Rule, c.Summary, c.Ask)
		}
		if got != want {
			t.Errorf("%s %s %v: got %q, want %q", c.Rules, c.Tool, c.Input, got, want)
		}
	}
	probe := map[string][2]any{
		"force-push":    {"Bash", map[string]any{"command": "git push -f"}},
		"push-default":  {"Bash", map[string]any{"command": "git push origin master"}},
		"worktree-only": {"Write", map[string]any{"file_path": "/etc/x"}},
		"delete-branch": {"Bash", map[string]any{"command": "git branch -D x"}},
		"merge":         {"Bash", map[string]any{"command": "gh pr merge 1"}},
		"credentials":   {"Bash", map[string]any{"command": "gh auth token"}},
	}
	for role, msgs := range v.Messages {
		r := v.Rules["thread"]
		r.Role = role
		for rule, p := range probe {
			d := r.Judge(p[0].(string), p[1].(map[string]any))
			if d == nil || d.Message != msgs[rule] {
				t.Errorf("%s %s: got %+v, want %q", role, rule, d, msgs[rule])
			}
		}
	}
}

func TestCommands(t *testing.T) {
	got := Commands(`cd x && git push -f origin 'my branch'; echo "a $(gh pr merge 3) b" | cat`)
	want := [][]string{{"cd", "x"}, {"git", "push", "-f", "origin", "my branch"}, {"gh", "pr", "merge", "3"}, {"echo", "a $(gh pr merge 3) b"}, {"cat"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
	if got := Commands("cat < ~/.ssh/id_rsa"); !reflect.DeepEqual(got, [][]string{{"cat", "<", "~/.ssh/id_rsa"}}) {
		t.Errorf("got %q", got)
	}
}

func TestPatchPaths(t *testing.T) {
	p := "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** Update File: /w/b.go\n*** Move to: c/d.go\n@@\n-x\n+y\n*** Delete File: e \n*** End Patch"
	if got := PatchPaths(p); !reflect.DeepEqual(got, []string{"a.txt", "/w/b.go", "c/d.go", "e"}) {
		t.Errorf("got %q", got)
	}
}

// FuzzJudge: no command line panics the matcher.
func FuzzJudge(f *testing.F) {
	f.Add("git push -f origin 'x' && echo \"$(gh pr merge 1)\" `x` \\")
	f.Add("rm -rf ~/.terminatr/worktrees/../x; cat <~/.ssh/k")
	f.Add("& { git push -f }; $x = @\"\n$(gh pr merge 1)\n\"@ <# c #> 'a''b' \"`\"$(\" 2>&1 &")
	r := Rules{On: true, Role: "thread", Rules: []string{"force-push", "push-default", "worktree-only", "delete-branch", "merge", "credentials"},
		Home: "/h", Cwd: "/w", Writable: []string{"/w"}, Worktrees: "/h/wt", Protected: []string{"main"}, Secrets: []string{"/h/.ssh"},
		Tools: map[string]Tool{"Bash": {Kind: KindShell, Fields: []string{"command"}}, "PowerShell": {Kind: KindShell, Syntax: SyntaxPowerShell, Fields: []string{"command"}},
			"apply_patch": {Kind: KindPatch, Fields: []string{"command"}}, "Glob": {Kind: KindGlob, Fields: []string{"pattern"}}}}
	f.Fuzz(func(t *testing.T, s string) {
		r.Judge("Bash", map[string]any{"command": s})
		r.Judge("PowerShell", map[string]any{"command": s})
		r.Judge("apply_patch", map[string]any{"command": s})
		r.Judge("Glob", map[string]any{"pattern": s})
	})
}
