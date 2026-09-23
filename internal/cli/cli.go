// Package cli is the beam command line (docs/07). Every subcommand but daemon
// and version is a client of the control socket.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/notaharness/beam/internal/control"
)

// Version is the build's semver, set by the main package.
var Version = "dev"

// env is one invocation's world.
type env struct {
	args   []string
	vars   []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (e *env) getenv(k string) string {
	for i := len(e.vars) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(e.vars[i], k+"="); ok {
			return v
		}
	}
	return ""
}

func (e *env) paths() (control.Paths, error) { return control.ResolvePaths(e.getenv) }

// connect is connect-or-spawn for this invocation.
func (e *env) connect() (*control.Client, error) {
	p, err := e.paths()
	if err != nil {
		return nil, err
	}
	return control.Connect(p, e.vars)
}

// call connects, runs one op and closes.
func (e *env) call(op string, params map[string]any, out any) error {
	c, err := e.connect()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(op, params, out)
}

var commands = map[string]func(*env) int{
	"daemon":  runDaemon,
	"status":  runStatus,
	"peers":   runPeers,
	"peer":    runPeer,
	"exec":    runExec,
	"connect": runConnect,
	"msg":     runMsg,
	"version": func(e *env) int { fmt.Fprintln(e.stdout, Version); return 0 },
}

// errUsage marks a usage error: exit 2.
var errUsage = errors.New("usage")

const usage = `usage:
  beam daemon [--detach]
  beam status [--json]
  beam peers  [--json]
  beam peer alias <peer> <alias|->
  beam peer grant <peer> all|msg|none
  beam connect <peer> [--cwd PATH] [-- argv...]
  beam exec    <peer> [--cwd PATH] [--env K=V]... -- argv...
  beam msg send   <peer> [--topic T] [--base64] [--json] <payload|->
  beam msg listen [--topic T] [<peer>...]
  beam msg queue  [<peer>] [--which outbound|inbound|refused|quarantine]
  beam version
`

// Main runs one invocation and returns its exit code: 0, 1 for a beam error
// (its token on stderr), 2 for usage, or a remote status.
func Main(args, vars []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &env{vars: vars, stdin: stdin, stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	run, ok := commands[args[0]]
	if !ok {
		fmt.Fprint(stderr, usage)
		return 2
	}
	e.args = args[1:]
	return run(e)
}

// fail reports err and returns its exit code.
func (e *env) fail(err error) int {
	if errors.Is(err, errUsage) {
		fmt.Fprint(e.stderr, usage)
		return 2
	}
	fmt.Fprintln(e.stderr, err)
	return 1
}

// flags is a subcommand's flag set; parse errors are usage errors.
func (e *env) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
