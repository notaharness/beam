package mailbox

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/notaharness/beam/internal/store"
)

// ErrNotInFlight is an ack or defer for an envelope the subscriber does not
// hold.
var ErrNotInFlight = errors.New("no such envelope in flight to this subscriber")

// Subscribers hands inbound mail to subscribed connections, one envelope in
// flight to each at a time (docs/05). Mail no subscriber matches stays in
// inbound. Deliveries happen outside the lock, so a subscriber that does not
// take its envelope holds up no one else.
type Subscribers struct {
	st *store.Store

	mu   sync.Mutex
	last int64 // the last subscription id; ids stay unique while the daemon runs
	subs map[*Sub]bool
}

// Sub is one subscription.
type Sub struct {
	id       int64
	topic    *string
	from     []string
	deliver  func(json.RawMessage) error
	inflight bool
}

// NewSubscribers fans st's inbound mail out.
func NewSubscribers(st *store.Store) *Subscribers {
	return &Subscribers{st: st, subs: map[*Sub]bool{}}
}

// Subscribe starts a subscription for mail on topic (any when nil) from the
// peers in from (any when empty), handed to deliver; a delivery that fails
// ends the subscription. A new subscription is offered again what an earlier
// one deferred.
func (h *Subscribers) Subscribe(topic *string, from []string, deliver func(json.RawMessage) error) *Sub {
	h.mu.Lock()
	h.last++
	s := &Sub{id: h.last, topic: topic, from: from, deliver: deliver}
	h.subs[s] = true
	h.mu.Unlock()
	h.Offer()
	return s
}

// Ack deletes the envelope id s holds: the application has it.
func (h *Subscribers) Ack(s *Sub, id string) error {
	return h.settle(s, func() (bool, error) { return h.st.AckInbound(s.id, id) })
}

// Defer releases the envelope id s holds without acking it, recording why.
// It is offered again only to subscriptions made after this one.
func (h *Subscribers) Defer(s *Sub, id, reason string) error {
	return h.settle(s, func() (bool, error) { return h.st.DeferInbound(s.id, id, reason, h.last) })
}

func (h *Subscribers) settle(s *Sub, f func() (bool, error)) error {
	h.mu.Lock()
	ok, err := f()
	if ok {
		s.inflight = false
	}
	h.mu.Unlock()
	switch {
	case err != nil:
		return err
	case !ok:
		return ErrNotInFlight
	}
	h.Offer()
	return nil
}

// Close ends s, whose connection is gone, releasing what it held.
func (h *Subscribers) Close(s *Sub) error {
	h.mu.Lock()
	delete(h.subs, s)
	err := h.st.Release(s.id)
	h.mu.Unlock()
	h.Offer()
	return err
}

// Offer hands each idle subscription the oldest mail it matches. Call it
// whenever mail arrives or a subscriber frees up.
func (h *Subscribers) Offer() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.inflight {
			continue
		}
		env, ok, err := h.st.Take(s.id, s.topic, s.from)
		if err == nil && ok {
			s.inflight = true
			go h.hand(s, env) // one at a time per subscription
		}
	}
}

// hand delivers env to s. A delivery that fails ends s, releasing env.
func (h *Subscribers) hand(s *Sub, env json.RawMessage) {
	if s.deliver(env) != nil {
		_ = h.Close(s) // what it held is released at the next start if this fails
	}
}
