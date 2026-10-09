package main

import "os"

// openTTY opens the console's input and screen buffer.
func openTTY() (in, out *os.File, err error) {
	if in, err = os.OpenFile("CONIN$", os.O_RDWR, 0); err != nil {
		return nil, nil, err
	}
	if out, err = os.OpenFile("CONOUT$", os.O_RDWR, 0); err != nil {
		in.Close()
		return nil, nil, err
	}
	return in, out, nil
}
