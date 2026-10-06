package ticker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameProjectState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticker.json")
	if err := RenameProjectState(path, "a", "b"); err != nil {
		t.Fatalf("no file: %v", err)
	}
	os.WriteFile(path, []byte(`{"threads":{"a/t-0001":{"reports":3},"ab/t-0001":{"reports":1}},"projects":{"a":{"completed":{"T1":"c1"}},"ab":{}}}`), 0o600)
	if err := RenameProjectState(path, "a", "b"); err != nil {
		t.Fatal(err)
	}
	tk := New(Options{State: path})
	st := tk.st
	if st.Threads["b/t-0001"] == nil || st.Threads["b/t-0001"].Reports != 3 || st.Threads["a/t-0001"] != nil || st.Threads["ab/t-0001"] == nil {
		t.Fatalf("threads %+v", st.Threads)
	}
	if st.Projects["b"] == nil || st.Projects["b"].Completed["T1"] != "c1" || st.Projects["a"] != nil || st.Projects["ab"] == nil {
		t.Fatalf("projects %+v", st.Projects)
	}

	// In a running ticker, between sweeps; a failed move keeps the memos.
	if err := tk.RenameProject("b", "c", func() error { return os.ErrExist }); err == nil || tk.st.Projects["b"] == nil {
		t.Fatalf("failed move: %v %+v", err, tk.st.Projects)
	}
	if err := tk.RenameProject("b", "c", func() error { return nil }); err != nil || tk.st.Projects["c"] == nil || tk.st.Threads["c/t-0001"] == nil {
		t.Fatalf("move: %v %+v", err, tk.st)
	}
}
