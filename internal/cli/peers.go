package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/notaharness/beam/internal/control"
)

// fingerprint is a peer id's display form: 16 characters in groups of four.
func fingerprint(id string) string {
	if len(id) < 16 {
		return id
	}
	return id[0:4] + " " + id[4:8] + " " + id[8:12] + " " + id[12:16]
}

// jsonFlag parses a command's only flag, --json.
func (e *env) jsonFlag(name string) (bool, bool) {
	fs := e.flags(name)
	asJSON := fs.Bool("json", false, "")
	if fs.Parse(e.args) != nil || fs.NArg() != 0 {
		return false, false
	}
	return *asJSON, true
}

func (e *env) printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(e.stdout, string(b))
}

type status struct {
	Version  string `json:"version"`
	Ready    bool   `json:"ready"`
	Enrolled bool   `json:"enrolled"`
	PeerID   string `json:"peerId"`
	Label    string `json:"label"`
	FleetID  string `json:"fleetId"`
	DERP     struct {
		Region string `json:"region"`
	} `json:"derp"`
	Peers struct {
		Connected, Offline, Revoked, RevokedByFleet int
	} `json:"peers"`
}

func runStatus(e *env) int {
	asJSON, ok := e.jsonFlag("status")
	if !ok {
		return e.fail(errUsage)
	}
	var raw json.RawMessage
	if err := e.call("status", nil, &raw); err != nil {
		return e.fail(err)
	}
	if asJSON {
		e.printJSON(raw)
		return 0
	}
	var s status
	if err := json.Unmarshal(raw, &s); err != nil {
		return e.fail(err)
	}
	if !s.Enrolled {
		fmt.Fprintf(e.stdout, "daemon %s · not enrolled: run beam init or beam join\n", s.Version)
		return 0
	}
	fmt.Fprintf(e.stdout, "this machine: %s (%s) · fleet %s… · relay %s\n", s.Label, fingerprint(s.PeerID), s.FleetID[:4], s.DERP.Region)
	fmt.Fprintf(e.stdout, "peers: %d connected, %d offline, %d revoked\n", s.Peers.Connected, s.Peers.Offline, s.Peers.Revoked)
	if n := s.Peers.RevokedByFleet; n > 0 {
		fmt.Fprintf(e.stdout, "this machine is revoked: %d peers refuse it\n", n)
	}
	return 0
}

func runPeers(e *env) int {
	asJSON, ok := e.jsonFlag("peers")
	if !ok {
		return e.fail(errUsage)
	}
	c, err := e.connect()
	if err != nil {
		return e.fail(err)
	}
	defer c.Close()
	var all []control.PeerView
	for cursor := ""; ; {
		var page struct {
			Peers []control.PeerView `json:"peers"`
			Next  string             `json:"next"`
		}
		if err := c.Call("peers", map[string]any{"cursor": cursor}, &page); err != nil {
			return e.fail(err)
		}
		all = append(all, page.Peers...)
		if cursor = page.Next; cursor == "" {
			break
		}
	}
	if asJSON {
		e.printJSON(map[string]any{"peers": all})
		return 0
	}
	e.printPeers(all)
	return 0
}

func (e *env) printPeers(peers []control.PeerView) {
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tSTATE\tINBOUND\tPATH\tGRANT\tQUEUE out/in/refused")
	for _, p := range peers {
		name := p.Label
		if p.Alias != nil {
			name = *p.Alias
		}
		inbound := "-"
		if p.Inbound {
			inbound = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d/%d/%d\n", name, fingerprint(p.PeerID), p.State, inbound, p.Path,
			p.Grant, p.Queue.Outbound, p.Queue.Inbound, p.Queue.Refused)
	}
	w.Flush()
}

// runPeer is `beam peer alias <peer> <alias|->` and `beam peer grant <peer> <grant>`.
func runPeer(e *env) int {
	if len(e.args) != 3 {
		return e.fail(errUsage)
	}
	sub, peer, value := e.args[0], e.args[1], e.args[2]
	params := map[string]any{"peer": peer}
	switch sub {
	case "alias":
		params["alias"] = &value
		if value == "-" {
			params["alias"] = nil
		}
	case "grant":
		params["grant"] = value
	default:
		return e.fail(errUsage)
	}
	if err := e.call("peer."+sub, params, nil); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.stdout, "%s %s: %s\n", sub, peer, strings.TrimSpace(value))
	return 0
}
