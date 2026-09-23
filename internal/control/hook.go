package control

import "sync/atomic"

// hook, when a test sets one, runs at named points where a daemon races
// something the test must order. It gets the daemon's peer id, the point and
// the peer the point concerns.
var hook atomic.Pointer[func(self, point, peer string)]

func (d *daemon) at(point, peer string) {
	if f := hook.Load(); f != nil {
		self := ""
		if e := d.enrolment(); e != nil {
			self = e.self()
		}
		(*f)(self, point, peer)
	}
}
