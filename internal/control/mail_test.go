package control

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
)

// mailDaemon is a daemon enrolled on a bare store.
func mailDaemon(t *testing.T) (*daemon, *enrolment) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &enrolment{fleet: &identity.Fleet{}, store: st, mail: mailbox.NewSubscribers(st)}
	return &daemon{o: Options{Logf: t.Logf}, en: e}, e
}

// docs/05 Outcomes: a storage failure before anything is stored is msg.send's
// rejected outcome, storage-failure.
func TestSendStorageFailure(t *testing.T) {
	d, e := mailDaemon(t)
	e.store.Close() // every read fails
	res, err := opMsgSend(d, e, nil, request{To: "beta", Payload: "x"})
	if r, ok := res.(sendResult); err != nil || !ok || r.Outcome != rejected || r.Reason != mailbox.StorageFailure {
		t.Fatalf("%+v, %v; want rejected storage-failure", res, err)
	}
}

// docs/05 Delivery: a refused msg stream answers the sends waiting on it by
// its reason. grant: stored, pendingReason grant, and the flusher waits for
// new mail; revoked: rejected revoked-peer, the envelope quarantined; limit,
// and a reason this version does not know: the flusher retries as for an
// unopened stream, and the sends wait on.
func TestMsgRefusals(t *testing.T) {
	for _, tc := range []struct {
		reason     string
		want       *sendResult
		notGranted bool
		queued     int
	}{
		{"grant", &sendResult{Outcome: stored, PendingReason: "grant"}, true, 1},
		{"revoked", &sendResult{Outcome: rejected, Reason: "revoked-peer"}, false, 0},
		{"limit", nil, false, 1},
		{"kind", nil, false, 1},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			d, e := mailDaemon(t)
			d.sends = map[string]map[int64]chan sendResult{}
			peer := strings.Repeat("b", 32)
			if _, err := e.store.Pin(identity.Record{V: 1, Kind: identity.Member, PeerID: peer, Label: "b"}, 1); err != nil {
				t.Fatal(err)
			}
			seq, err := e.store.Enqueue(peer, 1, func(int64) ([]byte, error) { return []byte("{}"), nil })
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan sendResult, 1)
			d.await(peer, seq, done)
			err = d.msgRefused(e, peer, tc.reason)
			if errors.Is(err, mailbox.ErrNotGranted) != tc.notGranted || err == nil {
				t.Errorf("flusher error %v, want ErrNotGranted %v", err, tc.notGranted)
			}
			select {
			case got := <-done:
				if tc.want == nil || got != *tc.want {
					t.Errorf("answered %+v, want %+v", got, tc.want)
				}
			default:
				if tc.want != nil {
					t.Errorf("not answered, want %+v", *tc.want)
				}
			}
			items, _, err := e.store.Queue("outbound", peer, 0, 10)
			if err != nil || len(items) != tc.queued {
				t.Errorf("outbound %d, %v; want %d", len(items), err, tc.queued)
			}
		})
	}
}

// docs/06 msg.subscribe: a subscribe that runs after its connection's cleanup
// registers nothing, so nothing is held for a connection that is gone.
func TestSubscribeAfterCleanup(t *testing.T) {
	d, e := mailDaemon(t)
	ours, theirs := net.Pipe()
	defer theirs.Close()
	cc := &clientConn{c: ours}
	d.unsubscribeMail(cc)
	if _, err := opMsgSubscribe(d, e, cc, request{}); err == nil || cc.sub != nil {
		t.Fatalf("subscribed a closed connection: %v", err)
	}
}

// docs/06 Two kinds of connection: a client that leaves a line unread is
// disconnected, and the send fails, rather than holding the daemon.
func TestStalledClientDisconnected(t *testing.T) {
	t.Parallel()
	ours, theirs := net.Pipe() // theirs is never read
	defer theirs.Close()
	cc := &clientConn{c: ours}
	failed := make(chan error, 1)
	go func() { failed <- cc.send(event{"mail", "x"}) }()
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("the send succeeded")
		}
	case <-time.After(sendTimeout + 5*time.Second):
		t.Fatal("the send is still waiting")
	}
	if _, err := theirs.Read(make([]byte, 1)); err == nil {
		t.Error("the connection is still open")
	}
}

// docs/06 msg.defer: a reason is at most 1 KiB, so a refused envelope with
// its reason fits a msg.queue page.
func TestDeferReasonBounded(t *testing.T) {
	d, e := mailDaemon(t)
	_, err := opMsgDefer(d, e, &clientConn{}, request{EnvelopeID: "x", Reason: strings.Repeat("r", mailbox.MaxDeferReason+1)})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != "params" || !strings.Contains(oe.Detail, "reason") {
		t.Fatalf("%v, want params for the reason", err)
	}
}
