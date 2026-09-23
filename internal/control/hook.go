package control

import "sync/atomic"

// hook, when a test sets one, runs at named points where a daemon races
// something the test must order. It gets the daemon's peer id, the point and
// the peer the point concerns.
var hook atomic.Pointer[func(self, point, peer string)]

func (d *daemon) at(point, peer string) {
	if f := hook.Load(); f != nil {
		(*f)(d.fleet.Entry.PeerID, point, peer)
	}
}
