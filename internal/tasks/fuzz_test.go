package tasks

import (
	"reflect"
	"strings"
	"testing"
)

// FuzzParse checks the TASKS.md parser: it never panics, and whatever it
// accepts renders to a file that parses back to the same tasks (no content
// lost) and renders identically again (canonical form is stable).
func FuzzParse(f *testing.F) {
	f.Add(specExample)
	f.Add("")
	f.Add("# Tasks\n")
	f.Add("+++\nnext_id = 3\n+++\n### T1 A\nstatus: open\n\nnotes\n\n- [ ] a\n- [x] b\n")
	f.Add("### T2 B · c\nstatus: blocked · owner: me · thread: t-1\n- [ ] only step\n")
	f.Add("### T3 C\nstatus: ready\n\n#### sub\n- [ ] in notes\n\ntext\n\n- [X] step\n")
	f.Add("+++\n+++\n### T1 x\r\nstatus: done\r\n\r\nwin\r\n")
	f.Add("### T1 0\nstatus:done\n0\r") // a stray CR on the last line
	f.Fuzz(func(t *testing.T, in string) {
		b, err := Parse([]byte(in))
		if err != nil {
			return
		}
		out := b.Render()
		b2, err := Parse(out)
		if err != nil {
			t.Fatalf("rendered board doesn't parse: %v\ninput:\n%q\nrendered:\n%s", err, in, out)
		}
		if !sameTasks(b, b2) {
			t.Fatalf("content changed in a round trip\ninput: %q\nrendered:\n%s\nbefore: %s\nafter:  %s", in, out, dump(b), dump(b2))
		}
		if b2.NextID != b.NextID {
			t.Fatalf("next_id %d became %d", b.NextID, b2.NextID)
		}
		if again := b2.Render(); string(again) != string(out) {
			t.Fatalf("render not stable:\n%s\n---\n%s", out, again)
		}
		arch := b.RenderArchive()
		b3, err := Parse(arch)
		if err != nil || !sameTasks(b, b3) {
			t.Fatalf("archive round trip: %v\n%s", err, arch)
		}
	})
}

func sameTasks(a, b *Board) bool {
	a.SortByID()
	b.SortByID()
	if len(a.Tasks) != len(b.Tasks) {
		return false
	}
	for i := range a.Tasks {
		x, y := *a.Tasks[i], *b.Tasks[i]
		if len(x.Steps) == 0 && len(y.Steps) == 0 {
			x.Steps, y.Steps = nil, nil
		}
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

func dump(b *Board) string {
	var s []string
	for _, t := range b.Tasks {
		s = append(s, strings.ReplaceAll(Detail(t), "\n", "⏎"))
	}
	return strings.Join(s, " | ")
}

// FuzzStoreInputs checks that whatever a caller passes as title, notes,
// owner or step, either the input validation refuses it or it survives a
// write and a re-read unchanged.
func FuzzStoreInputs(f *testing.F) {
	f.Add("Fix login", "Users land on /home", "me", "Reproduce")
	f.Add("x", "## Done", "", "a")
	f.Add("t", "a\n- [ ] b", "o·x", "s")
	f.Add("t", "  lead\n\ntrail  ", "", " pad ")
	f.Add("### T1 t", "+++\nnext_id = 9\n+++", "", "- [ ] x")
	f.Add("0", "0\r", "", "0") // a lone CR in notes
	f.Fuzz(func(t *testing.T, title, notes, owner, step string) {
		s := &Store{Dir: t.TempDir()}
		res, err := s.Add(human, []NewTask{{Title: title, Notes: notes, Owner: owner, Steps: []string{step}}})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Result != "created" {
			return // refused by validation
		}
		got, err := s.Get(1)
		if err != nil {
			t.Fatalf("re-read: %v", err)
		}
		wantNotes, _ := checkNotes(notes)
		if got.Title != strings.TrimSpace(title) || got.Notes != wantNotes || got.Owner != strings.TrimSpace(owner) ||
			len(got.Steps) != 1 || got.Steps[0].Text != strings.TrimSpace(step) {
			t.Fatalf("lost content: %+v from %q %q %q %q", got, title, notes, owner, step)
		}
	})
}
