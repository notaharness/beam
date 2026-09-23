package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/notaharness/beam/internal/ceremony"
	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/identity"
)

// ackWait is how long revoke.wait waits for peers to read the revocation.
const ackWait = 5 * time.Second

// opInitStart is beam init (docs/02): a create for the fleet's passkey, then
// a get that signs this machine's entry and yields the directory keys.
func opInitStart(d *daemon, _ *clientConn, r request) (any, error) {
	gen := d.generation()
	if d.isEnrolled() {
		return nil, fail("already-enrolled", "")
	}
	label, err := labelOr(r.Label)
	if err != nil {
		return nil, err
	}
	fleetName := r.FleetName
	if fleetName == "" {
		fleetName = "beam"
	}
	k, err := d.homeKey()
	if err != nil {
		return nil, err
	}
	challenge := random(32)
	req := ceremony.Request{Op: ceremony.Create, Action: "Create your beam fleet", Label: label,
		Fingerprint: identity.Fingerprint(identity.PeerID(k.NodePublic())), Challenge: challenge, FleetName: fleetName}
	return d.begin("init", req, func(ctx context.Context, cc *clientConn, created ceremony.Result) (any, error) {
		cred, err := created.Credential(challenge)
		if err != nil {
			return nil, fail("bad-assertion", err.Error())
		}
		entry := memberEntry(k, label)
		got, err := another(ctx, cc, signing(entry, "Add "+label+" to your fleet"))
		if err != nil {
			return nil, err
		}
		f, err := d.signed(cred, &entry, got, nil)
		if err != nil {
			return nil, err
		}
		var e *enrolment
		if err := d.commit(gen, func() (err error) { e, err = d.enrollAs(f, false); return err }); err != nil {
			return nil, err
		}
		pub, err := d.publish(ctx, e, cc, entry)
		return map[string]any{"peerId": entry.PeerID, "fleetId": f.FleetID, "address": entry.Address, "published": pub}, err
	})
}

// opJoinStart is beam join (docs/02): a get that signs this machine's entry
// and yields the directory keys, then the directory read.
func opJoinStart(d *daemon, _ *clientConn, r request) (any, error) {
	gen := d.generation()
	label, err := labelOr(r.Label)
	if err != nil {
		return nil, err
	}
	k, err := d.homeKey()
	if err != nil {
		return nil, err
	}
	entry := memberEntry(k, label)
	return d.begin("join", signing(entry, "Add "+label+" to your fleet"), func(ctx context.Context, cc *clientConn, got ceremony.Result) (any, error) {
		return d.join(ctx, cc, gen, entry, got)
	})
}

func (d *daemon) join(ctx context.Context, cc *clientConn, gen uint64, entry identity.Record, got ceremony.Result) (any, error) {
	kDir, tRead, err := got.DirectoryKeys()
	if err != nil {
		return nil, fail("prf-unsupported", "")
	}
	cur := d.enrolment()
	rejoin := cur != nil
	var pinned *identity.Credential
	if rejoin {
		if !cur.sameKDir(kDir) {
			return nil, fail("wrong-passkey", "")
		}
		pinned = &cur.cred
	}
	stage(cc, "reading directory")
	cred, records, revoked, err := d.readFleet(ctx, kDir, tRead, pinned)
	if err != nil {
		return nil, err
	}
	f, err := d.signed(cred, &entry, got, func(id string) bool { return revoked[id] })
	if err != nil {
		return nil, err
	}
	d.at("enrolling", entry.PeerID)
	var e *enrolment
	if err := d.commit(gen, func() (err error) { e, err = d.enrollAs(f, rejoin); return err }); err != nil {
		return nil, err
	}
	for _, r := range records {
		b, _ := json.Marshal(r)
		d.learn(e, b)
	}
	pub, err := d.publish(ctx, e, cc, entry)
	return map[string]any{"peerId": entry.PeerID, "fleetId": f.FleetID, "members": e.members(), "published": pub}, err
}

// readFleet reads the directory tRead opens: the fleet's credential, its
// records, and the peers they verifiably revoke. A pinned credential, a
// re-joining machine's root, is the fleet's whatever the directory says.
func (d *daemon) readFleet(ctx context.Context, kDir, tRead []byte, pinned *identity.Credential) (identity.Credential, []identity.Record, map[string]bool, error) {
	p, err := d.directory().Read(ctx, tRead)
	switch {
	case errors.Is(err, directory.ErrUnauthorized):
		return identity.Credential{}, nil, nil, fail("wrong-passkey", "this passkey has no fleet in the directory")
	case err != nil:
		return identity.Credential{}, nil, nil, fail("directory-unavailable", err.Error())
	}
	pk, _ := base64.RawURLEncoding.DecodeString(p.CredentialPublicKey) // a key that does not decode verifies nothing
	cred := identity.Credential{ID: p.CredentialID, PublicKey: pk}
	if pinned != nil {
		cred = *pinned
	}
	records := p.Records(kDir)
	revoked := map[string]bool{}
	for _, r := range records {
		if r.Kind == identity.Revoke && cred.Verify(r, nil) == nil {
			revoked[r.PeerID] = true
		}
	}
	return cred, records, revoked, nil
}

// opRevokeStart is beam revoke (docs/02): a get that signs the revocation.
func opRevokeStart(d *daemon, e *enrolment, _ *clientConn, r request) (any, error) {
	gen := d.generation() // an e already replaced has its store closed: nothing commits
	id, err := e.resolve(r.Peer)
	if err != nil {
		return nil, err
	}
	p, _, err := e.store.Peer(id)
	if err != nil {
		return nil, err
	}
	rec := identity.Record{V: 1, Kind: identity.Revoke, PeerID: id, IssuedAt: now()}
	req := ceremony.Request{Op: ceremony.Get, Action: "Remove " + p.Entry.Label + " (" + identity.Fingerprint(id) + ") from your fleet",
		Label: p.Entry.Label, Fingerprint: identity.Fingerprint(id), Challenge: rec.Challenge()}
	return d.begin("revoke", req, func(ctx context.Context, cc *clientConn, got ceremony.Result) (any, error) {
		kDir, _, err := got.DirectoryKeys()
		if err != nil || !e.sameKDir(kDir) {
			return nil, fail("wrong-passkey", "")
		}
		rec.Assertion = got.Assertion()
		if err := e.cred.Verify(rec, nil); err != nil {
			return nil, refusal(err)
		}
		err = d.commit(gen, func() error {
			if _, err := e.store.Revoke(rec, now()); err != nil {
				return err
			}
			d.applyRevocation(e, id)
			return nil
		})
		if err != nil {
			return nil, err
		}
		by := d.pushAcked(rec)
		pub, err := d.publish(ctx, e, cc, rec)
		return map[string]any{"local": true, "published": pub, "acknowledgedBy": by}, err
	})
}

// pushAcked pushes r on every live sync stream and counts the peers that
// read it within ackWait (docs/02: the pong to a ping sent after it).
func (d *daemon) pushAcked(r identity.Record) int {
	d.mu.Lock()
	room := len(d.peers)
	d.mu.Unlock()
	acked := make(chan struct{}, room)
	n, by := d.broadcast(r, acked), 0
	deadline := time.After(ackWait)
	for by < n {
		select {
		case <-acked:
			by++
		case <-deadline:
			return by
		}
	}
	return by
}

// opFleetReset is beam fleet reset (docs/02). It ends the ceremony under way,
// and one already past its ceremony commits nothing.
func opFleetReset(d *daemon, _ *clientConn, r request) (any, error) {
	if r.Confirm != "reset" {
		return nil, fail("params", `confirm must be "reset"`)
	}
	d.enrolling.Lock()
	defer d.enrolling.Unlock()
	d.endFlow()
	return struct{}{}, d.unenroll(true)
}
