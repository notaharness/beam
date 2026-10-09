package stream

// PTY refuses a pty stream: Windows serves none (docs/04).
func PTY(c *Conn, _ Header, _ Spawn) {
	refuse(c, &refusal{"kind", "pty is not supported on windows"})
}

// Exec refuses an exec stream: Windows serves none yet (docs/04).
func Exec(c *Conn, _ Header, _ Spawn) {
	refuse(c, &refusal{"kind", "exec is not supported on windows"})
}
