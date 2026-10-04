package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/theclifmeister/termalator/internal/proto"
)

// FuzzControlDecode runs arbitrary bytes through everything the server
// decodes from a client before it acts: NDJSON line framing, the hello and
// proto.Check, the attach request, control requests and every method's
// params. None of it may panic, whatever a peer sends.
func FuzzControlDecode(f *testing.F) {
	f.Add([]byte(`{"protocol":1,"version":"dev","build":"b","kind":"control"}` + "\n" +
		`{"id":1,"method":"session.start","params":{"argv":["/bin/sh"],"cwd":"/","cols":80,"rows":24}}` + "\n"))
	f.Add([]byte(`{"protocol":-1,"kind":"attach"}` + "\n" + `{"attach":{"session":"s-1","cols":80}}` + "\n"))
	f.Add([]byte(`{"id":"x","method":7,"params":[1,2]}` + "\n"))
	f.Add([]byte("\n\n{\"id\":1e400}\n"))
	s := &Server{build: "test", opts: Options{Bin: "/bin/tm"}}
	f.Fuzz(func(t *testing.T, data []byte) {
		br := bufio.NewReader(bytes.NewReader(data))
		for i := 0; i < 8; i++ {
			var raw json.RawMessage
			if err := readJSONLine(br, &raw); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				continue
			}
			var h proto.Hello
			if json.Unmarshal(raw, &h) == nil {
				proto.Check(h, s.hello())
			}
			var ar proto.AttachRequest
			json.Unmarshal(raw, &ar)
			var req proto.Request
			if json.Unmarshal(raw, &req) != nil {
				continue
			}
			for _, v := range []any{
				&proto.ServerStopParams{}, &proto.SessionStartParams{}, &proto.SessionIDParams{},
				&proto.SessionReadParams{}, &proto.SessionKeysParams{},
			} {
				decodeParams(req.Params, v)
			}
		}
	})
}
