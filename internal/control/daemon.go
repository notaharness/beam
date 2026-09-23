package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/mailbox"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/stream"
	"github.com/notaharness/beam/internal/transport"
)

// ErrLocked is a second daemon's error for a $BEAM_DIR already in use.
var ErrLocked = errors.New("another daemon holds $BEAM_DIR")

// Options configure a daemon.
type Options struct {
	Paths     Paths
	Version   string
	Logf      func(format string, args ...any)
	DERPMap   string // the DERP map a new key is homed from; tailcat's when empty
	Directory string // the directory worker; beam.n10.is when empty
}

// daemon is one running beam daemon. Unenrolled, it has no store or node and
// serves only the socket.
type daemon struct {
	o       Options
	ctx     context.Context
	stop    context.CancelFunc
	started chan struct{} // closed once the start has enrolled, or found no fleet.json

	flow    *flow         // the ceremony under way, if any
	pending chan struct{} // says a directory write was queued

	mu           sync.Mutex
	en           *enrolment // nil while unenrolled
	peers        map[string]*peerState
	granted      map[string]map[*granted]bool // inbound pty, exec and msg streams, by peer
	inbound      map[string]int               // open inbound sync streams, by peer
	reservations map[string]*reservation
	active       map[string]context.CancelFunc // attached streams: each one's detach
	subscribers  map[*clientConn]bool
	conns        map[net.Conn]bool                    // every client connection, closed at shutdown
	sends        map[string]map[int64]chan sendResult // msg.send waiting for its ack, by peer and seq
}

// Run runs a daemon until ctx ends or a client sends daemon.shutdown. It takes
// the lock before anything else and serves the socket before the transport
// starts.
func Run(ctx context.Context, o Options) error {
	if o.DERPMap == "" {
		o.DERPMap = transport.DefaultDERPMap
	}
	if o.Directory == "" {
		o.Directory = directory.DefaultURL
	}
	for _, dir := range []string{filepath.Join(o.Paths.Dir, "run"), filepath.Dir(o.Paths.Socket)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	unlock, err := lock(o.Paths.file("run/beam.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	ln, err := listen(o.Paths.Socket)
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	d := &daemon{o: o, ctx: ctx, stop: stop, started: make(chan struct{}),
		peers: map[string]*peerState{}, granted: map[string]map[*granted]bool{}, inbound: map[string]int{},
		reservations: map[string]*reservation{}, active: map[string]context.CancelFunc{}, subscribers: map[*clientConn]bool{}, conns: map[net.Conn]bool{},
		sends: map[string]map[int64]chan sendResult{}, pending: make(chan struct{}, 1)}
	go d.serveSocket(ln)
	d.at("starting", "")
	if _, err := d.enroll(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	close(d.started)
	<-ctx.Done()
	d.close()
	return nil
}

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrLocked
	}
	return func() { f.Close() }, nil
}

// listen serves the socket with mode 0600, removing a file no one listens on.
func listen(path string) (net.Listener, error) {
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s: another daemon is listening", path)
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return ln, os.Chmod(path, 0o600)
}

// enroll loads fleet.json, key.json and state.db, starts the transport and
// dials every member, then reads the directory and retries queued writes
// while enrolled. With no fleet.json it returns fs.ErrNotExist and the daemon
// stays unenrolled.
func (d *daemon) enroll() (*enrolment, error) {
	f, err := d.o.Paths.loadFleet()
	if err != nil {
		return nil, err
	}
	k, err := d.o.Paths.loadKey()
	if err != nil {
		return nil, err
	}
	pk, err := base64.RawURLEncoding.DecodeString(f.CredentialPublicKey)
	if err != nil {
		return nil, fmt.Errorf("fleet.json: %w", err)
	}
	st, err := store.Open(d.o.Paths.file(stateFile))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(d.ctx)
	e := &enrolment{ctx: ctx, cancel: cancel, key: k, fleet: f, store: st, mail: mailbox.NewSubscribers(st),
		cred: identity.Credential{ID: f.CredentialID, PublicKey: pk}}
	entry, _ := json.Marshal(f.Entry)
	e.node, err = transport.Start(transport.Config{Key: k, Entry: entry,
		Admit:  func(raw json.RawMessage) (string, [32]byte, func(), string) { return d.admit(e, raw) },
		Handle: func(id string, h stream.Header, c *stream.Conn) { d.handle(e, id, h, c) }})
	if err != nil {
		cancel()
		st.Close()
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.en = e
	go d.readDirectory(e)
	go d.retryPending(e)
	peers, err := st.Peers()
	for _, p := range peers {
		if !p.Revoked {
			d.startDialerLocked(e, p.Entry.PeerID)
		}
	}
	return e, err
}

// enrolment is the enrolment under way, or nil.
func (d *daemon) enrolment() *enrolment {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.en
}

func (d *daemon) isEnrolled() bool { return d.enrolment() != nil }

// unenroll ends the enrolment: dialers, tunnels, the directory loops, and
// state.db. forget also empties it of the fleet and removes fleet.json (a
// fleet reset); key.json and the daemon's socket stay.
func (d *daemon) unenroll(forget bool) error {
	d.mu.Lock()
	e := d.en
	for id, ps := range d.peers {
		ps.cancel()
		delete(d.peers, id)
	}
	d.en = nil
	d.mu.Unlock()
	if e == nil {
		return nil
	}
	e.end()
	var err error
	if forget {
		err = errors.Join(e.store.Reset(), os.Remove(d.o.Paths.file(fleetFile)))
	}
	return errors.Join(err, e.store.Close())
}

func (d *daemon) close() {
	d.mu.Lock()
	e := d.en
	for c := range d.conns {
		c.Close()
	}
	d.mu.Unlock()
	if e != nil {
		e.end()
		e.store.Close()
	}
}

func now() int64 { return time.Now().UnixMilli() }
