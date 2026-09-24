package control

import (
	"sort"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
)

type opFunc func(d *daemon, cc *clientConn, r request) (any, error)

// enrolledOp is an op on the enrolment under way when it arrived.
type enrolledOp func(d *daemon, e *enrolment, cc *clientConn, r request) (any, error)

// Ops an unenrolled daemon answers.
var anytimeOps = map[string]opFunc{
	"status":           opStatus,
	"events.subscribe": func(d *daemon, cc *clientConn, _ request) (any, error) { d.subscribe(cc); return struct{}{}, nil },
	"daemon.shutdown":  func(*daemon, *clientConn, request) (any, error) { return struct{}{}, nil },

	"init.start":      opInitStart,
	"init.wait":       func(d *daemon, cc *clientConn, _ request) (any, error) { return d.finish("init", cc) },
	"join.start":      opJoinStart,
	"join.wait":       func(d *daemon, cc *clientConn, _ request) (any, error) { return d.finish("join", cc) },
	"ceremony.cancel": opCeremonyCancel,
	"fleet.reset":     opFleetReset,
}

var enrolledOps = map[string]enrolledOp{
	"peers":        opPeers,
	"peer.resolve": opResolve,
	"peer.alias":   opAlias,
	"peer.grant":   opGrant,
	"pty.open":     opPTYOpen,
	"exec.open":    opExecOpen,
	"stream.close": opStreamClose,

	"msg.send":      opMsgSend,
	"msg.subscribe": opMsgSubscribe,
	"msg.ack":       opMsgAck,
	"msg.defer":     opMsgDefer,
	"msg.queue":     opMsgQueue,

	"revoke.start": opRevokeStart,
	"revoke.wait":  func(d *daemon, _ *enrolment, cc *clientConn, _ request) (any, error) { return d.finish("revoke", cc) },
}

// op runs r. Every op but status waits for the daemon's start: the socket is
// up before the transport (docs/06), and an op meanwhile would find the
// daemon unenrolled that is about to be enrolled.
func (d *daemon) op(cc *clientConn, r request) (any, error) {
	if r.Op != "status" {
		select {
		case <-d.started:
		case <-d.ctx.Done():
			return nil, fail("internal", "the daemon stopped while starting")
		}
	}
	if f, ok := anytimeOps[r.Op]; ok {
		return f(d, cc, r)
	}
	f, ok := enrolledOps[r.Op]
	e := d.enrolment()
	switch {
	case !ok:
		return nil, fail("params", "unknown op "+r.Op)
	case e == nil:
		return nil, fail("not-enrolled", "")
	}
	return f(d, e, cc, r)
}

type statusResult struct {
	Version    string     `json:"version"`
	Ready      bool       `json:"ready"`
	Enrolled   bool       `json:"enrolled"`
	Generation uint64     `json:"generation"`
	PeerID     string     `json:"peerId,omitempty"`
	Label      string     `json:"label,omitempty"`
	FleetID    string     `json:"fleetId,omitempty"`
	Address    string     `json:"address,omitempty"`
	DERP       derpStatus `json:"derp"`
	Peers      peerCounts `json:"peers"`
}

type derpStatus struct {
	Region string `json:"region"`
	Source string `json:"source"`
}

type peerCounts struct {
	Connected      int `json:"connected"`
	Offline        int `json:"offline"`
	Revoked        int `json:"revoked"`
	RevokedByFleet int `json:"revokedByFleet"`
}

func opStatus(d *daemon, _ *clientConn, _ request) (any, error) {
	d.mu.Lock()
	e, gen := d.en, d.gen // the enrolment and its generation together
	d.mu.Unlock()
	res := statusResult{Version: d.o.Version, Generation: gen}
	if e == nil {
		return res, nil
	}
	me := e.fleet.Entry
	res.Ready, res.Enrolled = true, true
	res.PeerID, res.Label, res.FleetID, res.Address = me.PeerID, me.Label, e.fleet.FleetID, me.Address
	res.DERP = derpStatus{Region: e.key.RegionCode(), Source: "key.json"}
	views, err := d.peerViews(e)
	for _, v := range views {
		switch v.State {
		case stateConnected:
			res.Peers.Connected++
		case stateRevoked:
			res.Peers.Revoked++
		case stateRevokedByFleet:
			res.Peers.RevokedByFleet++
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

// QueueCounts are a peer's mail counts: outbound not yet acked, inbound not
// yet taken, and inbound an application refused.
type QueueCounts struct {
	Outbound int `json:"outbound"`
	Inbound  int `json:"inbound"`
	Refused  int `json:"refused"`
}

func (d *daemon) view(e *enrolment, p store.Peer) PeerView {
	v := PeerView{PeerID: p.Entry.PeerID, Label: p.Entry.Label, Alias: p.Alias, State: stateOffline,
		LastSeenAt: p.LastSeenAt, Grant: grantOf(p.Grant), RevokedAt: p.RevokedAt, PinnedAt: p.PinnedAt,
		Path: e.node.Path(p.Entry.PeerID)}
	d.mu.Lock()
	if ps, ok := d.peers[v.PeerID]; ok {
		v.State = ps.state
	}
	v.Inbound = d.inbound[v.PeerID] > 0
	d.mu.Unlock()
	q := &v.Queue
	q.Outbound, q.Inbound, q.Refused, _ = e.store.QueueCounts(v.PeerID) // zero counts if the store fails
	if p.Revoked {
		v.State = stateRevoked
	}
	return v
}

func (d *daemon) peerView(e *enrolment, peerID string) PeerView {
	p, _, _ := e.store.Peer(peerID)
	return d.view(e, p)
}

func (d *daemon) peerViews(e *enrolment) ([]PeerView, error) {
	ps, err := e.store.Peers()
	views := make([]PeerView, len(ps))
	for i, p := range ps {
		views[i] = d.view(e, p)
	}
	return views, err
}

type peersResult struct {
	Peers []PeerView `json:"peers"`
	Next  string     `json:"next,omitempty"`
}

func opPeers(d *daemon, e *enrolment, _ *clientConn, r request) (any, error) {
	limit := r.Limit
	if limit == 0 {
		limit = 200
	}
	if limit < 0 || limit > 200 {
		return nil, fail("params", "limit is 1–200")
	}
	views, err := d.peerViews(e)
	i := sort.Search(len(views), func(i int) bool { return views[i].PeerID > r.Cursor })
	res := peersResult{Peers: views[i:min(i+limit, len(views))]}
	if i+limit < len(views) {
		res.Next = res.Peers[len(res.Peers)-1].PeerID
	}
	return res, err
}

type resolveResult struct {
	PeerID string `json:"peerId"`
}

func opResolve(_ *daemon, e *enrolment, _ *clientConn, r request) (any, error) {
	id, err := e.resolve(r.Peer)
	return resolveResult{id}, err
}

func opAlias(d *daemon, e *enrolment, _ *clientConn, r request) (any, error) {
	id, err := e.resolve(r.Peer)
	if err != nil {
		return nil, err
	}
	if r.Alias != nil && !identity.ValidLabel(*r.Alias) {
		return nil, fail("params", "an alias follows the label rules")
	}
	if err := e.store.SetAlias(id, r.Alias); err != nil {
		return nil, err
	}
	d.emitPeer(e, id)
	return struct{}{}, nil
}

// opGrant sets a grant and ends the peer's open streams it no longer allows.
func opGrant(d *daemon, e *enrolment, _ *clientConn, r request) (any, error) {
	if grantOf(r.Grant) != r.Grant {
		return nil, fail("params", "grant is all, msg or none")
	}
	id, err := e.resolve(r.Peer)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	err = e.store.SetGrant(id, r.Grant)
	if err == nil {
		d.closeUngrantedLocked(id, r.Grant)
	}
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	d.emitPeer(e, id)
	return struct{}{}, nil
}

type openResult struct {
	StreamID string `json:"streamId"`
}

func (d *daemon) open(e *enrolment, r request, h stream.Header) (any, error) {
	id, err := e.resolve(r.Peer)
	if err != nil {
		return nil, err
	}
	if e.isRevoked(id) {
		return nil, fail("revoked-peer", id)
	}
	return openResult{d.reserve(e, id, h)}, nil
}

func opPTYOpen(d *daemon, e *enrolment, _ *clientConn, r request) (any, error) {
	return d.open(e, r, stream.Header{V: 1, Kind: stream.KindPTY, Argv: r.Argv, Cwd: r.Cwd, Env: r.Env, Cols: r.Cols, Rows: r.Rows})
}

func opExecOpen(d *daemon, e *enrolment, _ *clientConn, r request) (any, error) {
	if len(r.Argv) == 0 {
		return nil, fail("params", "argv is required")
	}
	return d.open(e, r, stream.Header{V: 1, Kind: stream.KindExec, Argv: r.Argv, Cwd: r.Cwd, Env: r.Env})
}

func opStreamClose(d *daemon, _ *enrolment, _ *clientConn, r request) (any, error) {
	if !d.closeStream(r.StreamID) {
		return nil, fail("params", "no such stream")
	}
	return struct{}{}, nil
}
