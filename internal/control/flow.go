package control

import (
	"context"
	"errors"

	"github.com/notaharness/beam/internal/ceremony"
	"github.com/notaharness/beam/internal/identity"
)

// flow is the ceremony under way (docs/06: one at a time): *.start began it
// and the matching *.wait finishes it with then.
type flow struct {
	op     string
	ctx    context.Context
	cancel context.CancelFunc
	cer    *ceremony.Ceremony
	then   func(context.Context, *clientConn, ceremony.Result) (any, error)
}

// begin starts op's first ceremony. A flow nobody waits on ends with its
// ceremony's timeout.
func (d *daemon) begin(op string, req ceremony.Request, then func(context.Context, *clientConn, ceremony.Result) (any, error)) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.flow != nil {
		return nil, fail("busy", "a ceremony is under way: "+d.flow.op)
	}
	cer, err := ceremony.Start(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, ceremony.Timeout)
	f := &flow{op: op, ctx: ctx, cancel: cancel, cer: cer, then: then}
	context.AfterFunc(ctx, func() {
		cer.Close()
		d.mu.Lock()
		if d.flow == f {
			d.flow = nil
		}
		d.mu.Unlock()
	})
	d.flow = f
	return map[string]string{"ceremonyUrl": cer.URL}, nil
}

// finish is *.wait for op: the flow's ceremony, then the rest of it.
func (d *daemon) finish(op string, cc *clientConn) (any, error) {
	d.mu.Lock()
	f := d.flow
	d.mu.Unlock()
	if f == nil || f.op != op {
		return nil, fail("ceremony-state", "no "+op+" ceremony is under way")
	}
	defer f.cancel()
	r, err := f.cer.Wait(f.ctx)
	if err != nil {
		return nil, ceremonyErr(err)
	}
	return f.then(f.ctx, cc, r)
}

// another runs a second ceremony within a flow, announcing it to the waiting
// client as a ceremony event.
func another(ctx context.Context, cc *clientConn, req ceremony.Request) (ceremony.Result, error) {
	cer, err := ceremony.Start(req)
	if err != nil {
		return ceremony.Result{}, err
	}
	_ = cc.send(event{"ceremony", map[string]string{"ceremonyUrl": cer.URL}}) // a gone client cancels nothing; the ceremony times out
	r, err := cer.Wait(ctx)
	return r, ceremonyErr(err)
}

// stage tells the client waiting on a flow what it does now (docs/07).
func stage(cc *clientConn, what string) {
	_ = cc.send(event{"stage", map[string]string{"stage": what}}) // a gone client misses a progress line
}

// refusal is a record's refusal as a socket error: its reason, except that
// revoked is revoked-peer there (docs/06).
func refusal(err error) error {
	if errors.Is(err, identity.Revoked) {
		return fail("revoked-peer", "")
	}
	return fail(err.Error(), "")
}

// ceremonyErr is a ceremony's end as a socket error.
func ceremonyErr(err error) error {
	for _, code := range []error{ceremony.ErrTimeout, ceremony.ErrState, ceremony.ErrCancelled, ceremony.ErrPRFUnsupported} {
		if errors.Is(err, code) {
			return fail(code.Error(), "")
		}
	}
	if err != nil {
		return fail("bad-assertion", err.Error())
	}
	return nil
}

// opCeremonyCancel ends the ceremony under way; the next may start at once.
func opCeremonyCancel(d *daemon, _ *clientConn, _ request) (any, error) {
	d.endFlow()
	return struct{}{}, nil
}

func (d *daemon) endFlow() {
	d.mu.Lock()
	f := d.flow
	d.flow = nil
	d.mu.Unlock()
	if f != nil {
		f.cancel()
	}
}
