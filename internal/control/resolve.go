package control

import (
	"strings"

	"github.com/notaharness/beam/internal/store"
)

// resolve maps a peer argument to a peer id (docs/06, peer.resolve): a full id
// or ≥ 8-character hex prefix first, then an alias or label.
func (e *enrolment) resolve(arg string) (string, error) {
	ps, err := e.store.Peers()
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
// if any match, else by alias or label, where a revoked peer counts only if
// no other does: after a re-join into the same fleet, revoking the old
// identity leaves its label to the new one.
func matching(ps []store.Peer, arg string) []store.Peer {
	var byPrefix, byName, revoked []store.Peer
	for _, p := range ps {
		switch {
		case len(arg) >= 8 && isHex(arg) && strings.HasPrefix(p.Entry.PeerID, arg):
			byPrefix = append(byPrefix, p)
		case !named(p, arg):
		case p.Revoked:
			revoked = append(revoked, p)
		default:
			byName = append(byName, p)
		}
	}
	switch {
	case len(byPrefix) > 0:
		return byPrefix
	case len(byName) > 0:
		return byName
	}
	return revoked
}

func named(p store.Peer, arg string) bool {
	return p.Alias != nil && *p.Alias == arg || p.Entry.Label == arg
}

func isHex(s string) bool {
	return strings.Trim(s, "0123456789abcdef") == ""
}
