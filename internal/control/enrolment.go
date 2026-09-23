package control

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/notaharness/beam/internal/ceremony"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/transport"
)

// enrolment is what enroll starts and unenroll ends. It does not change: a
// re-join or a reset replaces it whole, and whatever still runs on the one
// it replaced finds that one's context done, store closed and node gone.
type enrolment struct {
	ctx    context.Context // done at unenroll
	cancel context.CancelFunc
	key    *transport.Key
	fleet  *identity.Fleet
	cred   identity.Credential
	store  *store.Store
	node   *transport.Node
	mail   *mailbox.Subscribers
}

// end stops what runs on e; its store stays open for the caller to close.
func (e *enrolment) end() {
	e.cancel()
	e.node.Close()
}

// self is this machine's peer id.
func (e *enrolment) self() string { return e.fleet.Entry.PeerID }

// signed completes entry with the get's assertion and checks it and the
// directory keys the get yielded, returning the fleet.json it makes.
func (d *daemon) signed(cred identity.Credential, entry *identity.Record, got ceremony.Result, revoked func(string) bool) (*identity.Fleet, error) {
	kDir, tRead, err := got.DirectoryKeys()
	if err != nil {
		return nil, fail("prf-unsupported", "")
	}
	entry.Assertion = got.Assertion()
	if revoked == nil {
		revoked = func(string) bool { return false }
	}
	if err := cred.Verify(*entry, revoked); err != nil {
		return nil, refusal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return &identity.Fleet{V: 1, FleetID: cred.FleetID(), CredentialID: cred.ID, CredentialPublicKey: enc(cred.PublicKey),
		KDir: enc(kDir), TRead: enc(tRead), Entry: *entry}, nil
}

// enrollAs writes fleet.json and enrolls with it, first ending the
// enrolment a re-join replaces. d.enrolling must be held.
func (d *daemon) enrollAs(f *identity.Fleet, rejoin bool) (*enrolment, error) {
	if rejoin {
		if err := d.unenroll(false); err != nil {
			return nil, err
		}
	}
	if err := d.o.Paths.save(fleetFile, f); err != nil {
		return nil, err
	}
	return d.enroll()
}

func (e *enrolment) sameKDir(kDir []byte) bool {
	cached, err := base64.RawURLEncoding.DecodeString(e.fleet.KDir)
	return err == nil && hmac.Equal(cached, kDir)
}

// homeKey is key.json's key, made on first use, homed on the daemon's DERP
// map (docs/03: a machine that moves to another map re-joins).
func (d *daemon) homeKey() (*transport.Key, error) {
	k, err := d.o.Paths.loadKey()
	switch {
	case errors.Is(err, os.ErrNotExist):
		k = nil
	case err != nil:
		return nil, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, dialTimeout)
	defer cancel()
	if k, err = transport.HomeKey(ctx, d.o.DERPMap, derpCache{d.o.Paths}, k); err != nil {
		return nil, err
	}
	return k, d.o.Paths.save(keyFile, k)
}

func memberEntry(k *transport.Key, label string) identity.Record {
	pub := k.NodePublic()
	return identity.Record{V: 1, Kind: identity.Member, PeerID: identity.PeerID(pub),
		NodePublic: base64.RawURLEncoding.EncodeToString(pub[:]), Address: k.Address(), Label: label, IssuedAt: now()}
}

// signing is the get that signs r.
func signing(r identity.Record, action string) ceremony.Request {
	return ceremony.Request{Op: ceremony.Get, Action: action, Label: r.Label, Fingerprint: identity.Fingerprint(r.PeerID), Challenge: r.Challenge()}
}

// labelOr is label, or the short host name when empty, checked (docs/02).
func labelOr(label string) (string, error) {
	if label == "" {
		host, _ := os.Hostname()
		label = shortHost(host)
	}
	if !identity.ValidLabel(label) {
		return "", fail("params", "a label is 1–64 characters without / \\ { } or control characters")
	}
	return label, nil
}

// shortHost is a host name up to its first dot, cut to a label's 64
// characters.
func shortHost(host string) string {
	host, _, _ = strings.Cut(host, ".")
	if r := []rune(host); len(r) > 64 {
		host = string(r[:64])
	}
	return host
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// members is how many other machines this one knows and has not revoked.
func (e *enrolment) members() int {
	ps, _ := e.store.Peers()
	n := 0
	for _, p := range ps {
		if !p.Revoked {
			n++
		}
	}
	return n
}
