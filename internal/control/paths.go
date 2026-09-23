// Package control is the daemon and its local socket (docs/06): the lock, the
// NDJSON control connection and attach connections, the peers it dials and
// syncs with, the streams it serves, and the connect-or-spawn client.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/transport"
)

// Paths locate a daemon: its $BEAM_DIR and its socket.
type Paths struct {
	Dir    string
	Socket string
}

// maxSocket is the longest socket path a Unix socket address holds here: 107
// bytes on Linux, 103 on macOS.
var maxSocket = len(syscall.RawSockaddrUnix{}.Path) - 1

// ResolvePaths applies docs/02 and docs/06: $BEAM_CONFIG_DIR, else
// $XDG_CONFIG_HOME/beam, else ~/.config/beam; the socket is $BEAM_SOCKET, else
// run/beam.sock in that directory, and no longer than a socket address holds.
func ResolvePaths(getenv func(string) string) (Paths, error) {
	dir := getenv("BEAM_CONFIG_DIR")
	switch {
	case dir != "":
	case getenv("XDG_CONFIG_HOME") != "":
		dir = filepath.Join(getenv("XDG_CONFIG_HOME"), "beam")
	case getenv("HOME") != "":
		dir = filepath.Join(getenv("HOME"), ".config", "beam")
	default:
		return Paths{}, errors.New("no BEAM_CONFIG_DIR, XDG_CONFIG_HOME or HOME")
	}
	sock := getenv("BEAM_SOCKET")
	if sock == "" {
		sock = filepath.Join(dir, "run", "beam.sock")
	}
	if len(sock) > maxSocket {
		return Paths{}, fmt.Errorf("socket path too long (%d bytes, at most %d): %s; set BEAM_SOCKET to a shorter one", len(sock), maxSocket, sock)
	}
	return Paths{Dir: dir, Socket: sock}, nil
}

func (p Paths) file(name string) string { return filepath.Join(p.Dir, name) }

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Enrolment files.
const (
	keyFile      = "key.json"
	fleetFile    = "fleet.json"
	stateFile    = "state.db"
	derpFileName = "derpmap.json"
)

// save writes a config file as docs/02 says: a temporary file, mode 0600,
// renamed into place.
func (p Paths) save(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(p.Dir, name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // gone once renamed
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p.file(name))
}

// derpCache is derpmap.json: the last DERP map fetched, with where from and
// its ETag, which tailcat revalidates after an hour.
type derpCache struct{ p Paths }

type derpFile struct {
	URL  string          `json:"url"`
	ETag string          `json:"etag"`
	Map  json.RawMessage `json:"map"`
}

func (c derpCache) Get(url string) ([]byte, string, time.Time, bool) {
	var f derpFile
	st, err := os.Stat(c.p.file(derpFileName))
	if err != nil || readJSON(c.p.file(derpFileName), &f) != nil || f.URL != url {
		return nil, "", time.Time{}, false
	}
	return f.Map, f.ETag, st.ModTime(), true
}

func (c derpCache) Put(url string, data []byte, etag string) error {
	return c.p.save(derpFileName, derpFile{url, etag, data})
}

func (p Paths) loadKey() (*transport.Key, error) {
	k := new(transport.Key)
	return k, readJSON(p.file(keyFile), k)
}

func (p Paths) loadFleet() (*identity.Fleet, error) {
	f := new(identity.Fleet)
	return f, readJSON(p.file(fleetFile), f)
}
