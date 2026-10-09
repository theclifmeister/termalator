//go:build windows

package proc

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	rstrtmgr           = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession = rstrtmgr.NewProc("RmStartSession")
	procRmRegister     = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList      = rstrtmgr.NewProc("RmGetList")
	procRmEndSession   = rstrtmgr.NewProc("RmEndSession")
)

// rmProcessInfo is RM_PROCESS_INFO.
type rmProcessInfo struct {
	PID              uint32
	StartTime        windows.Filetime
	AppName          [256]uint16
	ServiceShortName [64]uint16
	ApplicationType  int32
	AppStatus        uint32
	TSSessionID      uint32
	Restartable      int32
}

const (
	errorMoreData   = 234
	cchRMSessionKey = 32
)

// Holders lists the processes that hold any of paths open or loaded (the
// Restart Manager, which `explorer` shows as "the file is open in…"), this
// process excluded. Paths that don't exist are skipped.
func Holders(paths ...string) ([]Holder, error) {
	var files []*uint16
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		u, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return nil, err
		}
		files = append(files, u)
	}
	if len(files) == 0 {
		return nil, nil
	}
	var session uint32
	key := make([]uint16, cchRMSessionKey+1)
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil, fmt.Errorf("RmStartSession: %w", syscall.Errno(r))
	}
	defer procRmEndSession.Call(uintptr(session))
	if r, _, _ := procRmRegister.Call(uintptr(session), uintptr(len(files)), uintptr(unsafe.Pointer(&files[0])), 0, 0, 0, 0); r != 0 {
		return nil, fmt.Errorf("RmRegisterResources: %w", syscall.Errno(r))
	}
	infos := make([]rmProcessInfo, 8)
	for range 4 {
		needed, n, reasons := uint32(0), uint32(len(infos)), uint32(0)
		r, _, _ := procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&n)),
			uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons)))
		switch r {
		case 0:
			var out []Holder
			for _, in := range infos[:n] {
				if int(in.PID) == os.Getpid() {
					continue
				}
				out = append(out, Holder{
					PID:     int(in.PID),
					Name:    windows.UTF16ToString(in.AppName[:]),
					Service: windows.UTF16ToString(in.ServiceShortName[:]),
				})
			}
			return out, nil
		case errorMoreData:
			infos = make([]rmProcessInfo, needed+4)
		default:
			return nil, fmt.Errorf("RmGetList: %w", syscall.Errno(r))
		}
	}
	return nil, fmt.Errorf("RmGetList: the list keeps changing")
}
