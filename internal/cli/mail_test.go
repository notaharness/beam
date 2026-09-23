//go:build beamtest

package cli_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
)

// subscribeMail subscribes to m's mail; next waits for the next envelope.
func subscribeMail(t *testing.T, m *machine, params map[string]any) (*control.Client, func() mailbox.Envelope) {
	t.Helper()
	c, err := control.Connect(m.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Call("msg.subscribe", params, nil); err != nil {
		t.Fatal(err)
	}
	return c, func() mailbox.Envelope {
		t.Helper()
		got := make(chan control.Event, 1)
		go func() {
			for {
				ev, err := c.Next()
				if err != nil {
					close(got)
					return
				}
				if ev.Name == "mail" {
					got <- ev
					return
				}
			}
		}()
		select {
		case ev, ok := <-got:
			var e mailbox.Envelope
			if !ok || json.Unmarshal(ev.Data, &e) != nil {
				t.Fatalf("mail: %s", ev.Data)
			}
			return e
		case <-time.After(45 * time.Second):
			t.Fatal("no mail")
		}
		return mailbox.Envelope{}
	}
}

func queue(t *testing.T, m *machine, args ...string) []string {
	t.Helper()
	r := m.beam("", append([]string{"msg", "queue"}, args...)...)
	if r.code != 0 {
		t.Fatalf("msg queue: %+v", r)
	}
	if r.out == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(r.out, "\n"), "\n")
}

// docs/10 mailbox: a send while connected is flushed at once and delivered;
// the subscriber gets the envelope as sent, acks it, and it is gone.
func TestMsgDelivered(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	start := time.Now()
	if r := a.beam("", "msg", "send", "beta", "--topic", "orchestra", "hello"); r.code != 0 || r.out != "delivered to beta\n" {
		t.Fatalf("send: %+v", r)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("delivered after %v: not flushed at once", d)
	}
	if r := a.beam("\x00\x01bytes", "msg", "send", "beta", "--base64", "-"); r.code != 0 || r.out != "delivered to beta\n" {
		t.Fatalf("send --base64: %+v", r)
	}
	c, next := subscribeMail(t, b, nil)
	for _, want := range []mailbox.Envelope{
		{From: a.id(), To: b.id(), Seq: 1, Topic: "orchestra", Payload: "hello", Encoding: "utf8"},
		{From: a.id(), To: b.id(), Seq: 2, Payload: base64.RawURLEncoding.EncodeToString([]byte("\x00\x01bytes")), Encoding: "base64"},
	} {
		e := next()
		if want.ID, want.CreatedAt = e.ID, e.CreatedAt; e != want {
			t.Errorf("got %+v\nwant %+v", e, want)
		}
		if err := c.Call("msg.ack", map[string]any{"envelopeId": e.ID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if q := queue(t, b, "--which", "inbound"); len(q) != 0 {
		t.Errorf("inbound after acks: %v", q)
	}
	if q := queue(t, a); len(q) != 0 {
		t.Errorf("outbound after delivery: %v", q)
	}
}

// docs/10 mailbox: mail to an offline peer is stored, said so, and delivered
// when the peer comes up.
func TestMsgStored(t *testing.T) {
	a, b := newMachine(t, "alpha"), newMachine(t, "beta")
	a.knows(t, b)
	b.knows(t, a)
	a.start(t)
	want := "stored for beta; delivery pending (beta is offline). beam will deliver it when beta connects. Do not send it again.\n"
	if r := a.beam("", "msg", "send", "beta", "later"); r.code != 0 || r.out != want {
		t.Fatalf("send: %+v", r)
	}
	if q := queue(t, a, "beta"); len(q) != 1 || !strings.Contains(q[0], `"payload":"later"`) {
		t.Fatalf("outbound: %v", q)
	}
	b.start(t)
	_, next := subscribeMail(t, b, nil)
	if e := next(); e.Payload != "later" {
		t.Errorf("got %+v", e)
	}
	waitFor(t, 10*time.Second, "alpha's outbound to empty", func() bool { return len(queue(t, a, "beta")) == 0 })
}

// docs/05: an envelope the recipient refuses for good is quarantined, and the
// queue behind it flows on.
func TestMsgQuarantine(t *testing.T) {
	a, b := newMachine(t, "alpha"), newMachine(t, "beta")
	a.knows(t, b)
	b.knows(t, a)
	st, err := store.Open(filepath.Join(a.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	bad, _ := mailbox.New(a.id(), b.id(), "", "fine", mailbox.UTF8, time.Now().UnixMilli())
	bad.Payload, bad.Encoding = "not-base64!", mailbox.Base64 // what msg.send would never queue
	if _, err := st.Enqueue(b.id(), bad.CreatedAt, func(seq int64) ([]byte, error) { bad.Seq = seq; return json.Marshal(bad) }); err != nil {
		t.Fatal(err)
	}
	st.Close()
	a.start(t)
	b.start(t)
	waitState(t, a, b, "connected")
	if r := a.beam("", "msg", "send", "beta", "after"); r.out != "delivered to beta\n" {
		t.Fatalf("send behind the bad envelope: %+v", r)
	}
	if q := queue(t, a, "--which", "quarantine"); len(q) != 1 || !strings.Contains(q[0], bad.ID) || !strings.Contains(q[0], `"reason":"invalid-envelope"`) {
		t.Errorf("quarantine: %v", q)
	}
}

// docs/06 msg.queue: pages of at most 100, which msg queue follows to the end
// in order.
func TestMsgQueuePages(t *testing.T) {
	a, b := newMachine(t, "alpha"), newMachine(t, "beta")
	a.knows(t, b)
	st, err := store.Open(filepath.Join(a.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 150 {
		e, _ := mailbox.New(a.id(), b.id(), "", strconv.Itoa(i), mailbox.UTF8, time.Now().UnixMilli())
		if _, err := st.Enqueue(b.id(), e.CreatedAt, func(seq int64) ([]byte, error) { e.Seq = seq; return json.Marshal(e) }); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	a.start(t)
	q := queue(t, a)
	if len(q) != 150 {
		t.Fatalf("%d lines, want 150", len(q))
	}
	for i, line := range q {
		if !strings.Contains(line, `"payload":"`+strconv.Itoa(i)+`"`) {
			t.Fatalf("line %d: %s", i, line)
		}
	}
}

// docs/05: msg.send rejects what it cannot send and stores nothing: an
// unknown or revoked peer, a bad topic, a payload too large, a full queue
// (quarantine included), or a store that failed (here, a lost send counter).
func TestMsgRejected(t *testing.T) {
	a, b, gone, full, lost := newMachine(t, "alpha"), newMachine(t, "beta"), newMachine(t, "gone"), newMachine(t, "full"), newMachine(t, "lost")
	a.knows(t, b, gone, full, lost)
	st, err := store.Open(filepath.Join(a.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Revoke(revocation(gone), 1)
	st.Close()
	sqlite(t, a, func(db *sql.Tx) error {
		for i := range store.MaxQueued {
			if _, err := db.Exec(`INSERT INTO quarantine VALUES (?, ?, '{}', 'invalid-envelope')`, full.id(), i+1); err != nil {
				return err
			}
		}
		_, err := db.Exec(`DELETE FROM send_seq WHERE peer = ?`, lost.id())
		return err
	})
	a.start(t)
	for _, tc := range []struct {
		stdin, reason string
		args          []string
	}{
		{"", "unknown-peer", []string{"nobody", "x"}},
		{"", "revoked-peer", []string{"gone", "x"}},
		{"", "invalid-topic", []string{"beta", "--topic", "a/b", "x"}},
		{strings.Repeat("x", mailbox.MaxPayload+1), "payload-too-large", []string{"beta", "-"}},
		{"", "queue-full", []string{"full", "x"}},
		{"", "storage-failure", []string{"lost", "x"}},
	} {
		r := a.beam(tc.stdin, append([]string{"msg", "send"}, tc.args...)...)
		if r.code != 1 || r.err != "rejected: "+tc.reason+"\n" {
			t.Errorf("%v: %+v, want rejected: %s", tc.args, r, tc.reason)
		}
	}
	if q := queue(t, a); len(q) != 0 {
		t.Errorf("outbound after rejections: %v", q)
	}
}

// sqlite runs f in one transaction on m's state.db, its daemon stopped.
func sqlite(t *testing.T, m *machine, f func(*sql.Tx) error) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(m.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err == nil {
		err = f(tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		t.Fatal(err)
	}
}

// docs/06 msg.queue: pages of large envelopes each fit a control line, and
// msg queue lists every envelope.
func TestMsgQueueLargePages(t *testing.T) {
	a, b := newMachine(t, "alpha"), newMachine(t, "beta")
	a.knows(t, b)
	a.start(t)
	for i := range 5 {
		payload := strconv.Itoa(i) + strings.Repeat("x", mailbox.MaxPayload-1)
		if r := a.beam(payload, "msg", "send", "beta", "-"); r.code != 0 {
			t.Fatalf("send %d: %+v", i, r)
		}
	}
	q := queue(t, a)
	if len(q) != 5 {
		t.Fatalf("%d lines, want 5", len(q))
	}
	for i, line := range q {
		if !strings.Contains(line, `"payload":"`+strconv.Itoa(i)+"x") {
			t.Errorf("line %d: %.80s", i, line)
		}
	}
}

// docs/05 Subscribers: a subscribe that its client left before the daemon
// ran it holds nothing; the mail goes to a subscriber that is there.
func TestSubscribeRacingDisconnect(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	reached, release := pauseAt(t, b, "subscribing", "")
	c, err := net.Dial("unix", b.paths().Socket)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(`{"id":1,"op":"msg.subscribe"}` + "\n"))
	await(t, reached, "the subscribe")
	c.Close()
	time.Sleep(300 * time.Millisecond) // for the connection's cleanup
	release()
	if r := a.beam("", "msg", "send", "beta", "for whoever is there"); r.out != "delivered to beta\n" {
		t.Fatalf("send: %+v", r)
	}
	_, next := subscribeMail(t, b, nil)
	if e := next(); e.Payload != "for whoever is there" {
		t.Errorf("got %+v", e)
	}
}

// docs/05: an envelope stored whose ack was lost comes again and is answered
// duplicate; the application gets it once.
func TestMsgDuplicateSuppressed(t *testing.T) {
	b := fleet(t, "beta")[0]
	tun, raw := rawTunnel(t, b)
	e, _ := mailbox.New(raw.id(), b.id(), "", "once", mailbox.UTF8, time.Now().UnixMilli())
	e.Seq = 1
	env, _ := e.Marshal()
	for _, want := range []mailbox.Ack{{Kind: "ack", ID: e.ID, Accepted: true}, {Kind: "ack", ID: e.ID, Reason: "duplicate"}} {
		sc := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindMsg})
		sc.WriteFrame(stream.Data, env)
		var a mailbox.Ack
		if _, p, err := sc.ReadFrame(); err != nil || json.Unmarshal(p, &a) != nil || a != want {
			t.Fatalf("ack %+v, %v; want %+v", a, err, want)
		}
		sc.Close() // as a sender whose ack never arrived
	}
	c, next := subscribeMail(t, b, nil)
	if got := next(); got.ID != e.ID {
		t.Fatalf("got %+v", got)
	}
	c.Call("msg.ack", map[string]any{"envelopeId": e.ID}, nil)
	if q := queue(t, b, "--which", "inbound"); len(q) != 0 {
		t.Errorf("inbound: %v", q)
	}
}

// docs/05: a deferred envelope is not offered to the subscription that
// deferred it, is listed as refused with its reason, and comes again to the
// next subscription; so does one whose subscriber disconnects unacked.
func TestMsgDefer(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	for _, p := range []string{"one", "two"} {
		if r := a.beam("", "msg", "send", "beta", p); r.code != 0 {
			t.Fatalf("send: %+v", r)
		}
	}
	c, next := subscribeMail(t, b, nil)
	one := next()
	var oe *control.OpError
	if err := c.Call("msg.ack", map[string]any{"envelopeId": "not-held"}, nil); !errors.As(err, &oe) || oe.Code != "params" {
		t.Errorf("ack of an envelope not held: %v", err)
	}
	if err := c.Call("msg.defer", map[string]any{"envelopeId": one.ID, "reason": "busy"}, nil); err != nil {
		t.Fatal(err)
	}
	two := next()
	if one.Payload != "one" || two.Payload != "two" {
		t.Fatalf("got %q then %q", one.Payload, two.Payload)
	}
	if q := queue(t, b, "--which", "refused"); len(q) != 1 || !strings.Contains(q[0], `"reason":"busy"`) || !strings.Contains(q[0], one.ID) {
		t.Errorf("refused: %v", q)
	}
	c.Call("msg.ack", map[string]any{"envelopeId": two.ID}, nil)
	if err := c.Call("msg.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	if again := next(); again.ID != one.ID {
		t.Errorf("after resubscribing: %+v, want %s again", again, one.ID)
	}
	c.Close()
	c, next = subscribeMail(t, b, nil)
	if again := next(); again.ID != one.ID {
		t.Errorf("after a disconnect: %+v, want %s again", again, one.ID)
	}
	c.Call("msg.ack", map[string]any{"envelopeId": one.ID}, nil)
	if q := queue(t, b, "--which", "refused"); len(q) != 0 {
		t.Errorf("refused after ack: %v", q)
	}
}

// lines is an io.Writer tests read while beam writes to it.
type lines struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// docs/07: msg listen prints one envelope per line, filtered by topic and
// sender, and acks each once written.
func TestMsgListen(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	var out lines
	done := make(chan int, 1)
	go func() {
		done <- b.run(strings.NewReader(""), &out, io.Discard, "msg", "listen", "--topic", "news", "alpha")
	}()
	send := func(args ...string) {
		t.Helper()
		if r := a.beam("", append([]string{"msg", "send", "beta"}, args...)...); r.code != 0 {
			t.Fatalf("send: %+v", r)
		}
	}
	send("--topic", "news", "first")
	send("--topic", "other", "skipped")
	tun, raw := rawTunnel(t, b) // news from a member not listened to
	e, _ := mailbox.New(raw.id(), b.id(), "news", "unheard", mailbox.UTF8, time.Now().UnixMilli())
	e.Seq = 1
	env, _ := e.Marshal()
	sc := rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindMsg})
	sc.WriteFrame(stream.Data, env)
	if _, _, err := sc.ReadFrame(); err != nil { // its ack: stored
		t.Fatal(err)
	}
	send("--topic", "news", "second")
	waitFor(t, 10*time.Second, "two lines", func() bool { return strings.Count(out.String(), "\n") == 2 })
	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.Contains(got[0], `"payload":"first"`) || !strings.Contains(got[1], `"payload":"second"`) {
		t.Errorf("listen printed %q", got)
	}
	waitFor(t, 5*time.Second, "both acked", func() bool { return len(queue(t, b, "--which", "inbound")) == 2 })
	b.stop()
	if code := <-done; code != 1 {
		t.Errorf("listen exited %d when the daemon went away, want 1", code)
	}
}

// docs/04 msg: one msg stream per tunnel, so envelopes from one sender are
// stored in order. A second is refused limit while the first is open, and
// one opened after it closed is admitted; exec and pty streams count apart.
func TestMsgOneStreamPerTunnel(t *testing.T) {
	b := fleet(t, "beta")[0]
	tun, _ := rawTunnel(t, b)
	defer rawOpen(t, tun, stream.Header{V: 1, Kind: stream.KindExec, Argv: []string{"sleep", "30"}}).Close()
	h := stream.Header{V: 1, Kind: stream.KindMsg}
	first := rawOpen(t, tun, h)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ref *transport.Refused
	if sc, err := tun.Open(ctx, h); !errors.As(err, &ref) || ref.Reason != "limit" {
		if sc != nil {
			sc.Close()
		}
		t.Fatalf("a second msg stream: %v, want limit", err)
	}
	first.Close()
	waitFor(t, 5*time.Second, "a msg stream after the first closed", func() bool {
		sc, err := tun.Open(ctx, h)
		if err == nil {
			sc.Close()
		}
		return err == nil
	})
}

// D34: a payload of markup or line separators travels as it is stored,
// never escaped past a control line: in msg.send, in the mail event and in a
// msg.queue page. The second is 960,000 bytes as stored.
func TestMsgMarkupPayload(t *testing.T) {
	ms := fleet(t, "alpha", "beta")
	a, b := ms[0], ms[1]
	waitState(t, a, b, "connected")
	for _, payload := range []string{
		strings.Repeat("<", mailbox.MaxPayload),
		strings.Repeat("\x01", 140000) + strings.Repeat("\u2028\u2029", 20000),
	} {
		if r := a.beam(payload, "msg", "send", "beta", "-"); r.out != "delivered to beta\n" {
			t.Fatalf("send: %d, %.200s", r.code, r.err)
		}
		var item struct{ Envelope mailbox.Envelope }
		if q := queue(t, b, "--which", "inbound"); len(q) != 1 || json.Unmarshal([]byte(q[0]), &item) != nil || item.Envelope.Payload != payload {
			t.Fatalf("queue: %d lines, %.200s", len(q), q)
		}
		c, next := subscribeMail(t, b, nil)
		e := next()
		if e.Payload != payload {
			t.Fatal("the mail event is not the payload")
		}
		if err := c.Call("msg.ack", map[string]any{"envelopeId": e.ID}, nil); err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
}
