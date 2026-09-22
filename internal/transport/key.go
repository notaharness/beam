// Package transport carries beam's streams over tailcat (docs/03): one Server
// per machine with its node key, one Client per dialed peer with a key of its
// own, and admission of every tunnel through hello.
package transport

import (
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// Port is the one tunnel port every stream uses.
const Port = 7000

// Key is a machine's node key with the pre-shared key and DERP region that
// complete its address: what key.json holds.
type Key struct {
	pk tailcat.PrivateKey
}

// NewKey generates a node key homed on region.
func NewKey(region *tailcfg.DERPRegion) *Key {
	pk := tailcat.NewPrivateKey()
	pk.Public.Region = []*tailcfg.DERPRegion{region}
	return &Key{*pk}
}

// Address is the tailcat address that reaches this machine's Server.
func (k *Key) Address() string {
	return string(k.pk.Public.Addr())
}

// NodePublic is the public half of the node key.
func (k *Key) NodePublic() [32]byte {
	return raw(k.pk.Private.Public())
}

// raw is a node public key's 32 bytes.
func raw(k key.NodePublic) [32]byte {
	return [32]byte(k.AppendTo(nil))
}
