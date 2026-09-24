package cli

import (
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/notaharness/beam/internal/control"
)

// docs/07 Ceremony errors, read from the spec itself: each token's
// explanation, the connection lost, a token the table does not have, and
// prf-unsupported's lines after it.
func TestExplainAsSpecified(t *testing.T) {
	doc, err := os.ReadFile("../../docs/07-cli.md")
	if err != nil {
		t.Fatal(err)
	}
	section := string(doc)[strings.Index(string(doc), "### Ceremony errors"):]
	rows := regexp.MustCompile("(?m)^\\| (`[a-z-]+`|connection lost) \\| (.+) \\|$").FindAllStringSubmatch(section, -1)
	if len(rows) != 18 {
		t.Fatalf("%d rows in docs/07's table", len(rows))
	}
	needs := regexp.MustCompile("(?s)```\n(beam needs WebAuthn PRF.*?)\n```").FindStringSubmatch(section)[1]
	for _, row := range rows {
		err := error(&control.OpError{Code: strings.Trim(row[1], "`")})
		if row[1] == "connection lost" {
			err = interrupted{io.ErrUnexpectedEOF}
		}
		want := row[2]
		if row[1] == "`prf-unsupported`" {
			want += "\n" + needs
		}
		if got := explain("init", err); got != want {
			t.Errorf("%s:\n got %q\nwant %q", row[1], got, want)
		}
	}
	internal := explain("init", &control.OpError{Code: "internal"})
	if got := explain("init", &control.OpError{Code: "something-new"}); got != internal {
		t.Errorf("an unknown token: %q, want internal's", got)
	}
	same := "Use the same fleet passkey; a new passkey creates a different fleet."
	for _, op := range []string{"join", "revoke"} {
		got := explain(op, &control.OpError{Code: "prf-unsupported"})
		if want := rows[0][2] + "\n" + same + "\n" + needs; got != want {
			t.Errorf("%s prf-unsupported:\n got %q\nwant %q", op, got, want)
		}
	}
	if got := explain("init", errors.New("the socket is not there")); got != "" {
		t.Errorf("an error before the ceremony: %q", got)
	}
}
