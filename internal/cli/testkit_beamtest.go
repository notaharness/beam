//go:build beamtest

package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"

	"github.com/notaharness/beam/internal/devderp"
	"github.com/notaharness/beam/internal/fakeworker"
	"tailscale.com/types/logger"
)

func init() { commands["testkit"] = runTestkit }

// runTestkit serves the dev DERP relay and the fake worker on loopback and
// prints where, as {derpMap, directory}, until SIGTERM or SIGINT, or with
// --exit-with-parent until its stdin ends (docs/10, the test kit).
func runTestkit(e *env) int {
	fs := e.flags("testkit")
	withParent := fs.Bool("exit-with-parent", false, "")
	if fs.Parse(e.args) != nil || fs.NArg() != 0 {
		return e.fail(errUsage)
	}
	if *withParent && !lifeline(e.stdin) {
		return e.noLifeline()
	}
	relay, err := devderp.Start(logger.Discard)
	if err != nil {
		return e.fail(err)
	}
	defer relay.Close()
	worker := httptest.NewServer(fakeworker.New())
	defer func() {
		worker.CloseClientConnections() // a slot read waits up to 25 s
		worker.Close()
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if *withParent {
		ctx = untilParentExits(ctx, e.stdin)
	}
	if err := json.NewEncoder(e.stdout).Encode(map[string]string{"derpMap": relay.MapURL, "directory": worker.URL}); err != nil {
		return e.fail(err)
	}
	<-ctx.Done()
	return 0
}
