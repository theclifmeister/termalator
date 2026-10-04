package mdfile

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		in, front, body string
		err             bool
	}{
		{"# hi\n", "", "# hi\n", false},
		{"+++\na = 1\n+++\n# hi\n", "a = 1\n", "# hi\n", false},
		{"+++\n+++\n", "", "", false},
		{"+++\na = 1\n+++", "a = 1\n", "", false},
		{"+++\r\na = 1\r\n+++\r\nbody", "a = 1\r\n", "body", false},
		{"+++\na = 1\n", "", "", true},
		{"", "", "", false},
	}
	for _, c := range cases {
		front, body, err := Split([]byte(c.in))
		if (err != nil) != c.err {
			t.Errorf("Split(%q) err = %v", c.in, err)
			continue
		}
		if string(front) != c.front || (!c.err && string(body) != c.body) {
			t.Errorf("Split(%q) = %q, %q; want %q, %q", c.in, front, body, c.front, c.body)
		}
	}
}

type fm struct {
	Name  string   `toml:"name"`
	Repos []string `toml:"repos"`
}

func TestWriteRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "P.md")
	if err := Write(p, fm{Name: "demo", Repos: []string{"/a"}}, []byte("# Body\n")); err != nil {
		t.Fatal(err)
	}
	var got fm
	body, err := Read(p, &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || len(got.Repos) != 1 || string(body) != "# Body\n" {
		t.Fatalf("got %+v %q", got, body)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "+++\nname = \"demo\"\n") {
		t.Fatalf("unexpected file:\n%s", raw)
	}
}

func TestUpdateConcurrent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.md")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Update(p, func(old []byte) ([]byte, error) {
				return append(old, 'x'), nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	raw, _ := os.ReadFile(p)
	if len(raw) != 20 {
		t.Fatalf("lost updates: %q", raw)
	}
}

func TestUpdateUnchangedKeepsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.md")
	os.WriteFile(p, []byte("same"), 0o644)
	before, _ := os.Stat(p)
	if err := Update(p, func(old []byte) ([]byte, error) { return old, nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged update replaced the file")
	}
}
