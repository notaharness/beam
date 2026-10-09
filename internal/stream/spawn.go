package stream

// Spawn is how the acceptor runs a stream's process.
type Spawn struct {
	Env    []string          // the daemon's environment, of which a login's part is passed on
	Inject map[string]string // BEAM_* variables, set last
	Home   string            // what ~ means
}

// refusal is why a header is refused before anything runs.
type refusal struct{ reason, detail string }

// refuse answers the header with r and closes the stream.
func refuse(c *Conn, r *refusal) {
	_ = c.WriteLine(Response{Reason: r.reason, Detail: r.detail}) // closing either way
	c.Close()
}
