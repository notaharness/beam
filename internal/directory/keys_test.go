//go:build beamtest

package directory_test

import (
	"context"
	"testing"

	"github.com/notaharness/beam/internal/identity"
)

// docs/02 "fleetId": the directory registers a new fleet only under CTAP2's
// encoding of its key, which the fake holds to as the worker does.
func TestRegisterCanonicalKey(t *testing.T) {
	c, o := worker(t), newOwner()
	first := o.entry(identity.Member)
	k := o.cred.PublicKey
	reordered := o
	reordered.cred.PublicKey = append([]byte{k[0], k[3], k[4], k[1], k[2]}, k[5:]...)
	if err := c.Register(context.Background(), reordered.cred, o.tRead, reordered.sealed(first)); err == nil {
		t.Errorf("registered fleet %s under a reordered key", reordered.cred.FleetID())
	}
	o.register(t, c, first)
}
