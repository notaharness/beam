package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

// DefaultDERPMap is tailcat's DERP map (docs/03).
const DefaultDERPMap = tailcat.DefaultDERPMapURL

// DERPMapCache keeps fetched DERP maps across runs, as tailcat.DERPMapCache.
type DERPMapCache interface {
	Get(url string) (data []byte, etag string, storedAt time.Time, ok bool)
	Put(url string, data []byte, etag string) error
}

// HomeKey homes k on the DERP map at mapURL (docs/03, "Addresses"): k itself
// while its region is one of the map's, else k's node key on the region that
// answers fastest; a nil k is a new node key.
func HomeKey(ctx context.Context, mapURL string, cache DERPMapCache, k *Key) (*Key, error) {
	dm, err := tailcat.FetchDERPMap(ctx, tailcat.DERPMapURL(mapURL), tailcat.DERPMapCache(cache))
	if err != nil {
		return nil, err
	}
	if k != nil && k.on(dm) {
		return k, nil
	}
	id, err := tailcat.PickBestRegion(ctx, dm)
	if err != nil {
		return nil, err
	}
	if id == 0 || dm.Regions[id] == nil {
		return nil, errors.New("no DERP region answered")
	}
	if k == nil {
		return NewKey(dm.Regions[id]), nil
	}
	return k.OnRegion(dm.Regions[id]), nil
}

// on reports whether k's region is, exactly, one of dm's.
func (k *Key) on(dm *tailcfg.DERPMap) bool {
	mine, _ := json.Marshal(k.pk.Public.Region[0])
	for _, r := range dm.Regions {
		if theirs, _ := json.Marshal(r); bytes.Equal(mine, theirs) {
			return true
		}
	}
	return false
}
