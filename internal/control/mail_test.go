package control

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
)

func mailDaemon(t *testing.T) *daemon {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &daemon{o: Options{Logf: t.Logf}, store: st, mail: mailbox.NewSubscribers(st)}
}

// docs/05 Outcomes: a storage failure before anything is stored is msg.send's
// rejected outcome, storage-failure.
func TestSendStorageFailure(t *testing.T) {
	d := mailDaemon(t)
	d.store.Close() // every read fails
	res, err := opMsgSend(d, nil, request{To: "beta", Payload: "x"})
	if r, ok := res.(sendResult); err != nil || !ok || r.Outcome != rejected || r.Reason != mailbox.StorageFailure {
		t.Fatalf("%+v, %v; want rejected storage-failure", res, err)
	}
}

// docs/06 msg.subscribe: a subscribe that runs after its connection's cleanup
// registers nothing, so nothing is held for a connection that is gone.
func TestSubscribeAfterCleanup(t *testing.T) {
	d := mailDaemon(t)
	ours, theirs := net.Pipe()
	defer theirs.Close()
	cc := &clientConn{c: ours}
	d.unsubscribeMail(cc)
	if _, err := opMsgSubscribe(d, cc, request{}); err == nil || cc.sub != nil {
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
	d := mailDaemon(t)
	_, err := opMsgDefer(d, &clientConn{}, request{EnvelopeID: "x", Reason: strings.Repeat("r", mailbox.MaxDeferReason+1)})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != "params" || !strings.Contains(oe.Detail, "reason") {
		t.Fatalf("%v, want params for the reason", err)
	}
}
