package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/identity"
)

// publish appends r, queued with the state it commits, to the directory now
// for the flow cc waits on, and dequeues it once the worker has it; or leaves
// it to be retried while the daemon runs. It reports the outcome as the
// socket does: true, or "pending" (docs/06).
func (d *daemon) publish(ctx context.Context, e *enrolment, cc *clientConn, r identity.Record) (any, error) {
	stage(cc, "publishing")
	e.writing.Lock()
	defer e.writing.Unlock()
	d.at("publishing", r.PeerID)
	err := d.appendRecord(ctx, e, r)
	if err == nil {
		d.at("published", r.PeerID)
		return true, e.store.DonePending(r)
	}
	d.o.Logf("directory: %s %s: %v; will retry", r.Kind, short(r.PeerID), err)
	select {
	case d.pending <- struct{}{}:
	default:
	}
	return "pending", nil
}

// appendRecord seals r and appends it. This machine's own first entry,
// whose fleet the directory does not know yet, registers the fleet instead:
// that is init's request, retried. A statement the directory held already
// counts only if a reader finds r in it.
func (d *daemon) appendRecord(ctx context.Context, e *enrolment, r identity.Record) error {
	f, cred := e.fleet, e.cred
	kDir, err1 := base64.RawURLEncoding.DecodeString(f.KDir)
	tRead, err2 := base64.RawURLEncoding.DecodeString(f.TRead)
	if err := errors.Join(err1, err2); err != nil {
		return err
	}
	c, entry := d.directory(), directory.NewEntry(kDir, f.FleetID, r)
	_, held, err := c.Append(ctx, f.FleetID, entry)
	if errors.Is(err, directory.ErrNoFleet) && r.Kind == identity.Member && r.PeerID == e.self() {
		err = c.Register(ctx, cred, tRead, entry)
	}
	if err == nil && held {
		err = holds(ctx, c, cred, tRead, kDir, r)
	}
	return err
}

// holds is nil if the directory serves r as a reader takes it: the worker
// keeps the first blob appended for a statement, and whoever had r before it
// was published could have appended another under its assertion (docs/02).
func holds(ctx context.Context, c directory.Client, cred identity.Credential, tRead, kDir []byte, r identity.Record) error {
	p, err := c.Read(ctx, tRead)
	if err != nil {
		return err
	}
	h := r.StatementHash()
	for _, got := range p.Records(kDir) {
		if got.StatementHash() == h && cred.Verify(got, func(string) bool { return false }) == nil {
			return nil
		}
	}
	return directory.ErrConflict
}

func (d *daemon) directory() directory.Client { return directory.Client{URL: d.o.Directory} }

// retryPending appends queued records, with backoff while the directory is
// unavailable, until e ends. It first waits a backoff, or for a publish that
// failed, so that a publish's own first attempt usually finds its record
// still queued and nothing to share it with.
func (d *daemon) retryPending(e *enrolment) {
	backoff := minBackoff
	wait := backoff/2 + rand.N(backoff/2)
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-d.pending:
			backoff = minBackoff
		case <-time.After(wait):
		}
		if d.flushPending(e) {
			wait = backoff/2 + rand.N(backoff/2)
			backoff = min(backoff*2, maxBackoff)
		} else {
			wait, backoff = 1<<62-1, minBackoff
		}
	}
}

// flushPending appends every queued record once and reports whether the
// directory was unavailable. A record the directory refuses outright is
// dropped: retrying cannot change the answer. One whose statement it holds
// with another blob stays queued, for the log and beam status, and is tried
// again at the next start or publish.
func (d *daemon) flushPending(e *enrolment) (unavailable bool) {
	e.writing.Lock()
	defer e.writing.Unlock()
	rs, err := e.store.Pending()
	unavailable = err != nil
	for _, r := range rs {
		switch err := d.appendRecord(e.ctx, e, r); {
		case err == nil:
			_ = e.store.DonePending(r) // appends are idempotent: a leftover lands again
			d.emit("directory.published", map[string]string{"kind": r.Kind, "peerId": r.PeerID})
		case errors.Is(err, directory.ErrUnavailable):
			unavailable = true
		case errors.Is(err, directory.ErrConflict):
			d.o.Logf("directory: %s %s: %v; kept queued", r.Kind, short(r.PeerID), err)
		default:
			d.o.Logf("directory: dropping %s %s: %v", r.Kind, short(r.PeerID), err)
			_ = e.store.DonePending(r) // a refusal stands
		}
	}
	return unavailable
}

// readDirectory learns every record in the directory, once, at start
// (docs/02). Nothing waits on it.
func (d *daemon) readDirectory(e *enrolment) {
	f := e.fleet
	kDir, err1 := base64.RawURLEncoding.DecodeString(f.KDir)
	tRead, err2 := base64.RawURLEncoding.DecodeString(f.TRead)
	if err := errors.Join(err1, err2); err != nil {
		return
	}
	p, err := d.directory().Read(e.ctx, tRead)
	if err != nil {
		d.o.Logf("%v", err) // the client's errors name the directory
		return
	}
	for _, r := range p.Records(kDir) {
		b, _ := json.Marshal(r)
		d.learn(e, b)
	}
}

func short(peerID string) string { return peerID[:min(8, len(peerID))] }
