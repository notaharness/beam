package transport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"golang.org/x/crypto/curve25519"
)

// helloFrame is the dialer's hello (docs/04): its signed entry and the proof
// that it holds the node key the entry names.
type helloFrame struct {
	Entry json.RawMessage `json:"entry"`
	Nonce b64             `json:"nonce"`
	MAC   b64             `json:"mac"`
}

// b64 is bytes as unpadded base64url, the encoding of every binary field.
type b64 []byte

func (b b64) MarshalText() ([]byte, error) {
	return []byte(base64.RawURLEncoding.EncodeToString(b)), nil
}

func (b *b64) UnmarshalText(s []byte) error {
	d, err := base64.RawURLEncoding.DecodeString(string(s))
	*b = d
	return err
}

// newHello builds the hello a machine with key k sends on the tunnel whose
// client key is c, to the server whose node key is r.
func newHello(k *Key, c, r [32]byte, entry json.RawMessage) helloFrame {
	nonce := make([]byte, 32)
	rand.Read(nonce)
	s := k.NodePublic()
	mac, _ := possessionMAC(k.pk.Private.Raw32(), r, c, s, r, nonce)
	return helloFrame{Entry: entry, Nonce: nonce, MAC: mac}
}

// possessionMAC is HMAC-SHA256(k, "beam-hello:v1" ‖ C ‖ S ‖ R ‖ nonce) with
// k = X25519(priv, other). The dialer passes s and R, the acceptor r and S,
// and both arrive at the same k (docs/03, Admission).
func possessionMAC(priv, other, c, s, r [32]byte, nonce []byte) ([]byte, error) {
	k, err := curve25519.X25519(priv[:], other[:])
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte("beam-hello:v1"))
	m.Write(c[:])
	m.Write(s[:])
	m.Write(r[:])
	m.Write(nonce)
	return m.Sum(nil), nil
}
