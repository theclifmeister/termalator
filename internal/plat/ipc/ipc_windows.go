package ipc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioAFUnixGetPeerPID is SIO_AF_UNIX_GETPEERPID (afunix.h):
// _WSAIOR(IOC_VENDOR, 256), the pid of the process at the other end.
const sioAFUnixGetPeerPID = 0x58000100

// Listen listens on a, connectable by this user only: the socket file
// gets a protected DACL that grants this user's SID alone (AF_UNIX on
// Windows checks it at connect; a chmod would change nothing). Until it
// is set, the directory's ACL applies, as on Unix before the chmod.
// Nothing may be at a: the caller removes a stale socket first, when it
// knows it is stale. Closing the listener leaves the socket file, which
// a successor may already have replaced; the owner removes it.
func Listen(a Addr) (net.Listener, error) {
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: string(a), Net: "unix"})
	if err != nil {
		return nil, err
	}
	ln.SetUnlinkOnClose(false)
	if err := ownerOnly(string(a)); err != nil {
		ln.Close()
		return nil, fmt.Errorf("restrict %s to this user: %w", a, err)
	}
	return ln, nil
}

// ownerOnly replaces path's DACL with one that grants this user full
// access and nobody else anything. The socket is a reparse point that
// can't be followed, so it is opened as one.
func ownerOnly(path string) error {
	sid, err := userSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// Dial connects to a, ours or a peer's (an agent's own socket, as named
// in its status file). ctx bounds the connect only.
func Dial(ctx context.Context, a Addr) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", string(a))
}

// IsAbsent reports whether a Dial error means nothing listens at the
// address: no socket there, or one nobody accepts on (left by a process
// that died). Windows refuses both with WSAECONNREFUSED, and a socket
// in a missing directory with WSAENETDOWN (ENOENT on Unix).
func IsAbsent(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, windows.WSAENETDOWN) ||
		errors.Is(err, fs.ErrNotExist)
}

// PeerOf returns the process at the other end of c, a connection
// accepted from Listen. SameUser compares the peer's token user SID with
// ours.
func PeerOf(c net.Conn) (Peer, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return Peer{}, fmt.Errorf("peer of %T: not a socket", c)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var pid uint32
	var serr error
	if err := raw.Control(func(fd uintptr) {
		var n uint32
		serr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0,
			(*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &n, nil, 0)
	}); err != nil {
		return Peer{}, err
	}
	if serr != nil {
		return Peer{}, fmt.Errorf("peer pid: %w", serr)
	}
	theirs, err := processSID(pid)
	if err != nil {
		return Peer{}, fmt.Errorf("peer %d: %w", pid, err)
	}
	ours, err := userSID()
	if err != nil {
		return Peer{}, err
	}
	return Peer{PID: int(pid), SameUser: theirs.Equals(ours)}, nil
}

// userSID is this process's user SID.
var userSID = sync.OnceValues(func() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
})

// processSID is the user SID of process pid's token.
func processSID(pid uint32) (*windows.SID, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return nil, err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}

// ShortDir is a short directory to make a socket directory in, for a run
// directory whose usual place is too long: the user's own temp dir
// (%TEMP%, e.g. C:\Users\<user>\AppData\Local\Temp).
func ShortDir() string { return os.TempDir() }
