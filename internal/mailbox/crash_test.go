package mailbox

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/notaharness/beam/internal/store"
	"modernc.org/sqlite"
	"pgregory.net/rapid"
)

// The machines' databases.
const dbA, dbB = "a.db", "b.db"

// kill crashes a machine in the middle of a write: SQLite's commit hook turns
// the commit a test chose into a rollback.
var kill = struct {
	sync.Mutex
	armed map[string]int  // database → commits to let through before the one killed
	fired map[string]bool // database → a commit was killed
}{armed: map[string]int{}, fired: map[string]bool{}}

func TestMain(m *testing.M) {
	sqlite.RegisterConnectionHook(func(c sqlite.ExecQuerierContext, dsn string) error {
		c.(sqlite.HookRegisterer).RegisterCommitHook(func() int32 {
			kill.Lock()
			defer kill.Unlock()
			for db, n := range kill.armed {
				switch {
				case !strings.Contains(dsn, "/"+db+"?"):
				case n > 0:
					kill.armed[db] = n - 1
				default:
					delete(kill.armed, db)
					kill.fired[db] = true
					return 1
				}
			}
			return 0
		})
		return nil
	})
	os.Exit(m.Run())
}

// world is machine A mailing machine B, with what is on the wire between
// them and B's two application subscribers.
type world struct {
	t    *rapid.T
	dir  string
	a, b *store.Store

	wire [][]byte // envelopes on their way to B
	acks []Ack    // acks on their way back

	subs [2]subscriber

	sent   map[string]bool // committed by msg.send on A
	stored map[string]int  // accepted, so stored, by B
	got    map[string]int  // acked by the application
}

type subscriber struct {
	conn     int64  // its current connection
	inflight string // the envelope id it holds
}

// open starts db's machine, again if it crashes while starting.
func (w *world) open(db string) *store.Store {
	for {
		st, err := store.Open(filepath.Join(w.dir, db))
		switch {
		case err == nil:
			return st
		case !killed(db):
			w.t.Fatal(err)
		}
	}
}

// killed reports, once, whether a commit on db was killed.
func killed(db string) bool {
	kill.Lock()
	defer kill.Unlock()
	fired := kill.fired[db]
	delete(kill.fired, db)
	return fired
}

// ok reports whether a step's writes on db went through. One whose commit was
// killed crashes db's machine instead.
func (w *world) ok(db string, err error) bool {
	switch {
	case killed(db):
		w.restart(db)
		return false
	case err != nil:
		w.t.Fatal(err)
	}
	return true
}

// restart is db's machine coming back from a crash: what was on the wire is
// lost, and on B every subscriber reconnects.
func (w *world) restart(db string) {
	if db == dbA {
		w.a.Close()
		w.a = w.open(dbA)
	} else {
		w.b.Close()
		w.b = w.open(dbB)
		for i := range w.subs {
			w.subs[i].conn += 2 // connections stay distinct across the two subscribers
			w.subs[i].inflight = ""
		}
	}
	w.lose(w.t)
}

func (w *world) send(t *rapid.T) {
	e, reason := New(peerA, peerB, "t", rapid.StringN(0, 64, -1).Draw(t, "payload"), UTF8, 1)
	if reason != "" {
		t.Skip(reason)
	}
	_, err := w.a.Enqueue(peerB, 1, func(seq int64) ([]byte, error) {
		e.Seq = seq
		b, _ := e.Marshal()
		return b, nil
	})
	if w.ok(dbA, err) {
		w.sent[e.ID] = true
	}
}

func (w *world) transmit(*rapid.T) {
	if _, env, ok, err := w.a.Head(peerB); w.ok(dbA, err) && ok {
		w.wire = append(w.wire, env)
	}
}

func (w *world) deliver(*rapid.T) {
	if len(w.wire) == 0 {
		return
	}
	env := w.wire[0]
	w.wire = w.wire[1:]
	a := Accept(w.b, peerA, peerB, env, 1)
	if !w.ok(dbB, nil) { // B crashed before its ack went out
		return
	}
	if a.Accepted {
		w.stored[a.ID]++
	}
	w.acks = append(w.acks, a)
}

func (w *world) ack(*rapid.T) {
	if len(w.acks) == 0 {
		return
	}
	a := w.acks[0]
	w.acks = w.acks[1:]
	seq, env, ok, err := w.a.Head(peerB)
	if w.ok(dbA, err) && ok && idOf(env) == a.ID { // an ack for an earlier head is stale
		_, _, err := Settle(w.a, peerB, seq, a)
		w.ok(dbA, err)
	}
}

func (w *world) subscriber(t *rapid.T) *subscriber {
	return &w.subs[rapid.IntRange(0, len(w.subs)-1).Draw(t, "subscriber")]
}

func (w *world) take(t *rapid.T) { w.takeBy(w.subscriber(t)) }

func (w *world) takeBy(s *subscriber) {
	if s.inflight != "" {
		return
	}
	if env, ok, err := w.b.Take(s.conn, nil, nil); w.ok(dbB, err) && ok {
		s.inflight = idOf(env)
	}
}

func (w *world) appAck(t *rapid.T) { w.ackBy(w.subscriber(t)) }

func (w *world) ackBy(s *subscriber) {
	if s.inflight == "" {
		return
	}
	ok, err := w.b.AckInbound(s.conn, s.inflight)
	if !w.ok(dbB, err) {
		return
	}
	if !ok {
		w.t.Fatalf("ack of %s: not in flight", s.inflight)
	}
	w.got[s.inflight]++
	s.inflight = ""
}

// appLeave is a subscriber's connection dropping; a new one follows.
func (w *world) appLeave(t *rapid.T) {
	s := w.subscriber(t)
	if w.ok(dbB, w.b.Release(s.conn)) {
		s.conn += 2
		s.inflight = ""
	}
}

func (w *world) lose(*rapid.T) { w.wire, w.acks = nil, nil }

// crash kills one of a machine's next few commits.
func (w *world) crash(t *rapid.T) {
	db := rapid.SampledFrom([]string{dbA, dbB}).Draw(t, "db")
	kill.Lock()
	kill.armed[db] = rapid.IntRange(0, 2).Draw(t, "commits before")
	kill.Unlock()
}

// restartIdle is a machine crashing between writes.
func (w *world) restartIdle(t *rapid.T) {
	w.restart(rapid.SampledFrom([]string{dbA, dbB}).Draw(t, "db"))
}

// drain runs delivery without faults until both queues are empty.
func (w *world) drain() {
	kill.Lock()
	clear(kill.armed)
	kill.Unlock()
	for range 10 * (len(w.sent) + 1) {
		w.transmit(w.t)
		w.deliver(w.t)
		w.ack(w.t)
		for i := range w.subs {
			w.takeBy(&w.subs[i])
			w.ackBy(&w.subs[i])
		}
	}
}

// docs/05 and docs/10: whatever the interleaving of delivery steps, lost
// connections, subscribers that leave, and crashes of either machine between
// writes or in the middle of one, every envelope msg.send committed is stored
// by the receiver exactly once and acked by its application exactly once.
func TestCrashPoints(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "mailbox")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		kill.Lock()
		clear(kill.armed)
		clear(kill.fired)
		kill.Unlock()
		w := &world{t: t, dir: dir, subs: [2]subscriber{{conn: 0}, {conn: 1}},
			sent: map[string]bool{}, stored: map[string]int{}, got: map[string]int{}}
		w.a, w.b = w.open(dbA), w.open(dbB)
		defer func() { w.a.Close(); w.b.Close() }()
		t.Repeat(map[string]func(*rapid.T){
			"send": w.send, "transmit": w.transmit, "deliver": w.deliver, "ack": w.ack,
			"take": w.take, "appAck": w.appAck, "appLeave": w.appLeave,
			"lose": w.lose, "crash": w.crash, "restart": w.restartIdle,
		})
		w.drain()
		for id := range w.sent {
			if w.stored[id] != 1 || w.got[id] != 1 {
				t.Fatalf("%s: stored %d times, acked %d times", id, w.stored[id], w.got[id])
			}
		}
		if len(w.got) != len(w.sent) {
			t.Fatalf("acked %d envelopes, sent %d", len(w.got), len(w.sent))
		}
	})
}
