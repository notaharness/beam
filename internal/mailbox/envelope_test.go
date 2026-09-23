package mailbox

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

// docs/04 msg: an envelope is exactly one JSON object in valid UTF-8; one
// with anything after it, or bytes that are not UTF-8, is invalid-envelope
// and changes nothing.
func TestAcceptRefusesMalformed(t *testing.T) {
	e, _ := New(peerA, peerB, "t", "hi", UTF8, 1)
	e.Seq = 1
	good, _ := e.Marshal()
	notUTF8 := strings.Replace(string(good), `"hi"`, "\"h\xffi\"", 1)
	for name, env := range map[string]string{
		"a trailing brace": string(good) + "}",
		"a second value":   string(good) + "{}",
		"invalid UTF-8":    notUTF8,
	} {
		t.Run(name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if a := Accept(st, peerA, peerB, []byte(env), 1); a.Accepted || a.Reason != InvalidEnvelope {
				t.Fatalf("ack %+v, want invalid-envelope", a)
			}
			if items, _, _ := st.Queue(store.Inbound, "", 0, 100); len(items) != 0 {
				t.Errorf("stored %v", items)
			}
			if a := Accept(st, peerA, peerB, good, 1); !a.Accepted {
				t.Errorf("seq 1 afterwards: %+v", a)
			}
		})
	}
}

// controlLine is the control socket's line bound, newline included (docs/06).
const controlLine = 1 << 20

// docs/05 Envelope: the largest envelope fits a control line with what wraps
// it, as a mail event and as a msg.queue page.
func TestLargestEnvelopeFitsALine(t *testing.T) {
	e := Envelope{ID: newID(), From: peerA, To: peerB, Seq: 1 << 53, Encoding: UTF8, CreatedAt: 1 << 53}
	base, _ := json.Marshal(e)
	e.Payload = strings.Repeat("\x01", (MaxEnvelope-len(base))/6) // escaped as \u0001
	env, reason := e.Marshal()
	if reason != "" || len(env) < MaxEnvelope-6 {
		t.Fatalf("an envelope of %d bytes (%s)", len(env), reason)
	}
	event, _ := stream.Marshal(map[string]any{"event": "mail", "data": json.RawMessage(env)})
	item, _ := stream.Marshal([]store.QueueItem{{Envelope: env, Reason: strings.Repeat("r", MaxDeferReason)}})
	if len(event) >= controlLine || len(item) > store.MaxPage {
		t.Errorf("event %d bytes, page %d bytes", len(event), len(item))
	}
}
