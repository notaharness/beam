// Package devderp runs an in-process DERP relay with loopback STUN for tests,
// as tailcat's runDevDERP does, and keeps engines under test off the real
// network, as tailcat's TestMain does.
package devderp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"

	"tailscale.com/derp/derpserver"
	"tailscale.com/envknob"
	"tailscale.com/net/netcheck"
	"tailscale.com/net/stun"
	"tailscale.com/syncs"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// Relay is a running dev DERP.
type Relay struct {
	Region *tailcfg.DERPRegion
	MapURL string // a DERP map of Region alone, as beam daemon --derp-map takes one
	derp   *derpserver.Server
	https  *httptest.Server
	maps   *httptest.Server
	stun   *net.UDPConn
}

// Start starts a relay on loopback.
func Start(logf logger.Logf) (*Relay, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	uln, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		ln.Close()
		return nil, err
	}
	d := derpserver.New(key.NewNode(), logf)
	srv := httptest.NewUnstartedServer(derpserver.Handler(d))
	srv.Listener = ln
	srv.Config.ErrorLog = logger.StdLogger(logf)
	srv.Config.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	srv.StartTLS()
	go serveSTUN(uln)
	r := &Relay{
		derp:  d,
		https: srv,
		stun:  uln,
		Region: &tailcfg.DERPRegion{
			RegionID:   1,
			RegionCode: "dev",
			Nodes: []*tailcfg.DERPNode{{
				Name:             "d1",
				RegionID:         1,
				HostName:         "127.0.0.1",
				IPv4:             "127.0.0.1",
				IPv6:             "none", // netcheck's "no v6 probes"; anything else waits out its 3 s timeout
				STUNPort:         uln.LocalAddr().(*net.UDPAddr).Port,
				DERPPort:         ln.Addr().(*net.TCPAddr).Port,
				InsecureForTests: true,
			}},
		},
	}
	m, _ := json.Marshal(tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{1: r.Region}})
	r.maps = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(m) }))
	r.MapURL = r.maps.URL + "/derpmap.json"
	return r, nil
}

// serveSTUN answers binding requests. Without it netcheck spends seconds timing
// out before an engine connects to its home relay.
func serveSTUN(c *net.UDPConn) {
	var buf [1500]byte
	for {
		n, src, err := c.ReadFromUDPAddrPort(buf[:])
		if err != nil {
			return
		}
		if txid, err := stun.ParseBindingRequest(buf[:n]); err == nil {
			_, _ = c.WriteToUDPAddrPort(stun.Response(txid, src), src)
		}
	}
}

// Close stops the relay.
func (r *Relay) Close() {
	r.stun.Close()
	r.maps.Close()
	r.https.Close()
	r.derp.Close()
}

// Isolate keeps engines off the real network: no port-mapping probes of the
// default gateway and no captive-portal requests. Call it from TestMain.
func Isolate() {
	envknob.Setenv("IN_TS_TEST", "true")
	netcheck.HookStartCaptivePortalDetection.SetForTest(func(context.Context, *netcheck.Client, *tailcfg.DERPMap, tailcfg.DERPRegionID, func(bool)) (<-chan struct{}, func()) {
		return syncs.ClosedChan(), func() {}
	})
}

// ForceRelay blocks UDP for every engine in the process, so all traffic goes
// through the relay. Call it from TestMain.
func ForceRelay() {
	envknob.Setenv("TS_DEBUG_ALWAYS_USE_DERP", "true")
}
