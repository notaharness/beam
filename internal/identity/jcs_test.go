package identity

import (
	"strings"
	"testing"
)

func TestCanonical(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		// RFC 8785 §3.2.3: keys sort by UTF-16 code units, so U+1F600 (a
		// surrogate pair, 0xD83D…) sorts before U+FB33.
		{"rfc sorting",
			`{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh","1":"One","\ud83d\ude00":"Emoji: Grinning Face","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`,
			"{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"\u00f6\":\"Latin Small Letter O With Diaeresis\",\"\u20ac\":\"Euro Sign\",\"\U0001F600\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}"},
		// RFC 8785 §3.2.2.2 string escaping.
		{"rfc strings",
			`{"string":"\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/"}`,
			`{"string":"€$\u000f\nA'B\"\\\\\"/"}`},
		{"short escapes", `["\b\f\t\r\n\u0001\u001f"]`, `["\b\f\t\r\n\u0001\u001f"]`},
		{"html and separators stay literal", `["<>&\u2028\u2029"]`, "[\"<>&\u2028\u2029\"]"},
		{"whitespace and nesting", ` { "b" : [ 1 , { "d" : true , "c" : null } ] , "a" : false } `, `{"a":false,"b":[1,{"c":null,"d":true}]}`},
		{"safe integers", `[0,-0,9007199254740991,-9007199254740991]`, `[0,0,9007199254740991,-9007199254740991]`},
		{"empty containers", `{"a":{},"b":[]}`, `{"a":{},"b":[]}`},
		{"an escaped backslash, then text", `["\\ud800"]`, `["\\ud800"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonical([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestCanonicalRejects(t *testing.T) {
	for _, in := range []string{
		`{"a":1,"a":2}`,       // duplicate key
		`{"a":{"b":1,"b":1}}`, // nested duplicate
		`[9007199254740992]`,  // beyond a safe integer
		`[-9007199254740992]`,
		`[1.5]`, // not an integer
		`[1.0]`,
		`[1e2]`,
		`{"a":1} {"b":2}`, // trailing value
		`{"a":1`,          // truncated
		``,
		// RFC 8785 §3.2.2.2: invalid Unicode fails rather than being repaired.
		"[\"\xff\"]",         // a byte that is not UTF-8
		"{\"\xc3\":1}",       // a truncated sequence, as a key
		`["\ud800"]`,         // a lone high surrogate
		`["\udc00"]`,         // a lone low surrogate
		`["\ud800\u0041"]`,   // a high surrogate not followed by a low one
		`{"\ude00\ud83d":1}`, // a pair in the wrong order, as a key
	} {
		if got, err := Canonical([]byte(in)); err == nil {
			t.Errorf("%s accepted as %s", in, got)
		}
	}
}

func TestCanonicalLongInput(t *testing.T) {
	in := `{"k":"` + strings.Repeat("x", 1<<16) + `"}`
	got, err := Canonical([]byte(in))
	if err != nil || string(got) != in {
		t.Fatalf("round trip of a long string failed: %v", err)
	}
}
