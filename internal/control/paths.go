// Package control is the daemon and its local socket (docs/06): the lock, the
// NDJSON control connection and attach connections, the peers it dials and
// syncs with, the streams it serves, and the connect-or-spawn client.
package control

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/notaharness/beam/internal/identity"
	"github.com/notaharness/beam/internal/transport"
)

// Paths locate a daemon: its $BEAM_DIR and its socket.
type Paths struct {
	Dir    string
	Socket string
}

// ResolvePaths applies docs/02 and docs/06: $BEAM_CONFIG_DIR, else
// $XDG_CONFIG_HOME/beam, else ~/.config/beam; the socket is $BEAM_SOCKET, else
// run/beam.sock in that directory.
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
	keyFile   = "key.json"
	fleetFile = "fleet.json"
	stateFile = "state.db"
)

func (p Paths) loadKey() (*transport.Key, error) {
	k := new(transport.Key)
	return k, readJSON(p.file(keyFile), k)
}

func (p Paths) loadFleet() (*identity.Fleet, error) {
	f := new(identity.Fleet)
	return f, readJSON(p.file(fleetFile), f)
}
