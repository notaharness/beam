package control

import (
	"strings"

	"github.com/notaharness/beam/internal/store"
)

// resolve maps a peer argument to a peer id (docs/06, peer.resolve): a full id
// or ≥ 8-character hex prefix first, then an alias or label.
func (d *daemon) resolve(arg string) (string, error) {
	ps, err := d.store.Peers()
	if err != nil {
		return "", err
	}
	matches := matching(ps, arg)
	switch len(matches) {
	case 0:
		return "", fail("unknown-peer", arg)
	case 1:
		return matches[0].Entry.PeerID, nil
	}
	var cands []string
	for _, p := range matches {
		cands = append(cands, p.Entry.PeerID+" ("+p.Entry.Label+")")
	}
	return "", fail("ambiguous-peer", strings.Join(cands, ", "))
}

// matching is the peers arg names: by peer id prefix of 8 or more hex digits
// if any match, else by alias or label.
func matching(ps []store.Peer, arg string) []store.Peer {
	var byPrefix, byName []store.Peer
	for _, p := range ps {
		if len(arg) >= 8 && isHex(arg) && strings.HasPrefix(p.Entry.PeerID, arg) {
			byPrefix = append(byPrefix, p)
		}
		if p.Alias != nil && *p.Alias == arg || p.Entry.Label == arg {
			byName = append(byName, p)
		}
	}
	if len(byPrefix) > 0 {
		return byPrefix
	}
	return byName
}

func isHex(s string) bool {
	return strings.Trim(s, "0123456789abcdef") == ""
}

type resolveResult struct {
	PeerID string `json:"peerId"`
}

func opResolve(d *daemon, _ *clientConn, r request) (any, error) {
	id, err := d.resolve(r.Peer)
	return resolveResult{id}, err
}
