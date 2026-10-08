//go:build unix && realclaude

package e2e

// TestRealModShots captures the Claude mod's surfaces as the real Claude
// draws them (T93): a thread's band and status entry, the CI toast, and
// the coordinator's /tm pane, docked beside a wide window and inline in
// a narrow one. Like TestShots it saves each screen as text and as the
// styled frame into $TM_SHOTS, checks nothing, and runs only when
// TM_SHOTS names a folder. It needs a logged-in claude (the user's HOME,
// as the rest of this suite) and costs a few cents: haiku, a coordinator
// greeting and a thread's first steps. The server is the test's own
// (its own TERMINATR_HOME and socket), never the user's.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealModShots(t *testing.T) {
	dir := os.Getenv("TM_SHOTS")
	if dir == "" {
		t.Skip("set TM_SHOTS to a folder to capture the mod's screens")
	}
	os.MkdirAll(dir, 0o755)
	env := realAgentEnv(t, "claude", nil)
	env.Setenv("ANTHROPIC_MODEL", "haiku") // the coordinator too
	env.Setenv("TERMINATR_TICK_SWEEP", "1s")
	env.Setenv("TERMINATR_TICK_PR", "1s")
	state := fakeGH(t, env)
	os.MkdirAll(env.Home, 0o700)
	if err := os.WriteFile(filepath.Join(env.Home, "config.toml"), []byte("[mods]\nenabled = true\nband = true\npane = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A project with a repository and a task in every state.
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"},
	} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	var p struct{ Slug, Dir string }
	if err := json.Unmarshal([]byte(env.MustCLI("project", "new", "demo", "--repo", repo, "--json")), &p); err != nil {
		t.Fatal(err)
	}
	pc := func(args ...string) { env.MustCLI(append(args, "--project", p.Slug)...) }
	pc("task", "add", "Say hello", "--status", "ready", "--step", "Read the brief", "--step", "Say hello", "--step", "Report")
	pc("task", "add", "Ship the release", "--step", "Tag", "--step", "Publish")
	pc("task", "status", "T2", "review")
	pc("task", "add", "Get the API keys", "--status", "blocked")
	pc("task", "add", "Write the docs")

	sizes := []shotSize{{"normal", 120, 36}, {"wide", 200, 50}}
	shots := map[string]*shooter{}
	for _, sz := range sizes {
		w := env.Window(sz.cols, sz.rows, "--own")
		shots[sz.name] = &shooter{t: t, dir: dir, size: sz.name, w: w}
		w.WaitFor("SESSIONS", wait)
	}
	// settle lets a session's turn run to its end, allowing what it asks
	// to run once, as the user would.
	settle := func(sess *Session) {
		Poll(3*realWait, func() bool {
			i, _ := env.Info(sess)
			if i.State == "blocked" && i.Reason == "permission" {
				time.Sleep(time.Second)
				env.Keys(sess, "1")
			}
			return i.State == "idle"
		})
	}
	// answerTrust accepts Claude's trust dialog for the session, as the
	// user would, when it shows.
	answerTrust := func(s *shooter, sess *Session) {
		Poll(realWait, func() bool {
			i, _ := env.Info(sess)
			return i.State == "idle" || i.Reason == "trust"
		})
		if i, _ := env.Info(sess); i.Reason == "trust" {
			time.Sleep(time.Second) // keys within ~0.5 s of the dialog are dropped
			s.w.Key(keyDown)        // the default is "No, exit"
			time.Sleep(200 * time.Millisecond)
			s.w.Key(Enter)
		}
		settle(sess)
	}
	openCoordinator := func(s *shooter) {
		if x, y := s.at(" coordinator", 1); y >= 0 {
			s.w.DoubleClick(x+3, y)
		}
	}

	// The coordinator, wide: the /tm pane docks beside it once the
	// window gives Claude 144 columns (the info panel hidden).
	wide := shots["wide"]
	openCoordinator(wide)
	coord := coordinatorOf(t, env, p.Slug)
	answerTrust(wide, coord)
	wide.w.Prefix("|")
	time.Sleep(8 * time.Second)
	wide.shot("mod-coordinator-pane")

	// Normal: the coordinator's /tm, inline.
	normal := shots["normal"]
	openCoordinator(normal)
	time.Sleep(3 * time.Second)
	normal.w.Type("/tm")
	time.Sleep(500 * time.Millisecond)
	normal.w.Key(Enter)
	time.Sleep(4 * time.Second)
	normal.shot("mod-coordinator-pane-inline")

	// A thread on T1: its band and status entry, then the CI toast.
	pc("thread", "start", "--task", "T1", "--model", "haiku", "--approved-by-user", "Say hello")
	th := threadSession(t, env, p.Slug, "t-0001")
	settle(th)
	pc("task", "steps", "T1", "check", "1")
	setPR(t, state, `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","title":"Say hello","statusCheckRollup":[{"status":"IN_PROGRESS"}]}`)
	for _, sz := range sizes {
		s := shots[sz.name]
		s.w.Prefix("d")
		time.Sleep(time.Second)
		if x, y := s.at("T1 Say hello", 1); y >= 0 {
			s.w.DoubleClick(x+2, y)
		}
		time.Sleep(3 * time.Second)
		if sz.name == "wide" {
			s.w.Prefix("|") // the info panel off: the band at Claude's full width
		}
		settle(th)
		time.Sleep(5 * time.Second)
		s.shot("mod-thread-band")
	}
	setPR(t, state, `{"number":7,"url":"https://github.com/o/r/pull/7","state":"OPEN","title":"Say hello","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`)
	// The toast is on screen for a few seconds only: look at every window
	// together and shoot each the moment it shows, not one after the
	// other, or the first's wait outlasts the others' toast. Both windows
	// view the one session, and Claude draws the toast in the corner of
	// the last-attached window's size, so a smaller window may show none.
	seen := map[string]bool{}
	for deadline := time.Now().Add(30 * time.Second); len(seen) == 0 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, sz := range sizes {
			if s := shots[sz.name]; strings.Contains(s.w.Screen(), "checks passed") {
				seen[sz.name] = true
				s.shot("mod-thread-toast")
			}
		}
	}
	if len(seen) == 0 {
		t.Errorf("no \"checks passed\" toast in any window after 30 s")
	}
	for _, sz := range sizes {
		if !seen[sz.name] {
			shots[sz.name].shot("mod-thread-toast")
		}
	}
	env.MustCLI("session", "stop", th.ID)
	env.MustCLI("session", "stop", coord.ID)
	for _, s := range shots {
		s.w.Quit()
		s.w.WaitExit(wait)
	}
}
