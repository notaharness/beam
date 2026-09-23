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

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/store"
	"github.com/notaharness/beam/internal/transport"
)

// ErrLocked is a second daemon's error for a $BEAM_DIR already in use.
var ErrLocked = errors.New("another daemon holds $BEAM_DIR")

// Options configure a daemon.
type Options struct {
	Paths   Paths
	Version string
	Logf    func(format string, args ...any)
}

// daemon is one running beam daemon. Unenrolled, it has no store or node and
// serves only the socket.
type daemon struct {
	o    Options
	ctx  context.Context
	stop context.CancelFunc

	// Set once by enroll.
	key   *transport.Key
	fleet *identity.Fleet
	cred  identity.Credential
	store *store.Store
	node  *transport.Node

	mu           sync.Mutex
	peers        map[string]*peerState
	shells       map[string]map[*shell]bool // inbound pty and exec streams, by peer
	inbound      map[string]int             // open inbound sync streams, by peer
	reservations map[string]*reservation
	active       map[string]context.CancelFunc // attached streams: each one's detach
	subscribers  map[*clientConn]bool
}

// Run runs a daemon until ctx ends or a client sends daemon.shutdown. It takes
// the lock before anything else and serves the socket before the transport
// starts.
func Run(ctx context.Context, o Options) error {
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
	d := &daemon{o: o, ctx: ctx, stop: stop,
		peers: map[string]*peerState{}, shells: map[string]map[*shell]bool{}, inbound: map[string]int{},
		reservations: map[string]*reservation{}, active: map[string]context.CancelFunc{}, subscribers: map[*clientConn]bool{}}
	go d.serveSocket(ln)
	if err := d.enroll(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
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

// enroll loads fleet.json, key.json and state.db and starts the transport. With
// no fleet.json it returns fs.ErrNotExist and the daemon stays unenrolled.
func (d *daemon) enroll() error {
	f, err := d.o.Paths.loadFleet()
	if err != nil {
		return err
	}
	k, err := d.o.Paths.loadKey()
	if err != nil {
		return err
	}
	pk, err := base64.RawURLEncoding.DecodeString(f.CredentialPublicKey)
	if err != nil {
		return fmt.Errorf("fleet.json: %w", err)
	}
	st, err := store.Open(d.o.Paths.file(stateFile))
	if err != nil {
		return err
	}
	entry, _ := json.Marshal(f.Entry)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.key, d.fleet, d.store = k, f, st
	d.cred = identity.Credential{ID: f.CredentialID, PublicKey: pk}
	d.node, err = transport.Start(transport.Config{Key: k, Entry: entry, Admit: d.admit, Handle: d.handle})
	if err != nil {
		return err
	}
	peers, err := st.Peers()
	for _, p := range peers {
		if !p.Revoked {
			d.startDialerLocked(p.Entry.PeerID)
		}
	}
	return err
}

func (d *daemon) enrolled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.node != nil
}

func (d *daemon) close() {
	d.mu.Lock()
	node, st := d.node, d.store
	d.mu.Unlock()
	if node != nil {
		node.Close()
	}
	if st != nil {
		st.Close()
	}
}

func now() int64 { return time.Now().UnixMilli() }
