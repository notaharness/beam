package cli

// runControl is `beam control --stdio`: the control connection on stdin and
// stdout, bytes as they are (docs/07). It lasts until stdin ends (exit 0) or
// the daemon closes it (exit 1).
func runControl(e *env) int {
	fs := e.flags("control")
	stdio := fs.Bool("stdio", false, "")
	if fs.Parse(e.args) != nil || fs.NArg() != 0 || !*stdio {
		return e.fail(errUsage)
	}
	c, err := e.connect()
	if err != nil {
		return e.fail(err)
	}
	if err := c.Proxy(e.stdin, e.stdout); err != nil {
		return e.fail(err)
	}
	return 0
}
