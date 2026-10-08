package agent_test

// Screen fixtures: real screens of an agent, captured in a tm pane, in
// testdata/<agent>/*.screen, each with the rule that must decide it.
// Every screen rule of an agent with fixtures must be exercised by one,
// so a rule edit that breaks a real screen, or a rule nobody captured,
// fails here (T105).

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/detect"
)

// fixture is one .screen file:
//
//	# source: where and how it was captured
//	# want: the rule that decides it ("none" when no rule may match)
//	# also: the other rules that match too, space-separated
//	# dim: text drawn faint, blanked for skip_dim rules (repeatable)
//	---
//	the screen's rows
type fixture struct {
	name, source, want string
	also, dim          []string
	rows               []string
}

func readFixtures(t *testing.T, dir string) []fixture {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.screen"))
	if len(files) == 0 {
		t.Fatalf("no fixtures in %s", dir)
	}
	var out []fixture
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		head, body, ok := strings.Cut(string(b), "\n---\n")
		if !ok {
			t.Fatalf("%s: no --- line after the header", f)
		}
		fx := fixture{name: filepath.Base(f), rows: strings.Split(strings.TrimSuffix(body, "\n"), "\n")}
		for _, l := range strings.Split(head, "\n") {
			k, v, ok := strings.Cut(strings.TrimPrefix(l, "# "), ": ")
			if !ok || !strings.HasPrefix(l, "# ") {
				t.Fatalf("%s: bad header line %q", f, l)
			}
			switch k {
			case "source":
				fx.source = v
			case "want":
				fx.want = v
			case "also":
				fx.also = strings.Fields(v)
			case "dim":
				fx.dim = append(fx.dim, v)
			default:
				t.Fatalf("%s: unknown header %q", f, k)
			}
		}
		if fx.source == "" || fx.want == "" {
			t.Fatalf("%s: source and want are required", f)
		}
		out = append(out, fx)
	}
	return out
}

func (fx fixture) screen() detect.Screen {
	s := detect.Screen{Rows: fx.rows}
	if len(fx.dim) > 0 {
		s.NoDim = make([]string, len(fx.rows))
		for i, r := range fx.rows {
			for _, d := range fx.dim {
				r = strings.ReplaceAll(r, d, strings.Repeat(" ", len([]rune(d))))
			}
			s.NoDim[i] = r
		}
	}
	return s
}

func TestScreenFixtures(t *testing.T) {
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := filepath.Glob(filepath.Join("testdata", "*"))
	if len(dirs) == 0 {
		t.Fatal("no testdata/<agent> directories")
	}
	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			a, ok := reg.Get(name)
			if !ok {
				t.Fatalf("testdata/%s: no such agent", name)
			}
			e, err := detect.New(a.Rules())
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, fx := range readFixtures(t, dir) {
				best, all := e.Eval(fx.screen())
				got := "none"
				if best != nil {
					got = best.Rule
				}
				var gotAll []string
				for _, m := range all {
					if m.Rule != got {
						gotAll = append(gotAll, m.Rule)
					}
					seen[m.Rule] = true
				}
				sort.Strings(gotAll)
				wantAll := slices.Clone(fx.also)
				sort.Strings(wantAll)
				if got != fx.want || !slices.Equal(gotAll, wantAll) {
					t.Errorf("%s (%s): decided by %s, also %v; want %s, also %v", fx.name, fx.source, got, gotAll, fx.want, wantAll)
				}
			}
			for _, r := range a.Rules() {
				if !seen[r.ID] {
					t.Errorf("rule %s matches none of the fixtures in %s", r.ID, dir)
				}
			}
		})
	}
}

// TestCodexAnswers: the dialogs tm answers itself, with the option the
// user chose for each (Continue without trusting, Skip), and the
// fixtures still number those options so.
func TestCodexAnswers(t *testing.T) {
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("codex")
	keys := map[string]string{}
	for _, r := range a.Rules() {
		if r.Keys != "" {
			keys[r.ID] = r.Keys
		}
	}
	if want := map[string]string{"hooks-review": "3", "update-available": "2"}; !maps.Equal(keys, want) {
		t.Fatalf("keys %q, want %q", keys, want)
	}
	pick := map[string]string{"hooks-review": "3. Continue without trusting", "update-available": "2. Skip\n"}
	for _, fx := range readFixtures(t, filepath.Join("testdata", "codex")) {
		if p, ok := pick[fx.want]; ok && !strings.Contains(strings.Join(fx.rows, "\n")+"\n", p) {
			t.Errorf("%s: no %q option for the keys to pick", fx.name, p)
		}
	}
}
