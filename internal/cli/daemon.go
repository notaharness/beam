package cli

import (
	"context"
	"fmt"
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
// $BEAM_DIR/daemon.log, and returns. --derp-map names the DERP map a new key
// is homed from.
func runDaemon(e *env) int {
	fs := e.flags("daemon")
	detach := fs.Bool("detach", false, "")
	derpMap := fs.String("derp-map", "", "")
	if fs.Parse(e.args) != nil || fs.NArg() != 0 {
		return e.fail(errUsage)
	}
	p, err := e.paths()
	if err != nil {
		return e.fail(err)
	}
	if *detach {
		if err := e.detach(p, e.args); err != nil {
			return e.fail(err)
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	logf := log.New(e.stderr, "", log.LstdFlags).Printf
	if err := control.Run(ctx, control.Options{Paths: p, Version: Version, Logf: logf, DERPMap: *derpMap}); err != nil {
		return e.fail(err)
	}
	return 0
}

// detach starts `beam daemon` with args but --detach in a new session.
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
	var rest []string
	for _, a := range args {
		if a != "--detach" && a != "-detach" {
			rest = append(rest, a)
		}
	}
	cmd := exec.Command(exe, append([]string{"daemon"}, rest...)...)
	cmd.Env, cmd.Stdout, cmd.Stderr = e.vars, logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}
	return cmd.Process.Release()
}
