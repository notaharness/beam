package control

import (
	"sort"
	"strings"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

type opFunc func(d *daemon, cc *clientConn, r request) (any, error)

// Ops an unenrolled daemon answers; ceremony ops join them in M4.
var anytimeOps = map[string]opFunc{
	"status":           opStatus,
	"events.subscribe": func(d *daemon, cc *clientConn, _ request) (any, error) { d.subscribe(cc); return struct{}{}, nil },
	"daemon.shutdown":  func(*daemon, *clientConn, request) (any, error) { return struct{}{}, nil },
}

var enrolledOps = map[string]opFunc{
	"peers":        opPeers,
	"peer.resolve": opResolve,
	"peer.alias":   opAlias,
	"peer.grant":   opGrant,
	"pty.open":     opPTYOpen,
	"exec.open":    opExecOpen,
	"stream.close": opStreamClose,
}

func (d *daemon) op(cc *clientConn, r request) (any, error) {
	if f, ok := anytimeOps[r.Op]; ok {
		return f(d, cc, r)
	}
	f, ok := enrolledOps[r.Op]
	switch {
	case !ok:
		return nil, fail("params", "unknown op "+r.Op)
	case !d.enrolled():
		return nil, fail("not-enrolled", "")
	}
	return f(d, cc, r)
}

type statusResult struct {
	Version  string     `json:"version"`
	Ready    bool       `json:"ready"`
	Enrolled bool       `json:"enrolled"`
	PeerID   string     `json:"peerId,omitempty"`
	Label    string     `json:"label,omitempty"`
	FleetID  string     `json:"fleetId,omitempty"`
	Address  string     `json:"address,omitempty"`
	DERP     derpStatus `json:"derp"`
	Peers    peerCounts `json:"peers"`
}

type derpStatus struct {
	Region string `json:"region"`
	Source string `json:"source"`
}

type peerCounts struct {
	Connected int `json:"connected"`
	Offline   int `json:"offline"`
	Revoked   int `json:"revoked"`
}

func opStatus(d *daemon, _ *clientConn, _ request) (any, error) {
	res := statusResult{Version: d.o.Version}
	if !d.enrolled() {
		return res, nil
	}
	e := d.fleet.Entry
	res.Ready, res.Enrolled = true, true
	res.PeerID, res.Label, res.FleetID, res.Address = e.PeerID, e.Label, d.fleet.FleetID, e.Address
	res.DERP = derpStatus{Region: d.key.RegionCode(), Source: "key.json"}
	views, err := d.peerViews()
	for _, v := range views {
		switch v.State {
		case stateConnected:
			res.Peers.Connected++
		case stateRevoked:
			res.Peers.Revoked++
		default:
			res.Peers.Offline++
		}
	}
	return res, err
}

// PeerView is one peer as the socket shows it (docs/06).
type PeerView struct {
	PeerID     string      `json:"peerId"`
	Label      string      `json:"label"`
	Alias      *string     `json:"alias"`
	State      string      `json:"state"`
	Inbound    bool        `json:"inbound"`
	Path       string      `json:"path"`
	LastSeenAt *int64      `json:"lastSeenAt"`
	Grant      string      `json:"grant"`
	RevokedAt  *int64      `json:"revokedAt"`
	PinnedAt   int64       `json:"pinnedAt"`
	Queue      QueueCounts `json:"queue"`
}

// QueueCounts are a peer's mailbox counts; the mailbox arrives in M3.
type QueueCounts struct {
	Outbound int `json:"outbound"`
	Inbound  int `json:"inbound"`
	Refused  int `json:"refused"`
}

func (d *daemon) view(p store.Peer) PeerView {
	v := PeerView{PeerID: p.Entry.PeerID, Label: p.Entry.Label, Alias: p.Alias, State: stateOffline,
		LastSeenAt: p.LastSeenAt, Grant: grantOf(p.Grant), RevokedAt: p.RevokedAt, PinnedAt: p.PinnedAt,
		Path: d.node.Path(p.Entry.PeerID)}
	d.mu.Lock()
	if ps, ok := d.peers[v.PeerID]; ok {
		v.State = ps.state
	}
	v.Inbound = d.inbound[v.PeerID] > 0
	d.mu.Unlock()
	if p.Revoked {
		v.State = stateRevoked
	}
	return v
}

func (d *daemon) peerView(peerID string) PeerView {
	p, _, _ := d.store.Peer(peerID)
	return d.view(p)
}

func (d *daemon) peerViews() ([]PeerView, error) {
	ps, err := d.store.Peers()
	views := make([]PeerView, len(ps))
	for i, p := range ps {
		views[i] = d.view(p)
	}
	return views, err
}

type peersResult struct {
	Peers []PeerView `json:"peers"`
	Next  string     `json:"next,omitempty"`
}

func opPeers(d *daemon, _ *clientConn, r request) (any, error) {
	limit := r.Limit
	if limit == 0 {
		limit = 200
	}
	if limit < 0 || limit > 200 {
		return nil, fail("params", "limit is 1–200")
	}
	views, err := d.peerViews()
	i := sort.Search(len(views), func(i int) bool { return views[i].PeerID > r.Cursor })
	res := peersResult{Peers: views[i:min(i+limit, len(views))]}
	if i+limit < len(views) {
		res.Next = res.Peers[len(res.Peers)-1].PeerID
	}
	return res, err
}

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

func opAlias(d *daemon, _ *clientConn, r request) (any, error) {
	id, err := d.resolve(r.Peer)
	if err != nil {
		return nil, err
	}
	if r.Alias != nil && !identity.ValidLabel(*r.Alias) {
		return nil, fail("params", "an alias follows the label rules")
	}
	if err := d.store.SetAlias(id, r.Alias); err != nil {
		return nil, err
	}
	d.emitPeer(id)
	return struct{}{}, nil
}

// opGrant sets a grant and ends the peer's open streams it no longer allows.
func opGrant(d *daemon, _ *clientConn, r request) (any, error) {
	if grantOf(r.Grant) != r.Grant {
		return nil, fail("params", "grant is all, msg or none")
	}
	id, err := d.resolve(r.Peer)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	err = d.store.SetGrant(id, r.Grant)
	if err == nil && r.Grant != store.GrantAll {
		d.closeShellsLocked(id)
	}
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	d.emitPeer(id)
	return struct{}{}, nil
}

type openResult struct {
	StreamID string `json:"streamId"`
}

func (d *daemon) open(r request, h stream.Header) (any, error) {
	id, err := d.resolve(r.Peer)
	if err != nil {
		return nil, err
	}
	if d.isRevoked(id) {
		return nil, fail("revoked-peer", id)
	}
	return openResult{d.reserve(id, h)}, nil
}

func opPTYOpen(d *daemon, _ *clientConn, r request) (any, error) {
	return d.open(r, stream.Header{V: 1, Kind: stream.KindPTY, Argv: r.Argv, Cwd: r.Cwd, Env: r.Env, Cols: r.Cols, Rows: r.Rows})
}

func opExecOpen(d *daemon, _ *clientConn, r request) (any, error) {
	if len(r.Argv) == 0 {
		return nil, fail("params", "argv is required")
	}
	return d.open(r, stream.Header{V: 1, Kind: stream.KindExec, Argv: r.Argv, Cwd: r.Cwd, Env: r.Env})
}

func opStreamClose(d *daemon, _ *clientConn, r request) (any, error) {
	if !d.closeStream(r.StreamID) {
		return nil, fail("params", "no such stream")
	}
	return struct{}{}, nil
}
