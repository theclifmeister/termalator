package plat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// forbidden are the imports only internal/plat may use.
var forbidden = []string{
	"syscall",
	"golang.org/x/sys/unix",
	"golang.org/x/sys/windows",
	"github.com/creack/pty",
}

// goos stands for a read of runtime.GOOS in the allow-list.
const goos = "runtime.GOOS"

// allowed are today's OS sites outside internal/plat (T161 REPORT §1.2),
// by file, with what each uses. Each later platform-layer task moves some
// into internal/plat and deletes their entries; the list ends empty. Never
// add to it: put the new OS code in internal/plat instead.
var allowed = map[string][]string{
	"internal/cli/attach.go":                  {"syscall"},
	"internal/cli/doctor.go":                  {goos},
	"internal/cli/server.go":                  {"syscall", goos},
	"internal/cli/servercli.go":               {goos},
	"internal/cli/update.go":                  {goos},
	"internal/doctor/doctor.go":               {goos},
	"internal/e2e/apps/fullscreen/main.go":    {"syscall", "golang.org/x/sys/unix"},
	"internal/e2e/apps/printer/main.go":       {"syscall", "golang.org/x/sys/unix"},
	"internal/e2e/apps/termquery/main.go":     {"golang.org/x/sys/unix"},
	"internal/e2e/env.go":                     {"syscall"},
	"internal/e2e/fakeagent/hooks.go":         {"syscall"},
	"internal/e2e/fakeagent/main.go":          {"syscall"},
	"internal/e2e/fakeagent/screen.go":        {"golang.org/x/sys/unix"},
	"internal/e2e/fakeagent/script.go":        {"syscall"},
	"internal/e2e/fakeagent/termios_bsd.go":   {"golang.org/x/sys/unix"},
	"internal/e2e/fakeagent/termios_linux.go": {"golang.org/x/sys/unix"},
	"internal/e2e/stale.go":                   {"syscall"},
	"internal/e2e/window.go":                  {"syscall"},
	"internal/pty/procargs_darwin.go":         {"golang.org/x/sys/unix"},
	"internal/pty/pty_unix.go":                {"syscall", "golang.org/x/sys/unix", "github.com/creack/pty"},
	"internal/server/client.go":               {"syscall", goos},
	"internal/server/clone_darwin.go":         {"golang.org/x/sys/unix"},
	"internal/server/daemon_other.go":         {"syscall"},
	"internal/server/daemon_unix.go":          {"syscall", "golang.org/x/sys/unix"},
	"internal/server/resolve.go":              {goos},
	"internal/server/server.go":               {goos},
	"internal/server/stop.go":                 {"syscall"},
	"internal/service/launchd.go":             {goos},
	"internal/session/proc_unix.go":           {"syscall"},
	"internal/update/update.go":               {goos},
}

// TestBoundary parses every non-test Go file of the module outside
// internal/plat, for every OS (build tags are ignored), and fails on a
// forbidden import or a runtime.GOOS read that the allow-list doesn't
// name, and on an allow-list entry that is no longer needed.
func TestBoundary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root: %v", err)
	}
	found := map[string][]string{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "node_modules" || name == "vendor" || name == "dist" ||
				rel == "internal/plat") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		if uses := osUses(f); len(uses) > 0 {
			found[rel] = uses
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range slices.Sorted(maps.Keys(found)) {
		for _, u := range found[rel] {
			if !slices.Contains(allowed[rel], u) {
				t.Errorf("%s uses %s: OS code belongs in internal/plat (docs: T161 REPORT §2.1)", rel, u)
			}
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(allowed)) {
		for _, u := range allowed[rel] {
			if !slices.Contains(found[rel], u) {
				t.Errorf("%s no longer uses %s: remove it from the allow-list", rel, u)
			}
		}
	}
}

// osUses lists the forbidden imports f has, then goos if it reads
// runtime.GOOS.
func osUses(f *ast.File) []string {
	var uses []string
	runtimeName := ""
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if slices.Contains(forbidden, p) && !slices.Contains(uses, p) {
			uses = append(uses, p)
		}
		if p == "runtime" {
			runtimeName = "runtime"
			if im.Name != nil {
				runtimeName = im.Name.Name
			}
		}
	}
	if runtimeName != "" && runtimeName != "_" {
		readsGOOS := false
		ast.Inspect(f, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == "GOOS" {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == runtimeName {
					readsGOOS = true
				}
			}
			return !readsGOOS
		})
		if readsGOOS {
			uses = append(uses, goos)
		}
	}
	return uses
}

// TestOSUses checks the detector on small files.
func TestOSUses(t *testing.T) {
	for _, c := range []struct {
		src  string
		want []string
	}{
		{`package p; import "os"; var _ = os.Args`, nil},
		{`package p; import "syscall"; var _ = syscall.Getpid`, []string{"syscall"}},
		{`package p; import u "golang.org/x/sys/unix"; var _ = u.Getpid`, []string{"golang.org/x/sys/unix"}},
		{`package p; import _ "github.com/creack/pty"`, []string{"github.com/creack/pty"}},
		{`package p; import "runtime"; var _ = runtime.GOOS`, []string{goos}},
		{`package p; import rt "runtime"; func f() bool { return rt.GOOS == "linux" }`, []string{goos}},
		{`package p; import "runtime"; var _ = runtime.GOARCH`, nil},
		{`package p; import ("runtime"; "syscall"); var _, _ = runtime.GOOS, syscall.Getpid`, []string{"syscall", goos}},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "x.go", c.src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := osUses(f); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.src, got, c.want)
		}
	}
}
