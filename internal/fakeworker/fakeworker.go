// Package fakeworker is the directory worker's API in memory, for tests
// (docs/10): the routes, the bearer check and assertion verification as the
// worker does them, with switches to take it down or have it withhold a
// record. The contract test holds it to the real worker.
package fakeworker

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/notaharness/beam/internal/directory"
	"github.com/notaharness/beam/internal/identity"
)

// Caps (docs/09).
const (
	maxBlob    = 8 << 10
	maxEntries = 5000
	pageSize   = 500
)

// Worker is an in-memory directory worker. The zero value is not usable; use
// New.
type Worker struct {
	mux *http.ServeMux

	mu       sync.Mutex
	fleets   map[string]*fleet // by fleetId
	byRead   map[string]*fleet // by SHA-256(read token), base64url
	down     bool
	withheld map[string]bool // statementHash → left out of reads
}

type fleet struct {
	id      string
	cred    identity.Credential
	entries []directory.Entry
	hashes  map[string]int64 // statementHash → seq
}

// New returns an empty worker.
func New() *Worker {
	w := &Worker{mux: http.NewServeMux(), fleets: map[string]*fleet{}, byRead: map[string]*fleet{}, withheld: map[string]bool{}}
	w.mux.HandleFunc("POST /v1/fleets", w.register)
	w.mux.HandleFunc("GET /v1/entries", w.read)
	w.mux.HandleFunc("POST /v1/fleets/{id}/entries", w.appendEntry)
	return w
}

// SetDown makes every request fail with 503, or no longer.
func (w *Worker) SetDown(down bool) {
	w.mu.Lock()
	w.down = down
	w.mu.Unlock()
}

// Withhold leaves the entry for statementHash out of reads.
func (w *Worker) Withhold(statementHash [32]byte) {
	w.mu.Lock()
	w.withheld[b64(statementHash[:])] = true
	w.mu.Unlock()
}

// Len is how many entries fleetID's directory holds.
func (w *Worker) Len(fleetID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f := w.fleets[fleetID]; f != nil {
		return len(f.entries)
	}
	return 0
}

func (w *Worker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	down := w.down
	w.mu.Unlock()
	if down {
		fail(rw, http.StatusServiceUnavailable, "down")
		return
	}
	w.mux.ServeHTTP(rw, r)
}

func (w *Worker) register(rw http.ResponseWriter, r *http.Request) {
	var req directory.Registration
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		fail(rw, http.StatusBadRequest, "params")
		return
	}
	pk, err1 := unb64(req.CredentialPublicKey)
	token, err2 := unb64(req.ReadToken)
	if err1 != nil || err2 != nil || len(token) != 32 || req.CredentialID == "" {
		fail(rw, http.StatusBadRequest, "params")
		return
	}
	cred := identity.Credential{ID: req.CredentialID, PublicKey: pk}
	req.First.Kind = identity.Member
	if code, reason := check(cred, req.First); code != 0 {
		fail(rw, code, reason)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fleets[cred.FleetID()] != nil {
		fail(rw, http.StatusConflict, "exists")
		return
	}
	f := &fleet{id: cred.FleetID(), cred: cred, hashes: map[string]int64{}}
	w.fleets[f.id], w.byRead[hashB64(token)] = f, f
	f.add(req.First)
	reply(rw, http.StatusCreated, map[string]string{"fleetId": f.id})
}

func (w *Worker) read(rw http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	raw, err := unb64(token)
	since, serr := strconv.ParseInt("0"+r.URL.Query().Get("since"), 10, 64)
	w.mu.Lock()
	defer w.mu.Unlock()
	f := w.byRead[hashB64(raw)]
	switch {
	case !ok || err != nil || f == nil:
		fail(rw, http.StatusUnauthorized, "unauthorized") // the same for every token that opens nothing
		return
	case serr != nil:
		fail(rw, http.StatusBadRequest, "params")
		return
	}
	p := directory.Page{FleetID: f.id, CredentialID: f.cred.ID, CredentialPublicKey: b64(f.cred.PublicKey), Entries: []directory.Entry{}}
	for _, e := range f.entries[min(since, int64(len(f.entries))):] {
		if len(p.Entries) == pageSize {
			p.Next = p.Entries[pageSize-1].Seq
			break
		}
		if !w.withheld[e.StatementHash] {
			p.Entries = append(p.Entries, e)
		}
	}
	reply(rw, http.StatusOK, p)
}

func (w *Worker) appendEntry(rw http.ResponseWriter, r *http.Request) {
	var e directory.Entry
	if json.NewDecoder(r.Body).Decode(&e) != nil || (e.Kind != identity.Member && e.Kind != identity.Revoke) {
		fail(rw, http.StatusBadRequest, "params")
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f := w.fleets[r.PathValue("id")]
	if f == nil {
		fail(rw, http.StatusNotFound, "no-fleet")
		return
	}
	if code, reason := check(f.cred, e); code != 0 {
		fail(rw, code, reason)
		return
	}
	if seq, ok := f.hashes[e.StatementHash]; ok {
		reply(rw, http.StatusOK, map[string]int64{"seq": seq})
		return
	}
	if len(f.entries) == maxEntries {
		fail(rw, http.StatusRequestEntityTooLarge, "fleet-full")
		return
	}
	reply(rw, http.StatusCreated, map[string]int64{"seq": f.add(e)})
}

// check verifies an entry's shape and its assertion under cred for its kind.
func check(cred identity.Credential, e directory.Entry) (int, string) {
	h, err := unb64(e.StatementHash)
	blob, berr := unb64(e.Blob)
	switch {
	case err != nil || len(h) != 32 || berr != nil || e.Assertion == nil:
		return http.StatusBadRequest, "params"
	case len(blob) > maxBlob:
		return http.StatusRequestEntityTooLarge, "blob-too-large"
	case e.Assertion.CredentialID != cred.ID || cred.VerifyAssertion(e.Assertion, identity.Challenge(e.Kind, [32]byte(h))) != nil:
		return http.StatusForbidden, "bad-assertion"
	}
	return 0, ""
}

func (f *fleet) add(e directory.Entry) int64 {
	e.Kind = ""
	e.Seq = int64(len(f.entries)) + 1
	f.entries = append(f.entries, e)
	f.hashes[e.StatementHash] = e.Seq
	return e.Seq
}

func reply(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v) // the client sees a short body
}

func fail(rw http.ResponseWriter, code int, reason string) {
	reply(rw, code, map[string]string{"error": reason})
}

func hashB64(b []byte) string {
	h := sha256.Sum256(b)
	return b64(h[:])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
