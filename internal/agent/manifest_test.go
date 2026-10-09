package agent

import "testing"

func TestHookexecFunc(t *testing.T) {
	f := funcs["hookexec"].(func(string, ...string) (string, error))
	got, err := f(`C:\Program Files\tm.exe`, "hook", "--agent", "claude")
	want := `"command":"C:\\Program Files\\tm.exe","args":["hook","--agent","claude"]`
	if err != nil || got != want {
		t.Fatalf("hookexec = %s, %v; want %s", got, err, want)
	}
	if got, _ := f("/bin/tm"); got != `"command":"/bin/tm","args":[]` {
		t.Fatalf("hookexec without args = %s", got)
	}
}

func TestPosixPath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\a\p`: "/c/Users/a/p",
		`d:/x`:         "/d/x",
		`C:`:           "/c",
		"/home/u":      "/home/u",
	} {
		if got := posixPath(in); got != want {
			t.Errorf("posixPath(%q) = %q, want %q", in, got, want)
		}
	}
}
