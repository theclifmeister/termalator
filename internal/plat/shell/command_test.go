package shell

import (
	"slices"
	"testing"
)

func TestBatchLine(t *testing.T) {
	line, env, err := batchLine(`C:\Windows\System32\cmd.exe`, `C:\x y\az.cmd`,
		[]string{"rest", "https://h/p?a=1&b=2|3>4^5", "100%", "plain"})
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Windows\System32\cmd.exe" /d /s /c ""C:\x y\az.cmd" "rest" "https://h/p?a=1&b=2|3>4^5" "%TM_ARG2%" "plain""`
	if line != want {
		t.Errorf("line\n got %s\nwant %s", line, want)
	}
	if !slices.Equal(env, []string{"TM_ARG2=100%"}) {
		t.Errorf("env %q", env)
	}
	for _, bad := range []string{`a"b`, "a\nb"} {
		if _, _, err := batchLine("cmd", "x.cmd", []string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
