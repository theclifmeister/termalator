package server

import (
	"strings"
	"testing"
)

func TestSocketPath(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{"home", Env{TermalatorHome: "/Users/me/.termalator", UID: 501}, "/Users/me/.termalator/run/tm.sock"},
		{"xdg", Env{TermalatorHome: "/home/me/.termalator", XDGRuntimeDir: "/run/user/1000", UID: 1000}, "/run/user/1000/termalator/tm.sock"},
		{"long home", Env{TermalatorHome: "/Users/" + strings.Repeat("x", 100) + "/.termalator", UID: 501}, "/tmp/termalator-501/tm.sock"},
		{"override", Env{TermalatorSocket: "/tmp/t.sock", TermalatorHome: "/h"}, "/tmp/t.sock"},
	}
	for _, c := range cases {
		got, err := SocketPath(c.env)
		if err != nil || got != c.want {
			t.Errorf("%s: SocketPath = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := SocketPath(Env{TermalatorSocket: "/" + strings.Repeat("y", 120)}); err == nil {
		t.Error("an overlong TERMALATOR_SOCKET must be refused, not truncated")
	}
}
