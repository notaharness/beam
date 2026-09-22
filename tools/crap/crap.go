package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"strings"
)

// score is Savoia's CRAP metric: comp² × (1 − cov)³ + comp, with cov in [0, 1].
func score(comp int, cov float64) float64 {
	c, u := float64(comp), 1-cov
	return c*c*u*u*u + c
}

// fn is one function declaration and the source range its coverage is taken from.
type fn struct {
	name       string
	file       string
	start, end token.Position
	comp       int
}

// functions parses one file and returns its function declarations with their
// cyclomatic complexity, counted as gocyclo counts it.
func functions(file string, src []byte) ([]fn, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var fns []fn
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			fns = append(fns, fn{
				name:  f.Name.Name + "." + funcName(fd),
				file:  file,
				start: fset.Position(fd.Pos()),
				end:   fset.Position(fd.End()),
				comp:  complexity(fd),
			})
		}
	}
	return fns, nil
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		return "(*" + typeName(star.X) + ")." + fd.Name.Name
	}
	return typeName(t) + "." + fd.Name.Name
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return typeName(t.X)
	case *ast.IndexListExpr:
		return typeName(t.X)
	}
	return "?"
}

func complexity(fd *ast.FuncDecl) int {
	c := 1
	ast.Inspect(fd, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			c++
		case *ast.CaseClause:
			if n.List != nil {
				c++
			}
		case *ast.CommClause:
			if n.Comm != nil {
				c++
			}
		case *ast.BinaryExpr:
			if n.Op == token.LAND || n.Op == token.LOR {
				c++
			}
		}
		return true
	})
	return c
}

// block is one coverage profile block. The same block reported by several
// test binaries is merged: it is covered if any of them ran it.
type block struct {
	startLine, startCol, endLine, endCol int
	stmts                                int
	covered                              bool
}

// parseProfile reads a `go test -coverprofile` file into blocks keyed by the
// file's import path.
func parseProfile(r io.Reader) (map[string][]block, error) {
	type key struct {
		file string
		pos  [4]int
	}
	index := map[key]int{}
	blocks := map[string][]block{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		var file string
		var b block
		var count int
		i := strings.LastIndex(line, ":")
		if i < 0 {
			return nil, fmt.Errorf("bad profile line %q", line)
		}
		file = line[:i]
		if _, err := fmt.Sscanf(line[i+1:], "%d.%d,%d.%d %d %d",
			&b.startLine, &b.startCol, &b.endLine, &b.endCol, &b.stmts, &count); err != nil {
			return nil, fmt.Errorf("bad profile line %q: %v", line, err)
		}
		b.covered = count > 0
		k := key{file, [4]int{b.startLine, b.startCol, b.endLine, b.endCol}}
		if j, ok := index[k]; ok {
			blocks[file][j].covered = blocks[file][j].covered || b.covered
			continue
		}
		index[k] = len(blocks[file])
		blocks[file] = append(blocks[file], b)
	}
	return blocks, sc.Err()
}

// coverage is the fraction of f's statements that ran. A function with no
// blocks was not instrumented and counts as uncovered; one whose blocks hold
// no statements counts as covered.
func coverage(f fn, blocks []block) float64 {
	var total, covered, seen int
	for _, b := range blocks {
		if !within(f, b) {
			continue
		}
		seen++
		total += b.stmts
		if b.covered {
			covered += b.stmts
		}
	}
	switch {
	case seen == 0:
		return 0
	case total == 0:
		return 1
	}
	return float64(covered) / float64(total)
}

func within(f fn, b block) bool {
	after := b.startLine > f.start.Line || b.startLine == f.start.Line && b.startCol >= f.start.Column
	before := b.endLine < f.end.Line || b.endLine == f.end.Line && b.endCol <= f.end.Column
	return after && before
}
