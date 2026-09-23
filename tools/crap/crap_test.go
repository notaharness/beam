package main

import (
	"math"
	"strings"
	"testing"
)

func TestScore(t *testing.T) {
	for _, tc := range []struct {
		comp int
		cov  float64
		want float64
	}{
		{1, 0, 2},
		{1, 1, 1},
		{5, 0, 30},
		{5, 1, 5},
		{10, 0.5, 22.5},
		{12, 0.5, 30},
		{30, 1, 30},
	} {
		if got := score(tc.comp, tc.cov); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("score(%d, %v) = %v, want %v", tc.comp, tc.cov, got, tc.want)
		}
	}
}

const src = `package p

func straight() int { return 1 }

func branches(a int, b bool) int {
	if a > 0 && b {
		return 1
	}
	for i := 0; i < a; i++ {
	}
	for range a {
	}
	switch a {
	case 1, 2:
	case 3:
	default:
	}
	select {
	case <-make(chan int):
	default:
	}
	f := func() bool { return a < 0 || b }
	return 0
}

type T struct{}

func (t *T) method(x any) {
	switch x.(type) {
	case int:
	}
}

func (T) value() {}
`

func TestFunctions(t *testing.T) {
	fns, err := functions("m/p/p.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"p.straight":    1,
		"p.branches":    9,
		"p.(*T).method": 2,
		"p.T.value":     1,
	}
	if len(fns) != len(want) {
		t.Fatalf("got %d functions, want %d: %+v", len(fns), len(want), fns)
	}
	for _, f := range fns {
		if want[f.name] != f.comp {
			t.Errorf("%s: complexity %d, want %d", f.name, f.comp, want[f.name])
		}
	}
}

const profile = `mode: atomic
m/p/p.go:3.23,3.33 1 4
m/p/p.go:5.34,6.15 1 1
m/p/p.go:6.15,8.3 1 0
m/p/p.go:9.2,9.29 1 1
m/p/p.go:9.2,9.29 1 0
m/p/p.go:34.18,34.19 0 0
m/q/q.go:1.1,2.2 3 0
`

func TestParseProfile(t *testing.T) {
	blocks, err := parseProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(blocks["m/p/p.go"]); n != 5 {
		t.Fatalf("p.go: %d distinct blocks, want 5 (duplicates merged)", n)
	}
	for _, b := range blocks["m/p/p.go"] {
		if b.startLine == 9 && !b.covered {
			t.Error("a block covered by any test binary is covered")
		}
	}
	if _, err := parseProfile(strings.NewReader("mode: set\nbad line\n")); err == nil {
		t.Error("malformed line accepted")
	}
}

func TestCoverage(t *testing.T) {
	blocks, _ := parseProfile(strings.NewReader(profile))
	fns, _ := functions("m/p/p.go", []byte(src))
	got := map[string]float64{}
	for _, f := range fns {
		got[f.name] = coverage(f, blocks["m/p/p.go"])
	}
	want := map[string]float64{
		"p.straight": 1,
		"p.branches": 2.0 / 3,
		"p.T.value":  1, // no statements
	}
	for name, w := range want {
		if math.Abs(got[name]-w) > 1e-9 {
			t.Errorf("%s: coverage %v, want %v", name, got[name], w)
		}
	}
	if got["p.(*T).method"] != 0 {
		t.Errorf("function absent from the profile: coverage %v, want 0", got["p.(*T).method"])
	}
}
