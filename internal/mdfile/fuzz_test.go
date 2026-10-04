package mdfile

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzSplit checks that Split never panics and never loses bytes: the
// input is exactly fence + front + fence + body.
func FuzzSplit(f *testing.F) {
	for _, s := range []string{"", "# hi\n", "+++\na = 1\n+++\nbody", "+++\r\n+++\r\n", "+++", "+++\n+++", "++\n", "+++\nx\n+++ \n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		front, body, err := Split(data)
		if err != nil {
			if !bytes.HasPrefix(data, []byte(fence)) {
				t.Fatalf("error without an opening fence: %v", err)
			}
			return
		}
		if front == nil {
			if !bytes.Equal(body, data) {
				t.Fatalf("no front matter, but body %q != input %q", body, data)
			}
			return
		}
		if !bytes.HasSuffix(data, body) {
			t.Fatalf("body %q is not the tail of %q", body, data)
		}
		head := data[:len(data)-len(body)]
		first, rest, _ := bytes.Cut(head, []byte("\n"))
		if string(bytes.TrimRight(first, "\r")) != fence || !bytes.HasPrefix(rest, front) {
			t.Fatalf("head %q doesn't start with fence + front %q", head, front)
		}
		closing := strings.TrimRight(strings.TrimSuffix(string(rest[len(front):]), "\n"), "\r")
		if closing != fence {
			t.Fatalf("closing fence %q", closing)
		}
	})
}

// FuzzJoinDecode checks that any front matter value and body survive a
// Join and a Decode unchanged.
func FuzzJoinDecode(f *testing.F) {
	f.Add("demo", "/a", "# Body\n")
	f.Add("", "", "")
	f.Add("+++", "\"quoted\"\n", "+++\nnot front\n+++\n")
	f.Add("ünï\tcode", "a\\b", "\r\n")
	f.Fuzz(func(t *testing.T, name, repo, body string) {
		data, err := Join(fm{Name: name, Repos: []string{repo}}, []byte(body))
		if err != nil {
			return // TOML can't encode it (e.g. invalid UTF-8); nothing written
		}
		var got fm
		b, err := Decode(data, &got)
		if err != nil {
			t.Fatalf("decode own output: %v\n%q", err, data)
		}
		if got.Name != name || len(got.Repos) != 1 || got.Repos[0] != repo || string(b) != body {
			t.Fatalf("lost content: %+v %q from %q %q %q", got, b, name, repo, body)
		}
	})
}
