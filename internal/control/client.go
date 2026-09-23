package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/notaharness/beam/internal/stream"
)

// spawnWait is how long connect-or-spawn waits for a new daemon's socket.
const spawnWait = 5 * time.Second

// Client is a control connection to a daemon.
type Client struct {
	c  net.Conn
	r  *bufio.Reader
	id int
}

// Connect connects to the daemon at p, starting one with `beam daemon
// --detach` when none listens (docs/06, connect-or-spawn). env is the spawned
// daemon's environment. A spawn that loses the lock race exits; the winner's
// socket appears all the same.
func Connect(p Paths, env []string) (*Client, error) {
	c, err := net.Dial("unix", p.Socket)
	if err == nil {
		return newClient(c), nil
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
	spawnErr := cmd.Run() // losing a race to start still leaves a daemon to reach
	for deadline := time.Now().Add(spawnWait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err = net.Dial("unix", p.Socket); err == nil {
			return newClient(c), nil
		}
	}
	return nil, errors.Join(spawnErr, err)
}

func newClient(c net.Conn) *Client {
	return &Client{c: c, r: bufio.NewReader(c)}
}

// Close closes the connection.
func (c *Client) Close() error { return c.c.Close() }

// Call sends op with params and decodes its result into out. Events arriving
// meanwhile are skipped. A refusal is an *OpError.
func (c *Client) Call(op string, params map[string]any, out any) error {
	c.id++
	req := map[string]any{"id": c.id, "op": op}
	for k, v := range params {
		req[k] = v
	}
	b, _ := json.Marshal(req)
	if _, err := c.c.Write(append(b, '\n')); err != nil {
		return err
	}
	for {
		line, err := stream.ReadLine(c.r, maxControlLine)
		if err != nil {
			return err
		}
		var resp struct {
			ID     int             `json:"id"`
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
			Detail string          `json:"detail"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.ID != c.id {
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

// Attach opens the attach connection for a reserved stream; frames follow in
// both directions.
func Attach(p Paths, streamID string) (*stream.Conn, error) {
	c, err := net.Dial("unix", p.Socket)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(map[string]string{"attach": streamID})
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.Close()
		return nil, err
	}
	return stream.NewConn(c), nil
}
