package identity

import (
	"crypto/hkdf"
	"crypto/sha256"
)

// PRFSalt is the PRF input whose output yields the directory keys.
const PRFSalt = "beam:v1:directory"

// DirectoryKeys derives K_dir, which encrypts directory blobs, and T_read, the
// directory read token, from the PRF output (docs/02).
func DirectoryKeys(first []byte) (kDir, tRead []byte) {
	kDir, _ = hkdf.Key(sha256.New, first, []byte("beam"), "dir", 32)
	tRead, _ = hkdf.Key(sha256.New, first, []byte("beam"), "read", 32)
	return kDir, tRead
}

// Fleet is fleet.json: the credential every record is verified under, the
// cached directory keys, and this machine's own entry. Binary fields are
// unpadded base64url.
type Fleet struct {
	V                   int    `json:"v"`
	FleetID             string `json:"fleetId"`
	CredentialID        string `json:"credentialId"`
	CredentialPublicKey string `json:"credentialPublicKey"`
	KDir                string `json:"kDir"`
	TRead               string `json:"tRead"`
	Entry               Record `json:"entry"`
}
