package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseArgs(t *testing.T) {
	o := parseArgs([]string{
		"--plugin-dir", "/rt/claude-plugin", "--settings=/rt/s.json",
		"--session-id", "abc", "", "", "--allowedTools", "kick", "--model", "haiku",
		"--dangerously-skip-permissions", "--", "do", "it",
	})
	if !reflect.DeepEqual(o.pluginDirs, []string{"/rt/claude-plugin"}) || !reflect.DeepEqual(o.settings, []string{"/rt/s.json"}) {
		t.Fatalf("dirs/settings: %+v", o)
	}
	if o.sessionID != "abc" || o.model != "haiku" || !o.yolo || o.resume != nil {
		t.Fatalf("flags: %+v", o)
	}
	if o.prompt != "kick do it" {
		t.Fatalf("prompt %q", o.prompt)
	}
	o = parseArgs([]string{"--resume", ""})
	if o.resume == nil || *o.resume != "" {
		t.Fatalf("resume empty: %+v", o)
	}
	o = parseArgs([]string{"--resume", "--model", "m"})
	if o.resume == nil || *o.resume != "" || o.model != "m" {
		t.Fatalf("resume no value: %+v", o)
	}
	o = parseArgs([]string{"--resume", "id1", "--version"})
	if o.resume == nil || *o.resume != "id1" || !o.version {
		t.Fatalf("resume id: %+v", o)
	}
}

func TestDecodeKeys(t *testing.T) {
	var got []key
	emit := func(k key) { got = append(got, k) }
	rest := decodeKeys([]byte("a\x1b[27u\x1b[13u\x1b[99;5u\x1b[117;5u\x1b[A\x1b[1;2B\x1b[I\x1b[<35;1;2M\x1b[200~x\ny\x1b[201~é\r\x7f"), false, emit)
	if len(rest) != 0 {
		t.Fatalf("rest %q", rest)
	}
	want := []keyKind{kRune, kEsc, kEnter, kCtrlC, kCtrlU, kUp, kDown, kPaste, kRune, kEnter, kBackspace}
	var kinds []keyKind
	for _, k := range got {
		kinds = append(kinds, k.kind)
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	if got[7].text != "x\ny" || got[8].r != 'é' {
		t.Fatalf("paste %q rune %q", got[7].text, got[8].r)
	}
	got = nil
	if rest := decodeKeys([]byte("\x1b"), false, emit); len(rest) != 1 || len(got) != 0 {
		t.Fatal("a lone ESC must wait")
	}
	decodeKeys([]byte("\x1b"), true, emit)
	if len(got) != 1 || got[0].kind != kEsc {
		t.Fatalf("flushed ESC: %v", got)
	}
	got = nil
	if rest := decodeKeys([]byte("\x1b[200~abc"), true, emit); len(rest) == 0 || len(got) != 0 {
		t.Fatal("an unfinished paste must wait")
	}
}

func TestDenied(t *testing.T) {
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := denyDirs([]string{"Edit(/" + ro + "/**)", "Read(//x/**)"}, "/home", dir)
	if len(dirs) != 1 {
		t.Fatalf("dirs %v", dirs)
	}
	if !denied(filepath.Join(ro, "a", "b.txt"), dirs) || !denied(ro, dirs) {
		t.Fatal("paths in ro must be denied")
	}
	if denied(filepath.Join(dir, "rox.txt"), dirs) || denied(filepath.Join(dir, "x.txt"), dirs) {
		t.Fatal("paths outside ro must be allowed")
	}
}

func TestEmbeddedScriptsParse(t *testing.T) {
	names := embeddedScripts()
	if len(names) < 9 {
		t.Fatalf("scripts: %v", names)
	}
	for _, n := range names {
		if _, err := loadScript(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := parseScript([]byte("[[step]]\ndo = \"stream\"\ntxt = \"x\"\n")); err == nil {
		t.Error("an unknown key must fail")
	}
	if _, err := loadScript("../x"); err != errNoScript {
		t.Error("path names are not scripts")
	}
}

func TestProjectDirName(t *testing.T) {
	if got := projectDirName("/Users/me/.termalator/w.1"); got != "-Users-me--termalator-w-1" {
		t.Fatal(got)
	}
}
