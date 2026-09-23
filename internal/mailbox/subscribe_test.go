package mailbox

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/store"
)

// inbox is a store holding one envelope from A per topic, and B's
// subscribers over it.
func inbox(t *testing.T, topics ...string) (*store.Store, *Subscribers) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i, topic := range topics {
		e, _ := New(peerA, peerB, topic, "p", UTF8, 1)
		e.Seq = int64(i + 1)
		b, _ := e.Marshal()
		if _, err := st.Receive(peerA, e.Seq, e.ID, topic, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	return st, NewSubscribers(st)
}

// into is a delivery that hands each envelope's topic to a channel.
func into(ch chan string) func(json.RawMessage) error {
	return func(env json.RawMessage) error {
		var e Envelope
		json.Unmarshal(env, &e)
		ch <- e.Topic
		return nil
	}
}

func expect(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("nothing delivered, want %q", want)
	}
}

func quiet(t *testing.T, chs ...chan string) {
	t.Helper()
	for _, ch := range chs {
		select {
		case got := <-ch:
			t.Fatalf("delivered %q", got)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func topic(s string) *string { return &s }

// docs/05 Subscribers: a subscriber that never takes its envelope stalls
// only itself; subscribing, offering and other deliveries go on.
func TestStalledSubscriberStallsOnlyItself(t *testing.T) {
	_, h := inbox(t, "a", "b")
	stuck := make(chan struct{})
	defer close(stuck)
	reached := make(chan string, 1)
	go h.Subscribe(topic("a"), nil, func(json.RawMessage) error { reached <- "a"; <-stuck; return nil })
	expect(t, reached, "a")
	got := make(chan string, 1)
	subscribed := make(chan struct{})
	go func() { h.Subscribe(topic("b"), nil, into(got)); close(subscribed) }()
	expect(t, got, "b")
	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribing waits on the stalled subscriber")
	}
}

// docs/05 Subscribers: a delivery that fails ends its subscription and
// releases the envelope to another.
func TestFailedDeliveryReleases(t *testing.T) {
	_, h := inbox(t, "a")
	h.Subscribe(nil, nil, func(json.RawMessage) error { return errors.New("gone") })
	got := make(chan string, 1)
	h.Subscribe(nil, nil, into(got))
	expect(t, got, "a")
}

// docs/05 Subscribers: a deferred envelope is offered again only to a
// subscription made after the defer, so two subscribers cannot pass it back
// and forth.
func TestDeferredToLaterSubscriptions(t *testing.T) {
	_, h := inbox(t, "a")
	subs := map[*Sub]chan string{}
	for range 2 {
		ch := make(chan string, 1)
		subs[h.Subscribe(nil, nil, into(ch))] = ch
	}
	var holder *Sub
	for holder == nil {
		for s, ch := range subs {
			select {
			case <-ch:
				holder = s
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	var e Envelope
	items, _, _ := h.st.Queue(store.Inbound, "", 0, 1)
	json.Unmarshal(items[0].Envelope, &e)
	if err := h.Defer(holder, e.ID, "busy"); err != nil {
		t.Fatal(err)
	}
	for _, ch := range subs {
		quiet(t, ch)
	}
	later := make(chan string, 1)
	h.Subscribe(nil, nil, into(later))
	expect(t, later, "a")
}

// docs/05 Subscribers: a connection that replaces its subscription while a
// delivery to the old one is under way never gets that envelope after the
// new one has acked it.
func TestReplacedSubscriptionDeliversNothingStale(t *testing.T) {
	_, h := inbox(t, "a")
	delivered := make(chan string, 4) // envelope ids, as the connection sees them
	sending, release := make(chan struct{}), make(chan struct{})
	var later atomic.Bool
	conn := func(env json.RawMessage) error {
		if !later.Swap(true) { // the old subscription's send, held
			close(sending)
			<-release
		}
		var e Envelope
		json.Unmarshal(env, &e)
		delivered <- e.ID
		return nil
	}
	old := h.Subscribe(nil, nil, conn)
	<-sending
	replaced := make(chan *Sub, 1)
	go func() { h.Close(old); replaced <- h.Subscribe(nil, nil, conn) }()
	var cur *Sub
	select {
	case cur = <-replaced:
	case <-time.After(300 * time.Millisecond):
		close(release) // the replacement waits for the send under way
		<-delivered
		cur = <-replaced
	}
	id := <-delivered
	if err := h.Ack(cur, id); err != nil {
		t.Fatalf("ack through the new subscription: %v", err)
	}
	select {
	case <-release:
	default:
		close(release)
	}
	select {
	case stale := <-delivered:
		t.Errorf("delivered %s after its ack", stale)
	case <-time.After(300 * time.Millisecond):
	}
}

// docs/05 Subscribers: a delivery goes out only while it is its
// subscription's current one: not once the subscription has ended, settled
// the envelope or been handed the next.
func TestStaleDeliveryDropped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		topics []string
		then   func(h *Subscribers, s *Sub, id string, got chan json.RawMessage)
	}{
		{"ended", []string{"a"}, func(h *Subscribers, s *Sub, _ string, _ chan json.RawMessage) { h.Close(s) }},
		{"settled", []string{"a"}, func(h *Subscribers, s *Sub, id string, _ chan json.RawMessage) { h.Ack(s, id) }},
		{"handed the next", []string{"a", "b"}, func(h *Subscribers, s *Sub, id string, got chan json.RawMessage) {
			h.Ack(s, id)
			<-got
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h := inbox(t, tc.topics...)
			got := make(chan json.RawMessage, 2)
			s := h.Subscribe(nil, nil, func(env json.RawMessage) error { got <- env; return nil })
			env := <-got
			var e Envelope
			json.Unmarshal(env, &e)
			tc.then(h, s, e.ID, got)
			h.hand(s, env, 1) // a delivery of the first, late
			select {
			case <-got:
				t.Error("delivered")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// docs/05 Subscribers: an ack waits out a delivery under way to its
// subscription, so nothing arrives after the ack's reply. Here the
// connection acks the copy an earlier subscription got while the new one's
// copy is being sent.
func TestAckWaitsOutDelivery(t *testing.T) {
	_, h := inbox(t, "a")
	got := make(chan json.RawMessage, 1)
	old := h.Subscribe(nil, nil, func(env json.RawMessage) error { got <- env; return nil })
	var e Envelope
	json.Unmarshal(<-got, &e)
	h.Close(old)
	var mu sync.Mutex
	var log []string
	note := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
	sending, release := make(chan struct{}), make(chan struct{})
	cur := h.Subscribe(nil, nil, func(json.RawMessage) error { close(sending); <-release; note("delivered"); return nil })
	<-sending
	acked := make(chan error, 1)
	go func() { err := h.Ack(cur, e.ID); note("acked"); acked <- err }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-acked; err != nil {
		t.Fatal(err)
	}
	if log[0] != "delivered" {
		t.Errorf("%v: delivered after the ack", log)
	}
}
