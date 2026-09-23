// Package transport carries beam's streams over tailcat (docs/03): one Server
// per machine with its node key, one Client per dialed peer with a key of its
// own, and admission of every tunnel through hello.
package transport

import (
	"encoding/json"
	"errors"

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

// OnRegion is k's node key homed on region: the same peer at a new address.
func (k *Key) OnRegion(region *tailcfg.DERPRegion) *Key {
	moved := *k
	moved.pk.Public.Region = []*tailcfg.DERPRegion{region}
	return &moved
}

// MarshalJSON is key.json: tailcat's PrivateKey, region included.
func (k *Key) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.pk)
}

// UnmarshalJSON reads key.json, refusing a key without exactly one region.
func (k *Key) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &k.pk); err != nil {
		return err
	}
	if k.pk.Private.IsZero() || len(k.pk.Public.Region) != 1 || k.pk.Public.PresharedKey.IsZero() {
		return errors.New("key.json: need a node key, a pre-shared key and one DERP region")
	}
	return nil
}

// RegionCode names the DERP region the key is homed on.
func (k *Key) RegionCode() string {
	return k.pk.Public.Region[0].RegionCode
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

// AddressKey returns the node key an address reaches. It refuses an address
// that does not parse or carries no pre-shared key.
func AddressKey(address string) ([32]byte, error) {
	ci, err := tailcat.ParseAddr(tailcat.Addr(address))
	if err != nil {
		return [32]byte{}, err
	}
	if ci.PresharedKey.IsZero() {
		return [32]byte{}, errors.New("address has no pre-shared key")
	}
	return raw(ci.ServerPublic.NodePublic), nil
}
