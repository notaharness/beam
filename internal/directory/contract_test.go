//go:build beamtest

package directory_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/fakeworker"
	"github.com/notaharness/beam/internal/identity"
)

// worker is the directory under test: the real worker under miniflare when
// BEAM_WORKER_URL names it (CI), else the fake, which this holds to the same
// contract.
func worker(t *testing.T) directory.Client {
	if u := os.Getenv("BEAM_WORKER_URL"); u != "" {
		return directory.Client{URL: u}
	}
	s := httptest.NewServer(fakeworker.New())
	t.Cleanup(s.Close)
	return directory.Client{URL: s.URL}
}

// owner is a fleet's passkey and what it yields.
type owner struct {
	auth        *identity.Authenticator
	cred        identity.Credential
	kDir, tRead []byte
}

func newOwner() owner {
	a := identity.NewAuthenticator()
	kDir, tRead := identity.DirectoryKeys(a.PRF([]byte(identity.PRFSalt)))
	return owner{a, a.Credential(), kDir, tRead}
}

// entry is a signed record of kind for a random peer.
func (o owner) entry(kind string) identity.Record {
	id := make([]byte, 16)
	rand.Read(id)
	r := identity.Record{V: 1, Kind: kind, PeerID: hex.EncodeToString(id), IssuedAt: 1}
	if kind == identity.Member {
		r.Label = "m"
	}
	o.auth.SignRecord(&r)
	return r
}

func (o owner) sealed(r identity.Record) directory.Entry {
	return directory.NewEntry(o.kDir, o.cred.FleetID(), r)
}

// register creates o's fleet with first.
func (o owner) register(t *testing.T, c directory.Client, first identity.Record) {
	t.Helper()
	if err := c.Register(context.Background(), o.cred, o.tRead, o.sealed(first)); err != nil {
		t.Fatal(err)
	}
}

// docs/09 Routes: a fleet is created once with its first entry and read back
// by its read token alone; every token that opens nothing is refused alike.
func TestRegisterAndRead(t *testing.T) {
	c, o := worker(t), newOwner()
	first := o.entry(identity.Member)
	o.register(t, c, first)
	if err := c.Register(context.Background(), o.cred, o.tRead, o.sealed(first)); !errors.Is(err, directory.ErrExists) {
		t.Errorf("second registration: %v, want exists", err)
	}
	p, err := c.Read(context.Background(), o.tRead)
	if err != nil {
		t.Fatal(err)
	}
	if p.FleetID != o.cred.FleetID() || p.CredentialID != o.cred.ID || len(p.Entries) != 1 || p.Entries[0].Seq != 1 {
		t.Fatalf("page %+v", p)
	}
	if rs := p.Records(o.kDir); len(rs) != 1 || rs[0].PeerID != first.PeerID {
		t.Fatalf("records %+v", rs)
	}
	for name, token := range map[string][]byte{"unknown": newOwner().tRead, "short": {1, 2, 3}, "empty": nil} {
		if _, err := c.Read(context.Background(), token); !errors.Is(err, directory.ErrUnauthorized) {
			t.Errorf("%s token: %v, want unauthorized", name, err)
		}
	}
}

// docs/09 Routes: a registration's first entry must be a member statement
// signed by the credential it registers.
func TestRegisterVerifies(t *testing.T) {
	c, o := worker(t), newOwner()
	for name, first := range map[string]directory.Entry{
		"a revocation":        o.sealed(o.entry(identity.Revoke)),
		"another's assertion": newOwner().sealed(newOwner().entry(identity.Member)),
	} {
		var ref *directory.Refused
		if err := c.Register(context.Background(), o.cred, o.tRead, first); !errors.As(err, &ref) || ref.Status != 403 {
			t.Errorf("%s: %v, want 403", name, err)
		}
	}
}

// docs/09 Routes: an append needs a tap: an assertion by the fleet's
// credential over the statement's hash in the domain of its kind. It is
// idempotent by statement hash.
func TestAppend(t *testing.T) {
	c, o := worker(t), newOwner()
	o.register(t, c, o.entry(identity.Member))
	ctx, fleet := context.Background(), o.cred.FleetID()
	member, revoke := o.entry(identity.Member), o.entry(identity.Revoke)
	for i, r := range []identity.Record{member, revoke, member} {
		seq, err := c.Append(ctx, fleet, o.sealed(r))
		if want := []int64{2, 3, 2}[i]; err != nil || seq != want {
			t.Fatalf("append %d: seq %d, %v; want %d", i, seq, err, want)
		}
	}
	wrongKind := o.sealed(o.entry(identity.Revoke))
	wrongKind.Kind = identity.Member
	big := o.sealed(o.entry(identity.Member))
	big.Blob = base64.RawURLEncoding.EncodeToString(make([]byte, 8193))
	for name, tc := range map[string]struct {
		e      directory.Entry
		status int
	}{
		"the other domain":   {wrongKind, 403},
		"another credential": {newOwner().sealed(newOwner().entry(identity.Member)), 403},
		"a blob over 8 KiB":  {big, 413},
	} {
		var ref *directory.Refused
		if _, err := c.Append(ctx, fleet, tc.e); !errors.As(err, &ref) || ref.Status != tc.status {
			t.Errorf("%s: %v, want %d", name, err, tc.status)
		}
	}
	if _, err := c.Append(ctx, newOwner().cred.FleetID(), o.sealed(member)); !errors.Is(err, directory.ErrNoFleet) {
		t.Errorf("unknown fleet: %v", err)
	}
	p, err := c.Read(ctx, o.tRead)
	if err != nil || len(p.Entries) != 3 {
		t.Fatalf("read %d entries, %v", len(p.Entries), err)
	}
}

// docs/02 The directory: a reader discards a validly signed entry whose blob
// holds another statement.
func TestJunkDiscarded(t *testing.T) {
	c, o := worker(t), newOwner()
	o.register(t, c, o.entry(identity.Member))
	signed, other := o.sealed(o.entry(identity.Member)), o.sealed(o.entry(identity.Member))
	signed.Blob = other.Blob
	if _, err := c.Append(context.Background(), o.cred.FleetID(), signed); err != nil {
		t.Fatal(err)
	}
	p, err := c.Read(context.Background(), o.tRead)
	if err != nil || len(p.Entries) != 2 || len(p.Records(o.kDir)) != 1 {
		t.Fatalf("%d entries, %d records, %v; want the junk stored and discarded", len(p.Entries), len(p.Records(o.kDir)), err)
	}
}

// docs/09 Routes: pages hold at most 500 entries and Read follows them all.
func TestReadPages(t *testing.T) {
	c, o := worker(t), newOwner()
	o.register(t, c, o.entry(identity.Member))
	for range 520 {
		if _, err := c.Append(context.Background(), o.cred.FleetID(), o.sealed(o.entry(identity.Member))); err != nil {
			t.Fatal(err)
		}
	}
	p, err := c.Read(context.Background(), o.tRead)
	if err != nil || len(p.Entries) != 521 || p.Entries[520].Seq != 521 {
		t.Fatalf("%d entries, %v", len(p.Entries), err)
	}
}
