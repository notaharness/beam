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
)

// maxSafe is the largest integer a JSON number carries exactly (2^53 − 1).
const maxSafe = 1<<53 - 1

var (
	errDuplicateKey = errors.New("canonical JSON: duplicate key")
	errNumber       = errors.New("canonical JSON: numbers must be safe integers")
	errTrailing     = errors.New("canonical JSON: data after the value")
)

// Canonical returns data in the JSON Canonicalization Scheme (RFC 8785): keys
// sorted by UTF-16 code units, no whitespace, minimal string escapes. beam's
// statements carry only safe integers, so any other number is an error, as is
// a duplicate key.
func Canonical(data []byte) ([]byte, error) {
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
