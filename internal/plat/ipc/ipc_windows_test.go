package ipc

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// Dial's errors with no socket at the address and with one nobody
// accepts on: Windows refuses both.
var errNoSocket, errRefused error = windows.WSAECONNREFUSED, windows.WSAECONNREFUSED

func sockDir(t *testing.T) string { return t.TempDir() }

// checkPrivate fails unless only this user can connect to a: a protected
// DACL with one entry, for this user's SID.
func checkPrivate(t *testing.T, a Addr) {
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(string(a)), windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := userSID()
	if err != nil {
		t.Fatal(err)
	}
	s := sd.String()
	if !strings.HasPrefix(s, "D:P") || strings.Count(s, "(") != 1 || !strings.Contains(s, "(A;;FA;;;"+sid.String()+")") {
		t.Fatalf("socket DACL %s, want only full access for %s", s, sid)
	}
}
