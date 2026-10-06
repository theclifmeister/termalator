package session

import (
	"github.com/theclifmeister/terminatr/internal/proto"
)

// MaxPending bounds how far an attach client may lag behind. Past it the
// backlog is thrown away and replaced by a fresh snapshot: a slow client
// resyncs instead of blocking the PTY reader or being disconnected.
var MaxPending = 4 << 20

// Subscriber is one attached client's ordered frame stream. The session
// appends encoded frames under its lock; the client's writer drains them
// with Next.
type Subscriber struct {
	s *Session
	q *frameQueue
}

// Attach registers a subscriber. Its stream starts with a FrameSnapshot
// (libghostty's snapshot of the pane emulator), followed by every byte of
// output after that point and every resize, in order.
func (s *Session) Attach() (*Subscriber, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return nil, ErrExited
	}
	snap, err := s.term.Snapshot()
	if err != nil {
		return nil, err
	}
	sub := &Subscriber{s: s, q: newFrameQueue()}
	sub.q.push(proto.FrameSnapshot, snap)
	s.subs[sub] = struct{}{}
	return sub, nil
}

// Next blocks until frames are queued and returns them encoded, ready to
// write. ok is false once the subscriber is closed and drained.
func (sub *Subscriber) Next() (frames []byte, ok bool) { return sub.q.next() }

// Detach removes the subscriber from its session and ends its stream.
func (sub *Subscriber) Detach() {
	sub.s.mu.Lock()
	delete(sub.s.subs, sub)
	sub.s.mu.Unlock()
	sub.q.close()
}

// Resyncs counts how often the subscriber fell behind and was resynced.
func (sub *Subscriber) Resyncs() int { return sub.q.resyncCount() }

// enqueue adds a frame; s.mu held.
func (sub *Subscriber) enqueue(typ proto.FrameType, payload []byte) {
	if sub.q.wouldOverflow(len(payload)) {
		sub.resyncLocked()
		if typ == proto.FrameOutput || typ == proto.FrameResize {
			return // the snapshot already includes it
		}
	}
	sub.q.push(typ, payload)
}

// resyncLocked replaces the backlog with a fresh snapshot; s.mu held.
func (sub *Subscriber) resyncLocked() {
	snap, err := sub.s.term.Snapshot()
	if err != nil {
		sub.s.cfg.Logf("session %s: resync snapshot: %v", sub.s.cfg.ID, err)
		sub.q.close()
		return
	}
	sub.q.reset(proto.FrameSnapshot, snap)
}

func (sub *Subscriber) close() { sub.q.close() }
