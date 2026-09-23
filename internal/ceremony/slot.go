package ceremony

import (
	"context"
	"crypto/hpke"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// info binds a sealed result to its slot (docs/02, Ceremonies).
const info = "beam-ceremony:v1"

// retry is how long the wait on a slot pauses after the worker failed to
// answer; the worker itself holds a read up to 25 s for the write.
var retry = 2 * time.Second

var slotClient = &http.Client{Timeout: 40 * time.Second}

// suite is the HPKE suite the page seals with: DHKEM(X25519, HKDF-SHA256),
// HKDF-SHA256, AES-128-GCM.
func suite() (hpke.KDF, hpke.AEAD) { return hpke.HKDFSHA256(), hpke.AES128GCM() }

// open is a sealed result as the result it carries; anything that does not
// open under the ceremony's key, or is not a result, is not an answer to
// this ceremony.
func (c *Ceremony) open(sealed []byte) (Result, error) {
	k, err := hpke.NewDHKEMPrivateKey(c.key)
	if err != nil {
		return Result{}, err
	}
	kdf, aead := suite()
	plain, err := hpke.Open(k, kdf, aead, []byte(info+c.slot), sealed)
	if err != nil {
		return Result{}, ErrState
	}
	f, err := url.ParseQuery(string(plain))
	if err != nil {
		return Result{}, ErrState
	}
	return parse(f)
}

// errRead is a slot read already: its result went to a read whose answer
// never arrived, and will not come again.
var errRead = errors.New("the slot was read already")

// readSlot waits on the slot at worker until it holds a sealed result, and
// takes it, or until ctx ends or the slot turns out read already. A worker
// that fails to answer is asked again after retry, and one that answers
// nothing yet at most once a second.
func readSlot(ctx context.Context, worker, slot string, readKey []byte) ([]byte, error) {
	for {
		began := time.Now()
		sealed, err := readOnce(ctx, worker, slot, readKey)
		switch {
		case sealed != nil || errors.Is(err, errRead):
			return sealed, err
		case ctx.Err() != nil:
			return nil, ctx.Err()
		}
		pause := time.Second - time.Since(began)
		if err != nil {
			pause = retry
		}
		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
}

// readOnce is one read of the slot: its sealed result, nil for none yet, or
// the worker's failure.
func readOnce(ctx context.Context, worker, slot string, readKey []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", worker+"/v1/slots/"+slot, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b64(readKey))
	resp, err := slotClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		return nil, nil
	case http.StatusGone:
		return nil, errRead
	default:
		return nil, fmt.Errorf("slot read: %s", resp.Status)
	}
	var got struct {
		Sealed string `json:"sealed"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&got); err != nil {
		return nil, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(got.Sealed)
	if err != nil {
		return nil, err
	}
	return sealed, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
