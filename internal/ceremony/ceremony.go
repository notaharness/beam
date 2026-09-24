// Package ceremony runs one passkey ceremony (docs/02, Ceremonies): the URL
// of the page at beam.n10.is that makes the WebAuthn call, carrying a key
// made for this ceremony and the slot on the worker the page answers
// through; the wait on that slot; and the result, opened with the key.
package ceremony

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net/url"
	"sync"
	"time"
)

// Page is the ceremony page.
const Page = "https://beam.n10.is/"

// Timeout bounds a ceremony; only tests change it.
var Timeout = 5 * time.Minute

// What a tap approves: the fragment's o (docs/02, Ceremonies).
const (
	Create = "c" // the fleet's passkey
	Add    = "a" // a get over a machine's member statement
	Remove = "r" // a get over a machine's revocation
)

// Why a ceremony ended without a result; their text is the socket's error.
var (
	ErrTimeout        = errors.New("ceremony-timeout")
	ErrState          = errors.New("ceremony-state")
	ErrCancelled      = errors.New("ceremony-cancelled")
	ErrPRFUnsupported = errors.New("prf-unsupported")
	ErrFailed         = errors.New("the browser's passkey call failed")
)

// Request is what the page shows and asks the authenticator for.
type Request struct {
	Kind      string // Create, Add or Remove
	Label     string // the machine the tap is for
	PeerID    string // its peerId, whose fingerprint the page shows
	Challenge []byte
	FleetName string // Create only
}

// Ceremony is one running ceremony, waiting on its slot.
type Ceremony struct {
	URL  string
	Kind string // the Request's

	worker   string // the directory worker holding the slot
	key      *ecdh.PrivateKey
	readKey  []byte
	slot     string
	stop     context.CancelFunc // ends the wait on the slot
	done     chan outcome
	once     sync.Once
	timer    *time.Timer   // the ceremony's one clock, from Start
	mu       sync.Mutex    // orders a wait's take against the clock
	taken    bool          // a wait has the outcome
	timedOut chan struct{} // closed if the clock ran out before a wait took the outcome
}

type outcome struct {
	r   Result
	err error
}

// answer, set only by beamtest builds, answers a ceremony in place of the
// browser when a test authenticator is configured.
var answer func(*Ceremony)

// Start makes the ceremony's key and slot, starts waiting on the slot at
// worker, and returns the ceremony, whose URL the client opens, prints or
// draws.
func Start(req Request, worker string) (*Ceremony, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	readKey := make([]byte, 32)
	rand.Read(readKey)
	h := sha256.Sum256(readKey)
	ctx, stop := context.WithCancel(context.Background())
	c := &Ceremony{Kind: req.Kind, worker: worker, key: key, readKey: readKey, slot: b64(h[:16]), stop: stop,
		done: make(chan outcome, 1), timedOut: make(chan struct{})}
	frag := url.Values{"o": {req.Kind}, "s": {c.slot}, "k": {b64(key.PublicKey().Bytes())}, "c": {b64(req.Challenge)},
		"l": {req.Label}, "f": {req.PeerID[:min(16, len(req.PeerID))]}} // the table in docs/02
	if req.Kind == Create {
		frag.Set("n", req.FleetName)
	}
	c.URL = Page + "#" + frag.Encode()
	c.timer = time.AfterFunc(Timeout, c.expire)
	go c.await(ctx)
	if answer != nil {
		go answer(c)
	}
	return c, nil
}

// await ends the ceremony with what its slot brings, unless it ends first.
func (c *Ceremony) await(ctx context.Context) {
	sealed, err := readSlot(ctx, c.worker, c.slot, c.readKey)
	switch {
	case err == nil:
		c.finish(c.open(sealed))
	case errors.Is(err, errRead):
		c.finish(Result{}, ErrState)
	}
}

// Wait returns the page's result once it arrives, or why the ceremony ended
// without one: its timeout, Timeout after Start, which also takes a result
// that arrived and was not waited for by then; a slot answered with what
// does not open; the page's own failure; or ctx's end (cancelled), unless
// one of the others came with it. The wait on the slot ends either way.
func (c *Ceremony) Wait(ctx context.Context) (Result, error) {
	defer c.Close()
	select {
	case o := <-c.done:
		return c.take(o)
	case <-ctx.Done():
		select {
		case o := <-c.done:
			return c.take(o)
		default:
			return Result{}, ErrCancelled
		}
	}
}

// take gives a wait the outcome, unless the clock ran out first.
func (c *Ceremony) take(o outcome) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.timedOut:
		return Result{}, ErrTimeout
	default:
	}
	c.taken = true
	return o.r, o.err
}

// expire is the clock running out: the ceremony ends ceremony-timeout if
// nothing ended it before, and TimedOut closes unless a wait has taken the
// outcome, so a result nobody waits for expires too.
func (c *Ceremony) expire() {
	c.finish(Result{}, ErrTimeout)
	c.stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.taken {
		close(c.timedOut)
	}
}

// TimedOut is closed if the clock ran out before a wait took the outcome.
func (c *Ceremony) TimedOut() <-chan struct{} { return c.timedOut }

// Close ends the wait on the slot and the ceremony's clock.
func (c *Ceremony) Close() {
	c.timer.Stop()
	c.stop()
}

// finish ends the ceremony with r or err, unless it has ended already.
func (c *Ceremony) finish(r Result, err error) {
	c.once.Do(func() { c.done <- outcome{r, err} })
}
