// Package directory is the fleet's encrypted, append-only log on the worker
// (docs/02, docs/09): blob encryption, the worker's HTTP client, and the
// records a reader keeps.
package directory

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"

	"github.com/notaharness/beam/internal/identity"
	"golang.org/x/crypto/chacha20poly1305"
)

// aadDomain separates directory blobs from anything else K_dir could seal.
const aadDomain = "beam-dir:v1"

// ErrJunk is a blob that does not open under K_dir, or holds a record whose
// statement does not hash to the statementHash stored with it.
var ErrJunk = errors.New("directory: blob does not hold its statement")

// Seal encrypts r, assertion included, for the directory:
// nonce(24) ‖ XChaCha20-Poly1305(kDir, nonce, aad, canonical(r)), with aad =
// "beam-dir:v1" ‖ fleetId (its 64 hex characters) ‖ statementHash.
func Seal(kDir []byte, fleetID string, r identity.Record) (statementHash [32]byte, blob []byte) {
	statementHash = r.StatementHash()
	aead, err := chacha20poly1305.NewX(kDir)
	if err != nil {
		panic(err) // K_dir is 32 bytes by derivation
	}
	b, _ := json.Marshal(r)
	plain, err := identity.Canonical(b)
	if err != nil {
		panic(err) // a record built in code with an unsafe integer
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(plain)+aead.Overhead())
	rand.Read(nonce)
	return statementHash, aead.Seal(nonce, nonce, plain, aad(fleetID, statementHash[:]))
}

// Open decrypts a blob and parses the record inside, refusing one whose
// statement does not hash to statementHash: a valid assertion appended with
// an unrelated blob costs its slot and nothing else.
func Open(kDir []byte, fleetID string, statementHash, blob []byte) (identity.Record, error) {
	aead, err := chacha20poly1305.NewX(kDir)
	if err != nil || len(blob) < chacha20poly1305.NonceSizeX {
		return identity.Record{}, ErrJunk
	}
	nonce, sealed := blob[:chacha20poly1305.NonceSizeX], blob[chacha20poly1305.NonceSizeX:]
	plain, err := aead.Open(nil, nonce, sealed, aad(fleetID, statementHash))
	if err != nil {
		return identity.Record{}, ErrJunk
	}
	r, err := identity.ParseRecord(plain)
	if err != nil {
		return identity.Record{}, ErrJunk
	}
	h := r.StatementHash()
	if !bytes.Equal(h[:], statementHash) {
		return identity.Record{}, ErrJunk
	}
	return r, nil
}

func aad(fleetID string, statementHash []byte) []byte {
	return append([]byte(aadDomain+fleetID), statementHash...)
}
