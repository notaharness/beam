package mailbox

import (
	"github.com/notaharness/beam/internal/store"
)

// Ack is the acceptor's answer to one envelope on a msg stream (docs/04).
type Ack struct {
	Kind     string `json:"kind"` // "ack"
	ID       string `json:"id"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// Accept is the receiver's step for one envelope from peer, addressed to
// self: it is stored, and high_seq raised, before the ack says accepted.
func Accept(st *store.Store, peer, self string, b []byte, now int64) Ack {
	e, reason := parse(b, peer, self)
	if reason == "" {
		var err error
		if reason, err = st.Receive(peer, e.Seq, e.ID, e.Topic, b, now); err != nil {
			reason = StorageFailure
		}
	}
	return Ack{Kind: "ack", ID: e.ID, Accepted: reason == "", Reason: reason}
}

// permanent refusals move an envelope aside; any other refusal is retried.
var permanent = map[string]bool{PayloadTooLarge: true, InvalidEnvelope: true}

// Settle is the sender's step for the ack of peer's queue head seq. It
// reports how the envelope left the queue ("" delivered, or the reason it
// was quarantined), or ok false when it stays to be sent again.
func Settle(st *store.Store, peer string, seq int64, a Ack) (reason string, ok bool, err error) {
	switch {
	case a.Accepted || a.Reason == store.Duplicate:
		return "", true, st.Delivered(peer, seq)
	case permanent[a.Reason]:
		return a.Reason, true, st.Quarantine(peer, seq, a.Reason)
	}
	return "", false, nil
}
