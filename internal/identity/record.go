// Package identity is membership (docs/02): peer ids and labels, the signed
// member and revoke records, their canonical statements and verification, and
// the fleet's credential and directory keys.
package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

// Record kinds.
const (
	Member = "member"
	Revoke = "revoke"
)

// Record is a membership entry (Kind Member) or a revocation (Kind Revoke), as
// signed by the passkey and carried by sync and the directory. Binary fields
// are unpadded base64url.
type Record struct {
	V          int        `json:"v"`
	Kind       string     `json:"kind"`
	PeerID     string     `json:"peerId"`
	NodePublic string     `json:"nodePublic,omitempty"`
	Address    string     `json:"address,omitempty"`
	Label      string     `json:"label,omitempty"`
	IssuedAt   int64      `json:"issuedAt"`
	Assertion  *Assertion `json:"assertion,omitempty"`
}

// Assertion is a WebAuthn get assertion over a record's challenge.
type Assertion struct {
	CredentialID      string `json:"credentialId"`
	ClientDataJSON    string `json:"clientDataJSON"`
	AuthenticatorData string `json:"authenticatorData"`
	Signature         string `json:"signature"`
}

// ParseRecord decodes a record. It refuses JSON that has no canonical form
// (duplicate keys, numbers that are not safe integers) and fields a record
// does not have, so every parsed record has a statement.
func ParseRecord(data []byte) (Record, error) {
	if _, err := Canonical(data); err != nil {
		return Record{}, BadEntry
	}
	var r Record
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return Record{}, BadEntry
	}
	return r, nil
}

// Statement is what the passkey signs: the record without its assertion, as
// canonical JSON.
func (r Record) Statement() []byte {
	r.Assertion = nil
	b, _ := json.Marshal(r)
	c, err := Canonical(b)
	if err != nil {
		panic(err) // only a record built in code with an unsafe integer gets here
	}
	return c
}

// StatementHash is SHA-256 of the statement: what the directory stores and the
// challenge commits to.
func (r Record) StatementHash() [32]byte {
	return sha256.Sum256(r.Statement())
}

// Challenge is the WebAuthn challenge for the record:
// SHA-256(domain ‖ statementHash), which the directory worker can recompute
// without the plaintext.
func (r Record) Challenge() []byte {
	return Challenge(r.Kind, r.StatementHash())
}

// Challenge is the WebAuthn challenge for a statement of kind with
// statementHash.
func Challenge(kind string, statementHash [32]byte) []byte {
	c := sha256.Sum256(append([]byte("beam-"+kind+":v1"), statementHash[:]...))
	return c[:]
}

// Supersedes reports whether r replaces s, two valid entries for one node key:
// the later issuedAt wins, and a tie goes to the greater statement hash.
func (r Record) Supersedes(s Record) bool {
	if r.IssuedAt != s.IssuedAt {
		return r.IssuedAt > s.IssuedAt
	}
	a, b := r.StatementHash(), s.StatementHash()
	return bytes.Compare(a[:], b[:]) > 0
}

// Fingerprint is a peer or fleet id's display form (docs/02): its first 16
// characters, 64 bits, in groups of four.
func Fingerprint(id string) string {
	if len(id) < 16 {
		return id
	}
	return id[0:4] + " " + id[4:8] + " " + id[8:12] + " " + id[12:16]
}

// PeerID is the lowercase hex of the first 16 bytes of SHA-256(nodePublic).
func PeerID(nodePublic [32]byte) string {
	h := sha256.Sum256(nodePublic[:])
	return hex.EncodeToString(h[:16])
}

// ValidPeerID reports whether s is exactly 32 lowercase hex characters.
func ValidPeerID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidText reports whether s is min to max Unicode scalar values with none of
// / \ { } or a control character: the rule for labels and topics.
func ValidText(s string, min, max int) bool {
	if !utf8.ValidString(s) {
		return false
	}
	if n := utf8.RuneCountInString(s); n < min || n > max {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '\\' || r == '{' || r == '}' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidLabel reports whether s is a valid machine label: 1–64 scalar values.
func ValidLabel(s string) bool {
	return ValidText(s, 1, 64)
}
