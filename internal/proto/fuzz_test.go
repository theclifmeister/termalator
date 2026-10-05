package proto

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzReadFrame feeds arbitrary bytes to the frame reader. It must never
// panic or allocate past MaxFrame, and a frame it accepts must re-encode to
// the same bytes.
func FuzzReadFrame(f *testing.F) {
	var b bytes.Buffer
	WriteFrame(&b, FrameOutput, []byte("hello"))
	f.Add(b.Bytes())
	f.Add([]byte{byte(FrameResize), 0, 0, 0, 4, 0, 120, 0, 40})
	f.Add([]byte{byte(FrameOutput), 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		for {
			typ, payload, err := ReadFrame(r, nil)
			if err != nil {
				return
			}
			if len(payload) > MaxFrame {
				t.Fatalf("payload of %d bytes passed MaxFrame", len(payload))
			}
			var out bytes.Buffer
			if err := WriteFrame(&out, typ, payload); err != nil {
				t.Fatal(err)
			}
			typ2, p2, err := ReadFrame(&out, nil)
			if err != nil || typ2 != typ || !bytes.Equal(p2, payload) {
				t.Fatalf("round trip changed the frame")
			}
			if typ == FrameResize {
				ParseSize(payload) // must not panic
			}
		}
	})
}

// FuzzHello decodes arbitrary hello lines and checks them against a
// server hello. Neither may panic.
func FuzzHello(f *testing.F) {
	f.Add([]byte(`{"protocol":1,"version":"v0.1.0","build":"b","kind":"attach"}`))
	f.Add([]byte(`{"protocol":-1,"kind":"control"}`))
	f.Add([]byte(`null`))
	server := Hello{Protocol: Protocol, Version: "v0.1.0", Build: "b", Bin: "/bin/tm"}
	f.Fuzz(func(t *testing.T, line []byte) {
		var h Hello
		if json.Unmarshal(line, &h) != nil {
			return
		}
		_ = Check(h, server)
	})
}
