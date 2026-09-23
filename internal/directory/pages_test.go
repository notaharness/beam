//go:build beamtest

package directory_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/fakeworker"
	"github.com/notaharness/beam/internal/identity"
)

// docs/09 Routes: pages hold at most 500 entries and Read follows them all.
// Against the fake only: the worker's rate limit keeps 520 appends out of
// the contract, and the worker's own tests page a seeded fleet.
func TestReadPages(t *testing.T) {
	s := httptest.NewServer(fakeworker.New())
	defer s.Close()
	c, o := directory.Client{URL: s.URL}, newOwner()
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
