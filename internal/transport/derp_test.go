package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/devderp"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

// mapCache is a DERPMapCache in memory.
type mapCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (c *mapCache) Get(url string) ([]byte, string, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[url]
	return b, "", time.Now(), ok
}

func (c *mapCache) Put(url string, data []byte, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[url] = data
	return nil
}

// docs/03 "Addresses": a key stays on its region while the map has it and
// moves, keeping its node key, when the map is another.
func TestHomeKey(t *testing.T) {
	var relays [2]*devderp.Relay
	for i := range relays {
		r, err := devderp.Start(logger.Discard)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		relays[i] = r
	}
	ctx, cache := context.Background(), &mapCache{m: map[string][]byte{}}
	k, err := HomeKey(ctx, relays[0].MapURL, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	if same, err := HomeKey(ctx, relays[0].MapURL, cache, k); err != nil || same != k {
		t.Errorf("on its own map: %p, %v, want the key itself", same, err)
	}
	moved, err := HomeKey(ctx, relays[1].MapURL, cache, k)
	if err != nil || moved.NodePublic() != k.NodePublic() || moved.Address() == k.Address() {
		t.Errorf("on another map: %v; node key kept %v, address kept %v", err, moved.NodePublic() == k.NodePublic(), moved.Address() == k.Address())
	}
}

// docs/03 "DERP": outside beamtest builds a DERP map is fetched over https
// alone and no node of it is InsecureForTests, the dev DERP's setting.
func TestFetchMapOutsideTests(t *testing.T) {
	relay, err := devderp.Start(logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay.Close)
	ctx, cache := context.Background(), &mapCache{m: map[string][]byte{}}
	if _, err := fetchMap(ctx, relay.MapURL, cache, false); err == nil {
		t.Errorf("fetched %s outside a beamtest build", relay.MapURL)
	}
	// A fresh cached map is used without a fetch, so an https URL needs no server.
	const url = "https://derp.invalid/derpmap.json"
	cache.m[url] = mustMap(t, relay.Region)
	dm, err := fetchMap(ctx, url, cache, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := dm.Regions[relay.Region.RegionID].Nodes[0]; n.InsecureForTests {
		t.Errorf("node %s kept InsecureForTests outside a beamtest build", n.Name)
	}
	if dm, err = fetchMap(ctx, relay.MapURL, cache, true); err != nil || !dm.Regions[relay.Region.RegionID].Nodes[0].InsecureForTests {
		t.Errorf("the dev DERP's map in a beamtest build: %v", err)
	}
}

func mustMap(t *testing.T, r *tailcfg.DERPRegion) []byte {
	t.Helper()
	b, err := json.Marshal(tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{r.RegionID: r}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
