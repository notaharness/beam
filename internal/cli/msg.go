package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// runMsg is `beam msg send|listen|queue` (docs/07).
func runMsg(e *env) int {
	if len(e.args) == 0 {
		return e.fail(errUsage)
	}
	sub := map[string]func(*env) int{"send": runMsgSend, "listen": runMsgListen, "queue": runMsgQueue}[e.args[0]]
	if sub == nil {
		return e.fail(errUsage)
	}
	e.args = e.args[1:]
	return sub(e)
}

// runMsgSend is `beam msg send <peer> [--topic T] [--base64] <payload|->`.
func runMsgSend(e *env) int {
	if len(e.args) == 0 {
		return e.fail(errUsage)
	}
	peer := e.args[0]
	fs := e.flags("msg send")
	topic := fs.String("topic", "", "")
	b64 := fs.Bool("base64", false, "")
	if fs.Parse(e.args[1:]) != nil || fs.NArg() != 1 {
		return e.fail(errUsage)
	}
	payload := []byte(fs.Arg(0))
	if fs.Arg(0) == "-" {
		var err error
		if payload, err = io.ReadAll(e.stdin); err != nil {
			return e.fail(err)
		}
	}
	params := map[string]any{"to": peer, "topic": *topic, "payload": string(payload), "encoding": "utf8"}
	if *b64 {
		params["payload"], params["encoding"] = base64.RawURLEncoding.EncodeToString(payload), "base64"
	}
	var res struct {
		Outcome       string `json:"outcome"`
		PendingReason string `json:"pendingReason"`
		Reason        string `json:"reason"`
	}
	if err := e.call("msg.send", params, &res); err != nil {
		return e.fail(err)
	}
	switch {
	case res.Outcome == "delivered":
		fmt.Fprintf(e.stdout, "delivered to %s\n", peer)
	case res.Outcome == "stored" && res.PendingReason == "no-ack":
		fmt.Fprintf(e.stdout, "stored for %s; delivery pending (%s has not acknowledged it). beam will keep delivering it until %s does. Do not send it again.\n", peer, peer, peer)
	case res.Outcome == "stored":
		fmt.Fprintf(e.stdout, "stored for %s; delivery pending (%s is offline). beam will deliver it when %s connects. Do not send it again.\n", peer, peer, peer)
	default:
		fmt.Fprintf(e.stderr, "rejected: %s\n", res.Reason)
		return 1
	}
	return 0
}

// runMsgListen is `beam msg listen [--topic T] [<peer>...]`: one envelope per
// line, each acked once written.
func runMsgListen(e *env) int {
	fs := e.flags("msg listen")
	params := map[string]any{}
	fs.Func("topic", "", func(t string) error { params["topic"] = t; return nil }) // absent: any topic
	if fs.Parse(e.args) != nil {
		return e.fail(errUsage)
	}
	params["from"] = fs.Args()
	c, err := e.connect()
	if err != nil {
		return e.fail(err)
	}
	defer c.Close()
	if err := c.Call("msg.subscribe", params, nil); err != nil {
		return e.fail(err)
	}
	for {
		ev, err := c.Next()
		if err != nil {
			return e.fail(err)
		}
		if ev.Name != "mail" {
			continue
		}
		if _, err := fmt.Fprintf(e.stdout, "%s\n", ev.Data); err != nil {
			return e.fail(err) // unacked, so delivered again
		}
		var env struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(ev.Data, &env) // an envelope without an id fails its ack
		if err := c.Call("msg.ack", map[string]any{"envelopeId": env.ID}, nil); err != nil {
			return e.fail(err)
		}
	}
}

// runMsgQueue is `beam msg queue [<peer>] [--which outbound|inbound|refused|quarantine]`:
// one {envelope, reason?} per line.
func runMsgQueue(e *env) int {
	params := map[string]any{}
	args := e.args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		params["peer"], args = args[0], args[1:]
	}
	fs := e.flags("msg queue")
	which := fs.String("which", "outbound", "")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return e.fail(errUsage)
	}
	params["which"] = *which
	c, err := e.connect()
	if err != nil {
		return e.fail(err)
	}
	defer c.Close()
	for {
		var page struct {
			Items []json.RawMessage `json:"items"`
			Next  string            `json:"next"`
		}
		if err := c.Call("msg.queue", params, &page); err != nil {
			return e.fail(err)
		}
		for _, it := range page.Items {
			fmt.Fprintf(e.stdout, "%s\n", it)
		}
		if page.Next == "" {
			return 0
		}
		params["cursor"] = page.Next
	}
}
