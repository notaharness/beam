package control

import (
	"strings"
	"testing"
)

// docs/02 $BEAM_DIR: where the directory and the socket are, and which
// settings are refused or ignored. Every path beam uses is absolute.
func TestResolvePaths(t *testing.T) {
	long := "/" + strings.Repeat("s", maxSocket)
	for _, tc := range []struct {
		env        map[string]string
		dir, sock  string
		errMention string
	}{
		{map[string]string{"BEAM_CONFIG_DIR": "/b", "XDG_CONFIG_HOME": "/x", "HOME": "/h"}, "/b", "/b/run/beam.sock", ""},
		{map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"}, "/x/beam", "/x/beam/run/beam.sock", ""},
		{map[string]string{"HOME": "/h"}, "/h/.config/beam", "/h/.config/beam/run/beam.sock", ""},
		{map[string]string{"XDG_CONFIG_HOME": "x", "HOME": "/h"}, "/h/.config/beam", "/h/.config/beam/run/beam.sock", ""}, // relative: ignored
		{map[string]string{"BEAM_CONFIG_DIR": "/b", "BEAM_SOCKET": "/s/b.sock"}, "/b", "/s/b.sock", ""},
		{map[string]string{"BEAM_CONFIG_DIR": "b", "HOME": "/h"}, "", "", "BEAM_CONFIG_DIR"},
		{map[string]string{"BEAM_CONFIG_DIR": "/b", "BEAM_SOCKET": "b.sock"}, "", "", "BEAM_SOCKET"},
		{map[string]string{"HOME": "h"}, "", "", "HOME"},
		{map[string]string{"XDG_CONFIG_HOME": "x"}, "", "", "HOME"},
		{map[string]string{}, "", "", "HOME"},
		{map[string]string{"BEAM_CONFIG_DIR": "/b", "BEAM_SOCKET": long}, "", "", "BEAM_SOCKET"},
	} {
		p, err := ResolvePaths(func(k string) string { return tc.env[k] })
		switch {
		case tc.errMention != "" && (err == nil || !strings.Contains(err.Error(), tc.errMention)):
			t.Errorf("%v: %+v, %v; want an error naming %s", tc.env, p, err, tc.errMention)
		case tc.errMention == "" && (err != nil || p.Dir != tc.dir || p.Socket != tc.sock):
			t.Errorf("%v: %+v, %v; want %s and %s", tc.env, p, err, tc.dir, tc.sock)
		}
	}
}
