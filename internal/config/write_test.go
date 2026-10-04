package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEdit(t *testing.T) {
	for _, tc := range []struct {
		name, in, table, key string
		value                any
		want                 string
	}{
		{"empty file", "", "projects.demo", "yolo", true,
			"[projects.demo]\nyolo = true\n"},
		{"replace keeps comments", "# mine\n[projects.demo]\n  yolo = false # careful\nauto_resolve = true\n", "projects.demo", "yolo", true,
			"# mine\n[projects.demo]\n  yolo = true # careful\nauto_resolve = true\n"},
		{"insert after the table's last setting", "[projects.demo]\nyolo = true\n\n# next\n[projects.other]\nyolo = false\n", "projects.demo", "start_threads", "auto",
			"[projects.demo]\nyolo = true\nstart_threads = \"auto\"\n\n# next\n[projects.other]\nyolo = false\n"},
		{"new table at the end", "[keys]\nprefix = \"ctrl+a\"", "projects.demo", "pr_followup", false,
			"[keys]\nprefix = \"ctrl+a\"\n\n[projects.demo]\npr_followup = false\n"},
		{"quoted header", "[ projects . \"demo\" ]\nyolo = false\n", "projects.demo", "yolo", true,
			"[ projects . \"demo\" ]\nyolo = true\n"},
		{"top level before the first table", "# settings\n\n[keys]\nprefix = \"ctrl+a\"\n", "", "default_agent", "pi",
			"default_agent = \"pi\"\n# settings\n\n[keys]\nprefix = \"ctrl+a\"\n"},
		{"top level replaced", "default_agent = \"claude\"\n[keys]\n", "", "default_agent", "pi",
			"default_agent = \"pi\"\n[keys]\n"},
		{"other table's key untouched", "[projects.other]\nyolo = false\n[projects.demo]\n", "projects.demo", "yolo", true,
			"[projects.other]\nyolo = false\n[projects.demo]\nyolo = true\n"},
		{"string escaped", "", "keys", "prefix", `ctrl+"`,
			"[keys]\nprefix = \"ctrl+\\\"\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Edit([]byte(tc.in), tc.table, tc.key, tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// TestEditOtherForms: a setting written as a dotted key or inline table
// is left alone, with ErrForm, and a broken file is never written over.
func TestEditOtherForms(t *testing.T) {
	for _, in := range []string{
		"projects.demo.yolo = false\n",
		"[projects]\ndemo = { yolo = false }\n",
	} {
		if _, err := Edit([]byte(in), "projects.demo", "yolo", true); !errors.Is(err, ErrForm) {
			t.Errorf("%q: err %v, want ErrForm", in, err)
		}
	}
	if _, err := Edit([]byte("[projects.demo\n"), "projects.demo", "yolo", true); err == nil {
		t.Error("a broken file was edited")
	}
}

func TestSetProject(t *testing.T) {
	write(t, "# my settings\n[projects.demo]\nyolo = false # not yet\n")
	path, _ := Path()
	os.Chmod(path, 0o640)
	if err := SetProject("demo", "yolo", true); err != nil {
		t.Fatal(err)
	}
	if err := SetProject("demo", "start_threads", StartAuto); err != nil {
		t.Fatal(err)
	}
	if err := SetProject("demo", "start_threads", "sometimes"); err == nil {
		t.Error("start_threads took a bad value")
	}
	if err := SetProject("demo", "nonsense", true); err == nil {
		t.Error("an unknown key was written")
	}
	for key, v := range map[string]any{"parallel_threads": 0, "auto_close_days": 366, "auto_close": "never"} {
		if err := SetProject("demo", key, v); err == nil {
			t.Errorf("%s took %v", key, v)
		}
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := c.Safety("demo")
	if !s.Yolo || s.StartThreads != StartAuto {
		t.Fatalf("got %+v", s)
	}
	data, _ := os.ReadFile(path)
	if want := "# my settings\n[projects.demo]\nyolo = true # not yet\nstart_threads = \"auto\"\n"; string(data) != want {
		t.Fatalf("file:\n%s", data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.toml.tmp*")); len(m) > 0 {
		t.Errorf("temp files left: %v", m)
	}
}

func TestDefaultAgent(t *testing.T) {
	write(t, "")
	if got := DefaultAgent("claude"); got != "claude" {
		t.Fatalf("unset: %q", got)
	}
	if err := Set("", "default_agent", "pi"); err != nil {
		t.Fatal(err)
	}
	if got := DefaultAgent("claude"); got != "pi" {
		t.Fatalf("set: %q", got)
	}
}
