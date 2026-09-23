package control

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/notaharness/beam/internal/ceremony"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
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

	writing sync.Mutex // one directory write at a time: a publish, or a pass over the queue

	serving  sync.Mutex
	ending   bool           // end has begun: no new stream is served
	handlers sync.WaitGroup // the streams served on e, until each has torn down
}

// teardown bounds how long end waits for e's streams: a pty session's
// SIGHUP, its SIGKILL after 5 s, and its reap (docs/04).
const teardown = 10 * time.Second

// serve runs a stream's handler on e, unless e is ending, and counts it
// until it returns.
func (e *enrolment) serve(c *stream.Conn, handle func()) {
	e.serving.Lock()
	if e.ending {
		e.serving.Unlock()
		c.Close()
		return
	}
	e.handlers.Add(1)
	e.serving.Unlock()
	defer e.handlers.Done()
	handle()
}

// end stops what runs on e and waits, up to teardown, for its streams to
// tear down, so that their processes are gone before the daemon is; its
// store stays open for the caller to close.
func (e *enrolment) end() {
	e.serving.Lock()
	e.ending = true
	e.serving.Unlock()
	e.cancel()
	e.node.Close()
	done := make(chan struct{})
	go func() { e.handlers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(teardown):
	}
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

// enrollAs enrolls with f, its entry queued for the directory, first ending
// the enrolment a re-join replaces. d.enrolling must be held.
func (d *daemon) enrollAs(f *identity.Fleet, rejoin bool) (*enrolment, error) {
	if rejoin {
		if err := d.unenroll(false); err != nil {
			return nil, err
		}
	}
	return d.enroll(f, &f.Entry)
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

// adding is the get that signs the member entry r.
func adding(r identity.Record) ceremony.Request {
	return ceremony.Request{Kind: ceremony.Add, Label: r.Label, PeerID: r.PeerID, Challenge: r.Challenge()}
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
