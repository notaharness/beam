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

// runMsgSend is `beam msg send <peer> [--topic T] [--base64] [--json]
// <payload|->`.
func runMsgSend(e *env) int {
	params, asJSON, err := msgSendParams(e)
	if err != nil {
		return e.fail(err)
	}
	var raw json.RawMessage
	if err := e.call("msg.send", params, &raw); err != nil {
		return e.fail(err)
	}
	var res struct {
		Outcome       string `json:"outcome"`
		PendingReason string `json:"pendingReason"`
		Reason        string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &res) // the daemon's own result
	peer := params["to"].(string)
	switch {
	case asJSON:
		e.printJSON(raw)
	case res.Outcome == "delivered":
		fmt.Fprintf(e.stdout, "delivered to %s\n", peer)
	case res.Outcome == "stored":
		fmt.Fprintf(e.stdout, storedSays[res.PendingReason], peer)
	default:
		fmt.Fprintf(e.stderr, "rejected: %s\n", res.Reason)
	}
	if res.Outcome != "delivered" && res.Outcome != "stored" {
		return 1
	}
	return 0
}

// msgSendParams reads msg send's arguments as msg.send's params, and --json.
func msgSendParams(e *env) (map[string]any, bool, error) {
	if len(e.args) == 0 {
		return nil, false, errUsage
	}
	peer := e.args[0]
	fs := e.flags("msg send")
	topic := fs.String("topic", "", "")
	b64 := fs.Bool("base64", false, "")
	asJSON := fs.Bool("json", false, "")
	if fs.Parse(e.args[1:]) != nil || fs.NArg() != 1 {
		return nil, false, errUsage
	}
	payload := []byte(fs.Arg(0))
	if fs.Arg(0) == "-" {
		var err error
		if payload, err = io.ReadAll(e.stdin); err != nil {
			return nil, false, err
		}
	}
	params := map[string]any{"to": peer, "topic": *topic, "payload": string(payload), "encoding": "utf8"}
	if *b64 {
		params["payload"], params["encoding"] = base64.RawURLEncoding.EncodeToString(payload), "base64"
	}
	return params, *asJSON, nil
}

// storedSays is what `beam msg send` says of mail stored for a peer, by its
// pendingReason (docs/05).
var storedSays = map[string]string{
	"offline": "stored for %[1]s; delivery pending (%[1]s is offline). beam will deliver it when %[1]s connects. Do not send it again.\n",
	"grant":   "stored for %[1]s; delivery pending (%[1]s's grant refuses mail from this machine). beam will deliver it once %[1]s allows it. Do not send it again.\n",
	"no-ack":  "stored for %[1]s; delivery pending (%[1]s has not acknowledged it). beam will keep delivering it until %[1]s does. Do not send it again.\n",
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
