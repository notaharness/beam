package stream

// PTY refuses a pty stream: Windows serves none (docs/04).
func PTY(c *Conn, _ Header, _ Spawn) {
	refuse(c, &refusal{"kind", "pty is not supported on windows"})
}
