// Package mailbox is beam's durable mail (docs/05) over the store: envelopes,
// the flusher that delivers a peer's outbound queue, the receiver that stores
// what arrives, and the fan-out to subscribers.
package mailbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/notaharness/beam/internal/identity"
)

// Limits (docs/05).
const (
	MaxPayload  = 256 << 10 // decoded
	MaxEnvelope = 1 << 20   // serialized
	maxTopic    = 128       // scalar values
)

// Payload encodings. base64 is unpadded base64url, like every binary field.
const (
	UTF8   = "utf8"
	Base64 = "base64"
)

// Reasons an envelope is refused or rejected (docs/04, docs/05).
const (
	PayloadTooLarge = "payload-too-large"
	InvalidEnvelope = "invalid-envelope"
	InvalidTopic    = "invalid-topic"
	StorageFailure  = "storage-failure"
)

// Envelope is one message (docs/05).
type Envelope struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Seq       int64  `json:"seq"`
	Topic     string `json:"topic"`
	Payload   string `json:"payload"`
	Encoding  string `json:"encoding"`
	CreatedAt int64  `json:"createdAt"`
}

// New builds the envelope msg.send queues, all but its seq, or returns why it
// cannot be sent: InvalidTopic, PayloadTooLarge, or InvalidEnvelope for an
// unknown encoding or a payload that is not in it.
func New(from, to, topic, payload, encoding string, now int64) (Envelope, string) {
	e := Envelope{ID: newID(), From: from, To: to, Topic: topic, Payload: payload, Encoding: encoding, CreatedAt: now}
	if !identity.ValidText(topic, 0, maxTopic) {
		return Envelope{}, InvalidTopic
	}
	return e, e.checkPayload()
}

// newID is a random UUID (version 4).
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (e Envelope) checkPayload() string {
	n := len(e.Payload)
	switch e.Encoding {
	case UTF8:
		if !utf8.ValidString(e.Payload) {
			return InvalidEnvelope
		}
	case Base64:
		b, err := base64.RawURLEncoding.DecodeString(e.Payload)
		if err != nil {
			return InvalidEnvelope
		}
		n = len(b)
	default:
		return InvalidEnvelope
	}
	if n > MaxPayload {
		return PayloadTooLarge
	}
	return ""
}

// Marshal serializes e, refusing one over MaxEnvelope.
func (e Envelope) Marshal() ([]byte, string) {
	b, _ := json.Marshal(e)
	if len(b) > MaxEnvelope {
		return nil, PayloadTooLarge
	}
	return b, ""
}

// parse reads an envelope from peer addressed to self, or returns why it is
// refused. The id is returned whenever it can be read, for the ack.
func parse(b []byte, peer, self string) (Envelope, string) {
	var e Envelope
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	switch {
	case len(b) > MaxEnvelope:
		return e, PayloadTooLarge
	case d.Decode(&e) != nil || d.More():
		return Envelope{ID: e.ID}, InvalidEnvelope
	case e.From != peer || e.To != self || e.Seq < 1 || e.Seq > 1<<53-1 || !validID(e.ID) ||
		!identity.ValidText(e.Topic, 0, maxTopic):
		return e, InvalidEnvelope
	}
	return e, e.checkPayload()
}

func validID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range []byte(id) {
		dash := i == 8 || i == 13 || i == 18 || i == 23
		isHex := '0' <= c && c <= '9' || 'a' <= c && c <= 'f'
		if dash != (c == '-') || !dash && !isHex {
			return false
		}
	}
	return true
}
