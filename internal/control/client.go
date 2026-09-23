package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/notaharness/beam/internal/stream"
)

// spawnWait is how long connect-or-spawn waits for a new daemon's socket.
const spawnWait = 5 * time.Second

// Client is a control connection to a daemon.
type Client struct {
	OnEvent func(Event) // when set, gets the events a Call reads instead of Next

	c      net.Conn
	r      *bufio.Reader
	id     int
	events []Event // read while a Call waited for its reply
}

// Event is one event line (docs/06).
type Event struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data"`
}

// Connect connects to the daemon at p, starting one with `beam daemon
// --detach` when none listens (docs/06, connect-or-spawn). env is the spawned
// daemon's environment. A daemon that loses the lock race exits in its own
// session; the winner's socket appears all the same.
func Connect(p Paths, env []string) (*Client, error) {
	c, err := Dial(p)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "daemon", "--detach")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil { // it refused to start, and says why
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	for deadline := time.Now().Add(spawnWait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err = Dial(p); err == nil {
			return c, nil
		}
	}
	return nil, err
}

// Dial connects to the daemon at p if one listens.
func Dial(p Paths) (*Client, error) {
	c, err := net.Dial("unix", p.Socket)
	if err != nil {
		return nil, err
	}
	return newClient(c), nil
}

func newClient(c net.Conn) *Client {
	return &Client{c: c, r: bufio.NewReader(c)}
}

// Close closes the connection.
func (c *Client) Close() error { return c.c.Close() }

// Call sends op with params and decodes its result into out. Events arriving
// meanwhile go to OnEvent, or are kept for Next. A refusal is an *OpError.
func (c *Client) Call(op string, params map[string]any, out any) error {
	c.id++
	req := map[string]any{"id": c.id, "op": op}
	for k, v := range params {
		req[k] = v
	}
	b, _ := stream.Marshal(req)
	if _, err := c.c.Write(append(b, '\n')); err != nil {
		return err
	}
	for {
		line, err := stream.ReadLine(c.r, maxControlLine)
		if err != nil {
			return err
		}
		var resp struct {
			Event
			ID     int             `json:"id"`
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
			Detail string          `json:"detail"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.ID != c.id {
			switch {
			case resp.Name == "":
			case c.OnEvent != nil:
				c.OnEvent(resp.Event)
			default:
				c.events = append(c.events, resp.Event)
			}
			continue
		}
		if !resp.OK {
			return &OpError{resp.Error, resp.Detail}
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	}
}

// Next returns the next event, waiting for one.
func (c *Client) Next() (Event, error) {
	if len(c.events) > 0 {
		ev := c.events[0]
		c.events = c.events[1:]
		return ev, nil
	}
	for {
		line, err := stream.ReadLine(c.r, maxControlLine)
		if err != nil {
			return Event{}, err
		}
		var ev Event
		if json.Unmarshal(line, &ev) == nil && ev.Name != "" {
			return ev, nil
		}
	}
}

// Attach opens the attach connection for a reserved stream; frames follow in
// both directions.
func Attach(p Paths, streamID string) (*stream.Conn, error) {
	c, err := net.Dial("unix", p.Socket)
	if err != nil {
		return nil, err
	}
	b, _ := stream.Marshal(map[string]string{"attach": streamID})
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.Close()
		return nil, err
	}
	return stream.NewConn(c), nil
}
