package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/notaharness/beam/internal/control"
	"github.com/notaharness/beam/internal/identity"
)

// runInit is `beam init [--label NAME] [--fleet-name NAME]` (docs/07).
func runInit(e *env) int {
	fs := e.flags("init")
	label := fs.String("label", "", "")
	fleetName := fs.String("fleet-name", "", "")
	if fs.Parse(e.args) != nil || fs.NArg() != 0 {
		return e.fail(errUsage)
	}
	var res struct {
		PeerID    string `json:"peerId"`
		FleetID   string `json:"fleetId"`
		Published any    `json:"published"`
	}
	err := e.ceremony("init", map[string]any{"label": *label, "fleetName": *fleetName}, "create", &res)
	if err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.stdout, "created fleet %s…  ·  this machine: %s (%s)\n%s\n", res.FleetID[:4], e.label(), identity.Fingerprint(res.PeerID), publication(res.Published))
	return 0
}

// runJoin is `beam join [--label NAME]` (docs/07).
func runJoin(e *env) int {
	fs := e.flags("join")
	label := fs.String("label", "", "")
	if fs.Parse(e.args) != nil || fs.NArg() != 0 {
		return e.fail(errUsage)
	}
	var res struct {
		FleetID string `json:"fleetId"`
		Members int    `json:"members"`
	}
	err := e.ceremony("join", map[string]any{"label": *label}, "sign", &res)
	var oe *control.OpError
	if errors.As(err, &oe) && oe.Code == "directory-unavailable" {
		fmt.Fprintln(e.stderr, "directory unavailable; try again")
		return 1
	}
	if err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.stdout, "joined fleet %s…; %d other machines known; connecting…\n", res.FleetID[:4], res.Members)
	return 0
}

// runRevoke is `beam revoke <peer>` (docs/07).
func runRevoke(e *env) int {
	if len(e.args) != 1 {
		return e.fail(errUsage)
	}
	var target struct {
		PeerID string `json:"peerId"`
	}
	var who struct {
		Peers []control.PeerView `json:"peers"`
	}
	if err := errors.Join(e.call("peer.resolve", map[string]any{"peer": e.args[0]}, &target), e.call("peers", nil, &who)); err != nil {
		return e.fail(err)
	}
	var res struct {
		Published      any `json:"published"`
		AcknowledgedBy int `json:"acknowledgedBy"`
	}
	if err := e.ceremony("revoke", map[string]any{"peer": e.args[0]}, "sign", &res); err != nil {
		return e.fail(err)
	}
	label, others := e.args[0], 0
	for _, p := range who.Peers {
		switch {
		case p.PeerID == target.PeerID:
			label = p.Label
		case p.RevokedAt == nil:
			others++
		}
	}
	fmt.Fprintf(e.stdout, "revoked %q on this machine\n%s\nacknowledged by %d of %d peers      (offline peers learn when they connect)\n",
		label, publication(res.Published), res.AcknowledgedBy, others)
	return 0
}

// runFleet is `beam fleet reset` (docs/07).
func runFleet(e *env) int {
	if len(e.args) != 1 || e.args[0] != "reset" {
		return e.fail(errUsage)
	}
	fmt.Fprint(e.stdout, `type "reset" to confirm: `)
	line, _ := bufio.NewReader(e.stdin).ReadString('\n')
	if strings.TrimSpace(line) != "reset" {
		fmt.Fprintln(e.stderr, "not reset")
		return 1
	}
	if err := e.call("fleet.reset", map[string]any{"confirm": "reset"}, nil); err != nil {
		return e.fail(err)
	}
	fmt.Fprintln(e.stdout, "fleet state removed; this machine keeps its key. Run beam init or beam join.")
	return 0
}

// ceremony runs op's ceremonies on one connection: op.start, then op.wait,
// showing its stages and each ceremony's URL as they come (the first's kind
// is first) and decoding the result into out.
func (e *env) ceremony(op string, params map[string]any, first string, out any) error {
	p, err := e.paths()
	if err != nil {
		return err
	}
	c, err := control.Dial(p)
	if err != nil {
		fmt.Fprintln(e.stdout, "starting daemon")
		if c, err = e.connect(); err != nil {
			return err
		}
	}
	defer c.Close()
	fmt.Fprintln(e.stdout, "preparing network")
	var start struct {
		CeremonyURL string `json:"ceremonyUrl"`
	}
	if err := c.Call(op+".start", params, &start); err != nil {
		return err
	}
	e.show(start.CeremonyURL, first)
	c.OnEvent = func(ev control.Event) {
		var data struct {
			CeremonyURL string `json:"ceremonyUrl"`
			Stage       string `json:"stage"`
		}
		switch {
		case json.Unmarshal(ev.Data, &data) != nil:
		case ev.Name == "ceremony":
			e.show(data.CeremonyURL, "sign")
		case ev.Name == "stage":
			fmt.Fprintln(e.stdout, data.Stage)
		}
	}
	return c.Call(op+".wait", nil, out)
}

// show shows a ceremony's URL and opens the browser when there is one; on a
// headless machine it says how to forward the ceremony's port first.
func (e *env) show(ceremonyURL, kind string) {
	fmt.Fprintf(e.stdout, "waiting for your passkey (%s)\n  %s\n", kind, ceremonyURL)
	if browser := e.browser(); browser != "" && exec.Command(browser, ceremonyURL).Start() == nil {
		return
	}
	u, _ := url.Parse(ceremonyURL)
	frag, _ := url.ParseQuery(u.Fragment)
	host, _ := os.Hostname()
	port := frag.Get("port")
	fmt.Fprintf(e.stdout, "forward the port first: ssh -L %s:127.0.0.1:%s %s\n", port, port, host)
}

// browser is the command that opens a URL here, or "" on a machine without
// a display.
func (e *env) browser() string {
	switch {
	case e.getenv("SSH_CONNECTION") != "":
		return ""
	case runtime.GOOS == "darwin":
		return "open"
	case e.getenv("DISPLAY") != "" || e.getenv("WAYLAND_DISPLAY") != "":
		return "xdg-open"
	}
	return ""
}

// label is this machine's label, from the daemon.
func (e *env) label() string {
	var st struct {
		Label string `json:"label"`
	}
	_ = e.call("status", nil, &st) // the result's line reads without it
	return st.Label
}

func publication(p any) string {
	if p == true {
		return "published to directory"
	}
	return "publication pending; will retry"
}
