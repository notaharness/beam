// Command crap fails when any function's CRAP score exceeds the limit.
//
// Usage: go run ./tools/crap cover.out
//
// CRAP (Change Risk Anti-Patterns, Savoia) is comp² × (1 − cov)³ + comp for a
// function of cyclomatic complexity comp whose statements are covered by
// fraction cov. Complexity is counted as gocyclo counts it; coverage comes from
// a `go test -coverprofile` run over the whole module.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// limit is crap4j's threshold: above it a function is too complex for its tests.
const limit = 30

func main() {
	os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr))
}

func cli(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: crap cover.out")
		return 2
	}
	f, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer f.Close()
	ok, err := run(".", f, stdout)
	switch {
	case err != nil:
		fmt.Fprintln(stderr, err)
		return 1
	case !ok:
		return 1
	}
	return 0
}

type result struct {
	fn
	cov, crap float64
}

// run scores every function in the module at root that the build under
// profile compiled (its file is in the profile; another OS's is not) and
// writes a report. It reports false if any function exceeds the limit.
func run(root string, profile io.Reader, out io.Writer) (bool, error) {
	module, err := modulePath(root)
	if err != nil {
		return false, err
	}
	blocks, err := parseProfile(profile)
	if err != nil {
		return false, err
	}
	fns, err := sources(root)
	if err != nil {
		return false, err
	}
	var results []result
	for _, f := range fns {
		fb, built := blocks[path.Join(module, f.file)]
		if !built {
			continue
		}
		cov := coverage(f, fb)
		results = append(results, result{f, cov, score(f.comp, cov)})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].crap > results[j].crap })
	return report(results, out), nil
}

func modulePath(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m, ok := strings.CutPrefix(sc.Text(), "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", errors.New("go.mod has no module line")
}

// sources returns the functions of every non-test Go file under root, skipping
// test fixtures and JavaScript dependencies.
func sources(root string) ([]fn, error) {
	var fns []fn
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && (d.Name() == "testdata" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && p != root):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go"):
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		got, err := functions(filepath.ToSlash(rel), src)
		fns = append(fns, got...)
		return err
	})
	return fns, err
}

func report(results []result, out io.Writer) bool {
	if len(results) == 0 {
		fmt.Fprintln(out, "crap: no functions")
		return true
	}
	var over int
	var covSum float64
	for _, r := range results {
		covSum += r.cov
		if r.crap > limit {
			over++
		}
	}
	top := results[0]
	fmt.Fprintf(out, "crap: %d functions, mean coverage %.1f%%, max CRAP %.1f (%s), limit %d\n",
		len(results), 100*covSum/float64(len(results)), top.crap, top.name, limit)
	for _, r := range results[:over] {
		fmt.Fprintf(out, "  %6.1f  %s:%d  %s  (complexity %d, coverage %.0f%%)\n",
			r.crap, r.file, r.start.Line, r.name, r.comp, 100*r.cov)
	}
	return over == 0
}
