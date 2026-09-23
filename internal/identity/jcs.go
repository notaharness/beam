package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// maxSafe is the largest integer a JSON number carries exactly (2^53 − 1).
const maxSafe = 1<<53 - 1

var (
	errDuplicateKey = errors.New("canonical JSON: duplicate key")
	errNumber       = errors.New("canonical JSON: numbers must be safe integers")
	errTrailing     = errors.New("canonical JSON: data after the value")
	errUnicode      = errors.New("canonical JSON: invalid Unicode")
)

// Canonical returns data in the JSON Canonicalization Scheme (RFC 8785): keys
// sorted by UTF-16 code units, no whitespace, minimal string escapes. beam's
// statements carry only safe integers, so any other number is an error, as is
// a duplicate key or invalid Unicode.
func Canonical(data []byte) ([]byte, error) {
	if !validUnicode(data) {
		return nil, errUnicode
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var b bytes.Buffer
	if err := canonValue(d, &b); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errTrailing
	}
	return b.Bytes(), nil
}

func canonValue(d *json.Decoder, b *bytes.Buffer) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim: // only '{' or '[' can start a value
		if v == '{' {
			return canonObject(d, b)
		}
		return canonArray(d, b)
	case string:
		writeString(b, v)
	case json.Number:
		n, err := v.Int64()
		if err != nil || n > maxSafe || n < -maxSafe {
			return errNumber
		}
		b.WriteString(strconv.FormatInt(n, 10))
	case bool:
		b.WriteString(strconv.FormatBool(v))
	default:
		b.WriteString("null")
	}
	return nil
}

func canonObject(d *json.Decoder, b *bytes.Buffer) error {
	members := map[string][]byte{}
	var keys []string
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		key := tok.(string) // the decoder rejects any other token in key position
		if _, dup := members[key]; dup {
			return errDuplicateKey
		}
		var v bytes.Buffer
		if err := canonValue(d, &v); err != nil {
			return err
		}
		members[key] = v.Bytes()
		keys = append(keys, key)
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	slices.SortFunc(keys, func(a, b string) int {
		return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
	})
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeString(b, k)
		b.WriteByte(':')
		b.Write(members[k])
	}
	b.WriteByte('}')
	return nil
}

func canonArray(d *json.Decoder, b *bytes.Buffer) error {
	b.WriteByte('[')
	for i := 0; d.More(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := canonValue(d, b); err != nil {
			return err
		}
	}
	b.WriteByte(']')
	_, err := d.Token()
	return err
}

// validUnicode rejects what encoding/json would quietly replace with U+FFFD:
// bytes that are not UTF-8, and \u escapes that leave a surrogate unpaired
// (RFC 8785 §3.2.2.2). Malformed escapes are left to the decoder.
func validUnicode(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	inString := false
	for i := 0; i < len(data); i++ {
		switch c := data[i]; {
		case c == '"':
			inString = !inString
		case c == '\\' && inString:
			i++
			if i < len(data) && data[i] == 'u' {
				n, ok := surrogates(data[i+1:])
				if !ok {
					return false
				}
				i += n
			}
		}
	}
	return true
}

// surrogates reads the four hex digits after \u in b. A high surrogate must be
// followed by an escaped low one, and a low one cannot stand alone. It
// returns how many bytes it consumed.
func surrogates(b []byte) (int, bool) {
	r := hex4(b)
	switch {
	case r < 0xd800 || r > 0xdfff:
		return 4, true
	case r >= 0xdc00:
		return 0, false
	}
	if len(b) < 10 || b[4] != '\\' || b[5] != 'u' {
		return 0, false
	}
	lo := hex4(b[6:])
	return 10, lo >= 0xdc00 && lo <= 0xdfff
}

// hex4 is the value of four hex digits, or -1.
func hex4(b []byte) rune {
	if len(b) < 4 {
		return -1
	}
	n, err := strconv.ParseUint(string(b[:4]), 16, 16)
	if err != nil {
		return -1
	}
	return rune(n)
}

// writeString escapes as ECMAScript's JSON.stringify does: the two-character
// forms where they exist, \u00xx for other controls, everything else literal.
func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
