// Package ceremony runs one passkey ceremony (docs/02, Ceremonies): a
// one-shot loopback listener, the URL of the page at beam.n10.is that makes
// the WebAuthn call, the /cb landing page that hands the page's fragment to
// the daemon, and the result it carries.
package ceremony

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Page is the ceremony page.
const Page = "https://beam.n10.is/"

// Timeout bounds a ceremony; only tests change it.
var Timeout = 5 * time.Minute

// Ops.
const (
	Create = "create"
	Get    = "get"
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
	Op          string // Create or Get
	Action      string // what the tap approves, shown as the heading
	Label       string
	Fingerprint string
	Challenge   []byte
	FleetName   string // Create only
}

// Ceremony is one running ceremony, waiting for the page's callback.
type Ceremony struct {
	URL  string
	Port int

	req      Request
	state    string
	srv      *http.Server
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

// Start listens on a random loopback port and returns the ceremony, whose
// URL the client opens or prints.
func Start(req Request) (*Ceremony, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	state := make([]byte, 16)
	rand.Read(state)
	c := &Ceremony{req: req, state: b64(state), Port: ln.Addr().(*net.TCPAddr).Port, done: make(chan outcome, 1),
		timedOut: make(chan struct{})}
	c.timer = time.AfterFunc(Timeout, c.expire)
	frag := url.Values{"op": {req.Op}, "state": {c.state}, "port": {strconv.Itoa(c.Port)}, "action": {req.Action},
		"label": {req.Label}, "fingerprint": {req.Fingerprint}, "challenge": {b64(req.Challenge)}}
	if req.Op == Create {
		frag.Set("fleetName", req.FleetName)
	}
	c.URL = Page + "#" + frag.Encode()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cb", c.landing)
	mux.HandleFunc("POST /cb/result", c.result)
	c.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = c.srv.Serve(ln) }() // ends at Close
	if answer != nil {
		go answer(c)
	}
	return c, nil
}

// Wait returns the page's result once it arrives, or why the ceremony ended
// without one: its timeout, Timeout after Start, which also takes a result
// that arrived and was not waited for by then; a callback with the wrong
// state; the page's own failure; or ctx's end (cancelled), unless one of the
// others came with it. The listener is closed either way.
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.taken {
		close(c.timedOut)
	}
}

// TimedOut is closed if the clock ran out before a wait took the outcome.
func (c *Ceremony) TimedOut() <-chan struct{} { return c.timedOut }

// Close ends the ceremony's listener and its clock. A request under way, the
// result that ended the wait among them, is answered first, for at most a
// second.
func (c *Ceremony) Close() {
	c.timer.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
	_ = c.srv.Close() // what the second did not end
}

// finish ends the ceremony with r or err, unless it has ended already.
func (c *Ceremony) finish(r Result, err error) {
	c.once.Do(func() { c.done <- outcome{r, err} })
}

// landing is /cb: its script reads the fragment the page navigated with,
// clears it, and posts it back to this origin.
func (c *Ceremony) landing(w http.ResponseWriter, r *http.Request) {
	if !c.local(r) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", landingCSP)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, landingPage)
}

// result is /cb/result: the fragment, checked against the ceremony's state.
func (c *Ceremony) result(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !c.local(r) || r.ParseForm() != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("state")), []byte(c.state)) != 1 {
		http.Error(w, "this is not the ceremony beam is waiting for", http.StatusBadRequest)
		c.finish(Result{}, ErrState)
		return
	}
	res, err := parse(r.PostForm)
	fmt.Fprint(w, "done, close this tab")
	c.finish(res, err)
}

// local reports whether a request names this listener as its host, so a page
// that rebinds a name to loopback cannot reach it.
func (c *Ceremony) local(r *http.Request) bool {
	return r.Host == "127.0.0.1:"+strconv.Itoa(c.Port)
}

const landingScript = `
const out = document.getElementById("out");
const fragment = location.hash.slice(1);
history.replaceState(null, "", "/cb");
fetch("/cb/result", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: fragment })
  .then((r) => r.text())
  .then((t) => { out.textContent = t; }, () => { out.textContent = "beam did not answer; run the command again"; });
`

const landingStyle = `body { font: 16px/1.5 system-ui, sans-serif; max-width: 34rem; margin: 4rem auto; padding: 0 1rem; }`

var (
	landingPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>beam</title><style>` + landingStyle +
		`</style></head><body><p id="out">finishing…</p><script>` + landingScript + `</script></body></html>`
	landingCSP = "default-src 'none'; script-src '" + cspHash(landingScript) + "'; style-src '" + cspHash(landingStyle) +
		"'; connect-src 'self'"
)

func cspHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256-" + base64.StdEncoding.EncodeToString(h[:])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
