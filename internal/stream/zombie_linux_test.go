package stream

import (
	"bytes"
	"os"
	"strconv"
)

// zombie reports whether pid has exited and is not yet reaped.
func zombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	i := bytes.LastIndexByte(b, ')')
	return err == nil && i >= 0 && bytes.HasPrefix(b[i+1:], []byte(" Z"))
}
