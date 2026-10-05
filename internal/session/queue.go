package session

import (
	"sync"

	"github.com/theclifmeister/termilator/internal/proto"
)

// frameQueue holds encoded frames for one subscriber. Adjacent output
// frames are merged, so a burst of small PTY reads becomes one write.
type frameQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []byte
	lastOut int // offset of a trailing FrameOutput in pending, or -1
	closed  bool
	resyncs int
}

func newFrameQueue() *frameQueue {
	q := &frameQueue{lastOut: -1}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *frameQueue) wouldOverflow(n int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)+n > MaxPending
}

func (q *frameQueue) push(typ proto.FrameType, payload []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if typ == proto.FrameOutput && q.lastOut >= 0 {
		q.pending = append(q.pending, payload...)
		n := len(q.pending) - q.lastOut - 5
		h := q.pending[q.lastOut+1:]
		h[0], h[1], h[2], h[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	} else {
		q.lastOut = -1
		if typ == proto.FrameOutput {
			q.lastOut = len(q.pending)
		}
		q.pending = proto.AppendFrame(q.pending, typ, payload)
	}
	q.cond.Signal()
}

// reset drops the backlog and queues a single frame in its place.
func (q *frameQueue) reset(typ proto.FrameType, payload []byte) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.resyncs++
	q.pending = proto.AppendFrame(q.pending[:0], typ, payload)
	q.lastOut = -1
	q.cond.Signal()
	q.mu.Unlock()
}

func (q *frameQueue) next() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.pending) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.pending) == 0 {
		return nil, false
	}
	b := q.pending
	q.pending = nil
	q.lastOut = -1
	return b, true
}

func (q *frameQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *frameQueue) resyncCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.resyncs
}
