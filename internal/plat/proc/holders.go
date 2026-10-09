package proc

import "fmt"

// Holder is a process that has a file open or loaded, so that a rename or
// delete of it fails.
type Holder struct {
	PID int
	// Name is the application's name as the system shows it ("Windows
	// Explorer", "tm.exe"); Service is set when it is a Windows service.
	Name, Service string
}

func (h Holder) String() string {
	s := fmt.Sprintf("%s (pid %d)", h.Name, h.PID)
	if h.Service != "" {
		s += ", service " + h.Service
	}
	return s
}
