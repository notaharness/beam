package control

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
)

// sendWait is how long msg.send waits for the recipient's ack before it
// answers stored (docs/05).
const sendWait = 10 * time.Second

// msg.send outcomes (docs/05).
const (
	delivered = "delivered"
	stored    = "stored"
	rejected  = "rejected"
)

type sendResult struct {
	Outcome       string `json:"outcome"`
	To            string `json:"to"`
	PendingReason string `json:"pendingReason,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// rejection is a msg.send that stored nothing.
type rejection string

func (r rejection) Error() string { return string(r) }

func opMsgSend(d *daemon, _ *clientConn, r request) (any, error) {
	to, err := d.resolve(r.To)
	var p store.Peer
	if err == nil {
		p, _, err = d.store.Peer(to)
	}
	var oe *OpError
	switch {
	case errors.As(err, &oe) && oe.Code == "unknown-peer":
		return sendResult{Outcome: rejected, To: r.To, Reason: "unknown-peer"}, nil
	case errors.As(err, &oe):
		return nil, err
	case err != nil:
		d.o.Logf("msg.send to %s: %v", r.To, err)
		return sendResult{Outcome: rejected, To: r.To, Reason: mailbox.StorageFailure}, nil
	case p.Revoked:
		return sendResult{Outcome: rejected, To: to, Reason: "revoked-peer"}, nil
	}
	encoding := r.Encoding
	if encoding == "" {
		encoding = mailbox.UTF8
	}
	e, reason := mailbox.New(d.fleet.Entry.PeerID, to, deref(r.Topic), r.Payload, encoding, now())
	switch reason {
	case "":
	case mailbox.InvalidEnvelope:
		return nil, fail("params", "encoding is utf8 or base64, and the payload must be in it")
	default:
		return sendResult{Outcome: rejected, To: to, Reason: reason}, nil
	}
	outcome := d.send(to, e)
	outcome.To = to
	return outcome, nil
}

// send queues e for its recipient and waits up to sendWait for the ack.
func (d *daemon) send(to string, e mailbox.Envelope) sendResult {
	done := make(chan string, 1)
	var seq int64
	_, err := d.store.Enqueue(to, now(), func(s int64) ([]byte, error) {
		e.Seq, seq = s, s
		b, reason := e.Marshal()
		if reason != "" {
			return nil, rejection(reason)
		}
		d.await(to, s, done) // before the commit, so the flusher cannot settle it unseen
		return b, nil
	})
	var rej rejection
	switch {
	case errors.As(err, &rej):
		return sendResult{Outcome: rejected, Reason: string(rej)}
	case errors.Is(err, store.ErrQueueFull):
		d.unawait(to, seq)
		return sendResult{Outcome: rejected, Reason: "queue-full"}
	case err != nil:
		d.unawait(to, seq)
		d.o.Logf("msg.send to %s: %v", to[:8], err)
		return sendResult{Outcome: rejected, Reason: mailbox.StorageFailure}
	}
	if !d.wakeFlusher(to) {
		d.unawait(to, seq)
		return sendResult{Outcome: stored, PendingReason: "offline"}
	}
	select {
	case reason := <-done:
		if reason != "" {
			return sendResult{Outcome: rejected, Reason: reason}
		}
		return sendResult{Outcome: delivered}
	case <-time.After(sendWait):
		d.unawait(to, seq)
		return sendResult{Outcome: stored, PendingReason: "no-ack"}
	}
}

func (d *daemon) await(peer string, seq int64, done chan string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sends[peer] == nil {
		d.sends[peer] = map[int64]chan string{}
	}
	d.sends[peer][seq] = done
}

func (d *daemon) unawait(peer string, seq int64) {
	d.mu.Lock()
	delete(d.sends[peer], seq)
	d.mu.Unlock()
}

// settled is the flusher's report that seq left peer's queue.
func (d *daemon) settled(peer string) func(seq int64, reason string) {
	return func(seq int64, reason string) {
		d.mu.Lock()
		done, ok := d.sends[peer][seq]
		delete(d.sends[peer], seq)
		d.mu.Unlock()
		if ok {
			done <- reason
		}
	}
}

// wakeFlusher tells peer's flusher there is mail and resets its dial
// backoff. It reports whether the peer is connected.
func (d *daemon) wakeFlusher(peer string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ps, ok := d.peers[peer]
	if !ok {
		return false
	}
	d.kickLocked(ps)
	select {
	case ps.wake <- struct{}{}:
	default:
	}
	return ps.state == stateConnected
}

// flush runs peer's flusher on the tunnel this machine dialed, for as long as
// ctx lives.
func (d *daemon) flush(ctx context.Context, peer string, ps *peerState, tun *transport.Tunnel) {
	open := func(ctx context.Context) (*stream.Conn, error) {
		return tun.Open(ctx, stream.Header{V: 1, Kind: stream.KindMsg})
	}
	mailbox.Flush(ctx, d.store, peer, open, ps.wake, d.settled(peer))
}

func opMsgSubscribe(d *daemon, cc *clientConn, r request) (any, error) {
	d.at("subscribing", "")
	from := make([]string, len(r.From))
	for i, arg := range r.From {
		id, err := d.resolve(arg)
		if err != nil {
			return nil, err
		}
		from[i] = id
	}
	cc.subMu.Lock()
	defer cc.subMu.Unlock()
	if cc.closed {
		return nil, errors.New("the connection is closed")
	}
	if cc.sub != nil {
		if err := d.mail.Close(cc.sub); err != nil {
			return nil, err
		}
	}
	cc.sub = d.mail.Subscribe(r.Topic, from, func(env json.RawMessage) error { return cc.send(event{"mail", env}) })
	return struct{}{}, nil
}

func opMsgAck(d *daemon, cc *clientConn, r request) (any, error) {
	return d.settleMail(cc, func(s *mailbox.Sub) error { return d.mail.Ack(s, r.EnvelopeID) })
}

func opMsgDefer(d *daemon, cc *clientConn, r request) (any, error) {
	if len(r.Reason) > mailbox.MaxDeferReason {
		return nil, fail("params", "reason is at most 1 KiB")
	}
	return d.settleMail(cc, func(s *mailbox.Sub) error { return d.mail.Defer(s, r.EnvelopeID, r.Reason) })
}

func (d *daemon) settleMail(cc *clientConn, f func(*mailbox.Sub) error) (any, error) {
	cc.subMu.Lock()
	defer cc.subMu.Unlock()
	if cc.sub == nil {
		return nil, fail("params", "not subscribed")
	}
	err := f(cc.sub)
	if errors.Is(err, mailbox.ErrNotInFlight) {
		return nil, fail("params", err.Error())
	}
	return struct{}{}, err
}

// unsubscribeMail ends a closed connection's mail subscription, and any it
// would make later.
func (d *daemon) unsubscribeMail(cc *clientConn) {
	cc.subMu.Lock()
	defer cc.subMu.Unlock()
	cc.closed = true
	if cc.sub != nil {
		_ = d.mail.Close(cc.sub) // its rows are released again at the next start
		cc.sub = nil
	}
}

type queueResult struct {
	Items []store.QueueItem `json:"items"`
	Next  string            `json:"next,omitempty"`
}

func opMsgQueue(d *daemon, _ *clientConn, r request) (any, error) {
	peer := ""
	if r.Peer != "" {
		id, err := d.resolve(r.Peer)
		if err != nil {
			return nil, err
		}
		peer = id
	}
	cursor, err := strconv.ParseInt("0"+r.Cursor, 10, 64)
	limit := r.Limit
	switch {
	case err != nil:
		return nil, fail("params", "bad cursor")
	case limit == 0:
		limit = 100
	case limit < 0 || limit > 100:
		return nil, fail("params", "limit is 1-100")
	}
	which := r.Which
	if which == "" {
		which = store.Outbound
	}
	items, next, err := d.store.Queue(which, peer, cursor, limit)
	if err != nil {
		return nil, fail("params", err.Error())
	}
	res := queueResult{Items: items}
	if next != 0 {
		res.Next = strconv.FormatInt(next, 10)
	}
	return res, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
