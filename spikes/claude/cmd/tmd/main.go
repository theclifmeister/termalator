// tmd is a stand-in daemon: it receives hook datagrams, stamps the receive
// time, and appends one JSON line per event to -log. It also serves
// SessionStart context on -ctx (stream socket) and accepts http hooks on -http.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

var (
	mu  sync.Mutex
	out *os.File
)

func write(rec map[string]any) {
	rec["recv_ns"] = time.Now().UnixNano()
	b, _ := json.Marshal(rec)
	mu.Lock()
	out.Write(append(b, '\n'))
	mu.Unlock()
}

func main() {
	sock := flag.String("sock", "", "datagram socket for hook events")
	ctxSock := flag.String("ctx", "", "stream socket serving SessionStart context")
	ctxFile := flag.String("ctxfile", "", "file whose content is served as context")
	httpAddr := flag.String("http", "", "listen address for http hooks")
	dgram := flag.Bool("dgram", false, "use a datagram socket instead of a stream socket")
	logPath := flag.String("log", "events.jsonl", "output log")
	flag.Parse()

	var err error
	out, err = os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}

	if *sock != "" && !*dgram {
		os.Remove(*sock)
		l, err := net.Listen("unix", *sock)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					c.SetReadDeadline(time.Now().Add(2 * time.Second))
					sc := bufio.NewScanner(c)
					sc.Buffer(make([]byte, 1<<20), 8<<20)
					for sc.Scan() {
						var rec map[string]any
						if json.Unmarshal(sc.Bytes(), &rec) != nil {
							rec = map[string]any{"raw": sc.Text()}
						}
						rec["via"] = "sock"
						write(rec)
					}
				}(c)
			}
		}()
	}
	if *sock != "" && *dgram {
		os.Remove(*sock)
		pc, err := net.ListenPacket("unixgram", *sock)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			buf := make([]byte, 8<<20)
			for {
				n, _, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				var rec map[string]any
				if json.Unmarshal(buf[:n], &rec) != nil {
					rec = map[string]any{"raw": string(buf[:n])}
				}
				rec["via"] = "sock"
				write(rec)
			}
		}()
	}

	if *ctxSock != "" {
		os.Remove(*ctxSock)
		l, err := net.Listen("unix", *ctxSock)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					line, _ := bufio.NewReader(c).ReadString('\n')
					b, _ := os.ReadFile(*ctxFile)
					fmt.Fprintf(c, "%s\n(served by tmd at %s for %s)", b, time.Now().Format(time.RFC3339), line)
					write(map[string]any{"via": "ctx", "req": line})
				}(c)
			}
		}()
	}

	if *httpAddr != "" {
		go http.ListenAndServe(*httpAddr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			rec := map[string]any{"via": "http", "path": r.URL.Path}
			if json.Valid(b) {
				rec["payload"] = json.RawMessage(b)
			} else if v, err := url.ParseQuery(string(b)); err == nil && v.Get("payload") != "" {
				rec["payload"] = json.RawMessage(v.Get("payload")) // orca form encoding
				rec["argv_ev"] = "user-orca"
			}
			write(rec)
			w.WriteHeader(200)
		}))
	}
	select {}
}
