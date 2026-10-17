package directory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/notaharness/beam/internal/identity"
)

// DefaultURL is the worker.
const DefaultURL = "https://beam.n10.is"

// The worker's bounds (docs/09): a page holds at most PageSize entries, a
// fleet at most MaxEntries.
const (
	PageSize   = 500
	MaxEntries = 5000
)

// A client's bounds on the worker: a request's time, a whole Read's, and a
// response's size, that of a full page of the largest entries.
const (
	requestTimeout = 20 * time.Second
	readTimeout    = time.Minute
	maxBody        = PageSize << 14
)

// Client errors: the worker could not be reached or asked to wait
// (Unavailable), the read token opens no fleet (Unauthorized), an append
// names no fleet (NoFleet), a registration names one that exists (Exists),
// the directory holds a statement under another blob (Conflict).
var (
	ErrUnavailable  = errors.New("directory-unavailable")
	ErrUnauthorized = errors.New("directory: read token opens no fleet")
	ErrNoFleet      = errors.New("directory: no such fleet")
	ErrExists       = errors.New("directory: fleet exists")
	ErrConflict     = errors.New("directory: the statement is held with another blob")
)

// Refused is any other refusal, with the worker's reason.
type Refused struct {
	Status int
	Reason string
}

func (r *Refused) Error() string {
	return fmt.Sprintf("directory refused (%d): %s", r.Status, r.Reason)
}

// Entry is one record as the worker holds it: the statement's hash, the
// sealed record and its assertion. Seq is set on reads, Kind on appends.
// Binary fields are unpadded base64url.
type Entry struct {
	Seq           int64               `json:"seq,omitempty"`
	Kind          string              `json:"kind,omitempty"`
	StatementHash string              `json:"statementHash"`
	Blob          string              `json:"blob"`
	Assertion     *identity.Assertion `json:"assertion"`
}

// NewEntry seals r for fleetID's directory.
func NewEntry(kDir []byte, fleetID string, r identity.Record) Entry {
	h, blob := Seal(kDir, fleetID, r)
	return Entry{Kind: r.Kind, StatementHash: b64(h[:]), Blob: b64(blob), Assertion: r.Assertion}
}

// Page is one page of a fleet's directory.
type Page struct {
	FleetID             string  `json:"fleetId"`
	CredentialID        string  `json:"credentialId"`
	CredentialPublicKey string  `json:"credentialPublicKey"`
	Entries             []Entry `json:"entries"`
	Next                int64   `json:"next,omitempty"`
}

// Records opens every entry of p under kDir, skipping junk. The caller
// verifies each.
func (p Page) Records(kDir []byte) []identity.Record {
	var rs []identity.Record
	for _, e := range p.Entries {
		h, err1 := base64.RawURLEncoding.DecodeString(e.StatementHash)
		blob, err2 := base64.RawURLEncoding.DecodeString(e.Blob)
		if err1 != nil || err2 != nil {
			continue
		}
		if r, err := Open(kDir, p.FleetID, h, blob); err == nil {
			rs = append(rs, r)
		}
	}
	return rs
}

// Registration is the request that creates a fleet with its first entry.
type Registration struct {
	CredentialID        string `json:"credentialId"`
	CredentialPublicKey string `json:"credentialPublicKey"`
	ReadToken           string `json:"readToken"`
	First               Entry  `json:"first"`
}

// Client talks to the worker at URL.
type Client struct {
	URL string
}

// Register creates the fleet of cred, readable with readToken, with first
// as its first entry.
func (c Client) Register(ctx context.Context, cred identity.Credential, readToken []byte, first Entry) error {
	first.Kind = ""
	body := Registration{CredentialID: cred.ID, CredentialPublicKey: b64(cred.PublicKey), ReadToken: b64(readToken), First: first}
	_, err := c.do(ctx, http.MethodPost, "/v1/fleets", "", body, nil)
	return err
}

// Append adds e to fleetID's directory and returns its seq. held says the
// directory had e's statement already, with the seq it has and the blob
// appended first, which need not be e's (docs/02).
func (c Client) Append(ctx context.Context, fleetID string, e Entry) (seq int64, held bool, err error) {
	var res struct {
		Seq int64 `json:"seq"`
	}
	code, err := c.do(ctx, http.MethodPost, "/v1/fleets/"+fleetID+"/entries", "", e, &res)
	return res.Seq, code == http.StatusOK, err
}

// Read returns the whole directory readToken opens: every page's entries
// under the first page's fleet and credential. A worker that answers more
// than its bounds allow, or pages without advancing, is unavailable.
func (c Client) Read(ctx context.Context, readToken []byte) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var all Page
	for since := int64(0); ; {
		var p Page
		if _, err := c.do(ctx, http.MethodGet, "/v1/entries?since="+strconv.FormatInt(since, 10), b64(readToken), nil, &p); err != nil {
			return Page{}, err
		}
		switch {
		case len(p.Entries) > PageSize || len(all.Entries)+len(p.Entries) > MaxEntries:
			return Page{}, fmt.Errorf("%w: more entries than a directory holds", ErrUnavailable)
		case p.Next != 0 && (p.Next <= since || len(p.Entries) < PageSize):
			return Page{}, fmt.Errorf("%w: a page that does not advance", ErrUnavailable)
		}
		if since == 0 {
			all = p
		} else {
			all.Entries = append(all.Entries, p.Entries...)
		}
		if p.Next == 0 {
			all.Next = 0
			return all, nil
		}
		since = p.Next
	}
}

// do sends a request and decodes the answer into res, returning its status.
func (c Client) do(ctx context.Context, method, path, token string, body, res any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	resp.Body = http.MaxBytesReader(nil, resp.Body, maxBody)
	if err := status(resp); err != nil {
		return resp.StatusCode, err
	}
	if res == nil {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(res); err != nil {
		return resp.StatusCode, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return resp.StatusCode, nil
}

// refusals are the reasons the worker gives for refusing a request
// (docs/09). Only a refusal with one of them is the worker's own.
var refusals = map[string]bool{"params": true, "bad-assertion": true, "too-large": true, "blob-too-large": true,
	"fleet-full": true, "unauthorized": true, "no-fleet": true, "not-found": true, "exists": true}

// status maps a response to the client's errors. A refusal without one of
// the worker's own reasons came from something between (a captive portal, a
// proxy, the edge), and so does not count as the worker's: the worker is
// unavailable, as it is when it asks to wait or fails.
func status(resp *http.Response) error {
	s := resp.StatusCode
	if s < 300 {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e) // a body that is not the worker's has no reason
	switch {
	case s == http.StatusTooManyRequests || s >= 500 || !refusals[e.Error]:
		return fmt.Errorf("%w: %s", ErrUnavailable, resp.Status)
	case s == http.StatusUnauthorized:
		return ErrUnauthorized
	case s == http.StatusNotFound:
		return ErrNoFleet
	case s == http.StatusConflict:
		return ErrExists
	}
	return &Refused{Status: s, Reason: e.Error}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
