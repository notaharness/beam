package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/stream"
)

// maxControlLine bounds one control request or reply (docs/06).
const maxControlLine = 1 << 20

// sendTimeout is how long a line may wait for its client to read it.
const sendTimeout = 10 * time.Second

// request is a control request; each op reads the fields it needs.
type request struct {
	ID       json.RawMessage   `json:"id"`
	Op       string            `json:"op"`
	Attach   string            `json:"attach"`
	Peer     string            `json:"peer"`
	Alias    *string           `json:"alias"`
	Grant    string            `json:"grant"`
	Argv     []string          `json:"argv"`
	Cwd      string            `json:"cwd"`
	Env      map[string]string `json:"env"`
	Cols     int               `json:"cols"`
	Rows     int               `json:"rows"`
	StreamID string            `json:"streamId"`
	Cursor   string            `json:"cursor"`
	Limit    int               `json:"limit"`

	To         string   `json:"to"`
	Topic      *string  `json:"topic"`
	Payload    string   `json:"payload"`
	Encoding   string   `json:"encoding"`
	From       []string `json:"from"`
	EnvelopeID string   `json:"envelopeId"`
	Reason     string   `json:"reason"`
	Which      string   `json:"which"`

	Label     string `json:"label"`
	FleetName string `json:"fleetName"`
	Confirm   string `json:"confirm"`
}

type response struct {
	ID     json.RawMessage `json:"id"`
	OK     bool            `json:"ok"`
	Result any             `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Detail string          `json:"detail,omitempty"`
}

type event struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// OpError is a failed op: a token from docs/06's error list, and detail.
type OpError struct {
	Code, Detail string
}

func (e *OpError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

func fail(code, detail string) error { return &OpError{code, detail} }

// clientConn is one control connection; replies and events share it.
type clientConn struct {
	mu sync.Mutex
	c  net.Conn

	subMu  sync.Mutex
	sub    *mailbox.Sub         // its msg.subscribe
	mail   *mailbox.Subscribers // the enrolment's that sub is on
	closed bool                 // the connection is gone: no subscription follows
}

// send writes one line. A client that leaves it unread for sendTimeout is
// disconnected, which ends its read loop and releases what it held.
func (cc *clientConn) send(v any) error {
	b, err := stream.Marshal(v)
	if err != nil {
		return err
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.c.SetWriteDeadline(time.Now().Add(sendTimeout))
	if _, err := cc.c.Write(append(b, '\n')); err != nil {
		cc.c.Close()
		return err
	}
	return nil
}

func (d *daemon) serveSocket(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go d.serveConn(c)
	}
}

// serveConn reads a connection's first line to tell an attach from a control
// connection, then serves requests concurrently.
func (d *daemon) serveConn(c net.Conn) {
	d.mu.Lock()
	d.conns[c] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.conns, c)
		d.mu.Unlock()
		c.Close()
	}()
	br := bufio.NewReader(c)
	cc := &clientConn{c: c}
	defer d.unsubscribe(cc)
	defer d.unsubscribeMail(cc)
	for first := true; ; first = false {
		line, err := stream.ReadLine(br, maxControlLine)
		if err != nil {
			return
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = cc.send(response{Error: "params", Detail: "not a JSON request"}) // a failed send closes the connection
			continue
		}
		if first && req.Attach != "" {
			d.attach(stream.NewConnReader(c, br), req.Attach)
			return
		}
		go d.dispatch(cc, req)
	}
}

func (d *daemon) dispatch(cc *clientConn, req request) {
	res, err := d.op(cc, req)
	resp := response{ID: req.ID, OK: err == nil, Result: res}
	var oe *OpError
	switch {
	case errors.As(err, &oe):
		resp.Error, resp.Detail = oe.Code, oe.Detail
	case err != nil:
		resp.Error, resp.Detail = "internal", err.Error()
	}
	_ = cc.send(resp) // a failed send closes the connection
	if req.Op == "daemon.shutdown" && err == nil {
		d.stop()
	}
}

func (d *daemon) subscribe(cc *clientConn) {
	d.mu.Lock()
	d.subscribers[cc] = true
	d.mu.Unlock()
}

func (d *daemon) unsubscribe(cc *clientConn) {
	d.mu.Lock()
	delete(d.subscribers, cc)
	d.mu.Unlock()
}

func (d *daemon) emit(name string, data any) {
	d.mu.Lock()
	subs := make([]*clientConn, 0, len(d.subscribers))
	for cc := range d.subscribers {
		subs = append(subs, cc)
	}
	d.mu.Unlock()
	for _, cc := range subs {
		_ = cc.send(event{name, data}) // a failed send closes the connection
	}
}

func (d *daemon) emitPeer(e *enrolment, peerID string) {
	d.mu.Lock()
	n := len(d.subscribers)
	d.mu.Unlock()
	if n > 0 {
		d.emit("peer", d.peerView(e, peerID))
	}
}
