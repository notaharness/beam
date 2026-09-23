package control

import (
	"errors"
	"fmt"

	"github.com/notaharness/beam/internal/mailbox"
)

// msgRefused is the peer's refusal of the msg stream (docs/05), as the error
// its flusher gets. grant: every msg.send waiting on peer hears its mail is
// stored for that reason, and the flusher waits for new mail. revoked: they
// hear rejected revoked-peer, their envelopes quarantined with the peer's
// reason. Any other (limit, or one this version does not know) is retried
// as an unopened stream is, and the sends wait on.
func (d *daemon) msgRefused(peer, reason string) error {
	d.at("msg-refused", peer)
	switch reason {
	case "grant":
		d.answer(peer, sendResult{Outcome: stored, PendingReason: "grant"}, "")
		return fmt.Errorf("%w: %s", mailbox.ErrNotGranted, reason)
	case "revoked":
		d.answer(peer, sendResult{Outcome: rejected, Reason: "revoked-peer"}, reason)
	}
	return errors.New("the peer refuses the msg stream: " + reason)
}

// answer gives every msg.send waiting on peer res, first moving its envelope
// to quarantine with the peer's reason when there is one; an envelope that
// cannot be moved stays queued and its send waits on.
func (d *daemon) answer(peer string, res sendResult, quarantine string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for seq, done := range d.sends[peer] {
		if quarantine != "" {
			if err := d.store.Quarantine(peer, seq, quarantine); err != nil {
				d.o.Logf("quarantine %s %d: %v", peer[:8], seq, err)
				continue
			}
		}
		done <- res
		delete(d.sends[peer], seq)
	}
}
