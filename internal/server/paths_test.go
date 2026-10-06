package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSocketPath(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{"home", Env{TerminatrHome: "/Users/me/.terminatr", UID: 501}, "/Users/me/.terminatr/run/tm.sock"},
		{"xdg", Env{TerminatrHome: "/home/me/.terminatr", XDGRuntimeDir: "/run/user/1000", UID: 1000}, "/run/user/1000/terminatr/tm.sock"},
		{"override", Env{TerminatrSocket: "/tmp/t.sock", TerminatrHome: "/h"}, "/tmp/t.sock"},
	}
	for _, c := range cases {
		got, err := SocketPath(c.env)
		if err != nil || got != c.want {
			t.Errorf("%s: SocketPath = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := SocketPath(Env{TerminatrSocket: "/" + strings.Repeat("y", 120)}); err == nil {
		t.Error("an overlong TERMINATR_SOCKET must be refused, not truncated")
	}
}

// TestSocketPathLongHomes: homes too long for a socket under them fall back
// to /tmp, one run directory per home, so they never share a server.
func TestSocketPathLongHomes(t *testing.T) {
	long := func(name string) string { return "/Users/" + strings.Repeat("x", 100) + "/" + name }
	a, errA := SocketPath(Env{TerminatrHome: long("a"), UID: 501})
	b, errB := SocketPath(Env{TerminatrHome: long("b"), UID: 501})
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if ok := regexp.MustCompile(`^/tmp/terminatr-501-[0-9a-f]{8}/tm\.sock$`); !ok.MatchString(a) || !ok.MatchString(b) {
		t.Fatalf("fallback sockets %q, %q", a, b)
	}
	if a == b {
		t.Fatalf("two homes share the socket %s", a)
	}
	if again, _ := SocketPath(Env{TerminatrHome: long("a") + "/", UID: 501}); again != a {
		t.Fatalf("the same home gives %s and %s", a, again)
	}
	if len(a) > maxSocketPath {
		t.Fatalf("%s is over %d bytes", a, maxSocketPath)
	}
	// An XDG run dir that is too long falls back per home too.
	x, _ := SocketPath(Env{TerminatrHome: "/home/me/.terminatr", XDGRuntimeDir: "/run/" + strings.Repeat("r", 100), UID: 1000})
	if !strings.HasPrefix(x, "/tmp/terminatr-1000-") {
		t.Fatalf("long XDG run dir: %s", x)
	}
}

// TestResolveStable: a home reached through a symlink resolves to the same
// path, before and after the home itself exists.
func TestResolveStable(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	want := resolve(filepath.Join(real, "home"))
	if got := resolve(filepath.Join(link, "home")); got != want {
		t.Fatalf("before mkdir: %s, want %s", got, want)
	}
	os.Mkdir(filepath.Join(real, "home"), 0o700)
	if got := resolve(filepath.Join(link, "home")); got != want {
		t.Fatalf("after mkdir: %s, want %s", got, want)
	}
}
