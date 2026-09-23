package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/notaharness/beam/internal/control"
)

// runDaemon runs the daemon in the foreground until SIGTERM or
// daemon.shutdown, or with --detach starts it in its own session, logging to
// $BEAM_DIR/daemon.log, and returns. With --exit-with-parent it also stops
// when its stdin ends: the parent holding the other end has exited (docs/07).
// --derp-map names the DERP map a new key is homed from.
func runDaemon(e *env) int {
	fs := e.flags("daemon")
	detach := fs.Bool("detach", false, "")
	withParent := fs.Bool("exit-with-parent", false, "")
	derpMap := fs.String("derp-map", "", "")
	dir := directoryFlag(fs)
	if fs.Parse(e.args) != nil || fs.NArg() != 0 {
		return e.fail(errUsage)
	}
	if *withParent && (*detach || !lifeline(e.stdin)) {
		fmt.Fprintln(e.stderr, "--exit-with-parent needs stdin to be a pipe or socket from the parent, and no --detach")
		return e.fail(errUsage)
	}
	p, err := e.paths()
	if err != nil {
		return e.fail(err)
	}
	if *detach {
		if err := e.detach(p, detached(fs)); err != nil {
			return e.fail(err)
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if *withParent {
		ctx = untilParentExits(ctx, e.stdin)
	}
	logf := log.New(e.stderr, "", log.LstdFlags).Printf
	if err := control.Run(ctx, control.Options{Paths: p, Version: Version, Logf: logf, DERPMap: *derpMap, Directory: *dir}); err != nil {
		return e.fail(err)
	}
	if errors.Is(context.Cause(ctx), errParentExited) {
		logf("stopped: %v", errParentExited) // after the shutdown, so a write that blocks cannot hold it
	}
	return 0
}

var errParentExited = errors.New("stdin ended: the parent has exited")

// untilParentExits is ctx, ended also when stdin ends: the parent holding
// its other end has exited. The parent's pipes to stdout and stderr may be
// gone with it, so a write to one fails rather than raising SIGPIPE, which
// would end the daemon before its shutdown. SIGTERM and SIGINT stay handled.
func untilParentExits(ctx context.Context, stdin io.Reader) context.Context {
	signal.Ignore(syscall.SIGPIPE)
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		_, _ = io.Copy(io.Discard, stdin) // until the parent's end closes, or fails
		cancel(errParentExited)
	}()
	return ctx
}

// lifeline reports whether stdin is a pipe or socket, whose end the parent
// can hold.
func lifeline(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&(os.ModeNamedPipe|os.ModeSocket) != 0
}

// detached is the arguments of the daemon --detach starts: the options that
// were set, but --detach, whatever its spelling.
func detached(fs *flag.FlagSet) []string {
	args := []string{"daemon"}
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "detach" {
			args = append(args, "--"+f.Name+"="+f.Value.String())
		}
	})
	return args
}

// detach starts beam with args in a new session.
func (e *env) detach(p control.Paths, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(p.Dir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = e.vars, logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}
	return cmd.Process.Release()
}
