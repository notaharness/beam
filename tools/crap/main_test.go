package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const complexSrc = `package a

func Hot(x int) int {
	if x == 1 { return 1 }
	if x == 2 { return 2 }
	if x == 3 { return 3 }
	if x == 4 { return 4 }
	if x == 5 { return 5 }
	return 0
}

func Cold() {}
`

func TestRun(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":                  "module m\n\ngo 1.27.1\n",
		"a/a.go":                  complexSrc,
		"a/a_test.go":             "package a\n\nfunc TestIgnored() { if true {} }\n",
		"node_modules/x/x.go":     "package x\n\nfunc Skipped() { if true {} }\n",
		"a/testdata/fixture/f.go": "package f\n\nfunc Skipped() { if true {} }\n",
	})
	for _, tc := range []struct {
		name    string
		profile string
		ok      bool
		report  []string
	}{
		{
			name:    "uncovered complex function fails",
			profile: "mode: set\nm/a/a.go:3.21,4.12 1 0\n",
			ok:      false,
			report:  []string{"a.Hot", "a/a.go:3", "42.0"},
		},
		{
			name:    "covered complex function passes",
			profile: "mode: set\nm/a/a.go:3.21,4.12 1 1\nm/a/a.go:12.13,12.14 0 0\n",
			ok:      true,
			report:  []string{"2 functions", "max CRAP 6.0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			ok, err := run(root, strings.NewReader(tc.profile), &out)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.ok {
				t.Errorf("ok = %v, want %v\n%s", ok, tc.ok, out.String())
			}
			for _, s := range tc.report {
				if !strings.Contains(out.String(), s) {
					t.Errorf("report lacks %q:\n%s", s, out.String())
				}
			}
		})
	}
}

func TestRunErrors(t *testing.T) {
	if _, err := run(t.TempDir(), strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Error("missing go.mod accepted")
	}
	root := writeTree(t, map[string]string{"go.mod": "module m\n", "a.go": "package a\nfunc {"})
	if _, err := run(root, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Error("unparsable source accepted")
	}
	root = writeTree(t, map[string]string{"go.mod": "module m\n"})
	if _, err := run(root, strings.NewReader("junk"), &bytes.Buffer{}); err == nil {
		t.Error("bad profile accepted")
	}
}

func TestCLI(t *testing.T) {
	t.Chdir(writeTree(t, map[string]string{
		"go.mod":   "module m\n",
		"a/a.go":   complexSrc,
		"pass.out": "mode: set\nm/a/a.go:3.21,4.12 1 1\n",
		"fail.out": "mode: set\nm/a/a.go:3.21,4.12 1 0\n",
	}))
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"a", "b"}, 2},
		{[]string{"missing.out"}, 1},
		{[]string{"fail.out"}, 1},
		{[]string{"pass.out"}, 0},
	} {
		var stdout, stderr bytes.Buffer
		if got := cli(tc.args, &stdout, &stderr); got != tc.code {
			t.Errorf("cli(%q) = %d, want %d\nstdout: %s\nstderr: %s", tc.args, got, tc.code, &stdout, &stderr)
		}
	}
}
