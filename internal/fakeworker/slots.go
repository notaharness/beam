package fakeworker

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// writeSlot takes a ceremony's sealed result, once.
func (w *Worker) writeSlot(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Sealed string `json:"sealed"`
	}
	derr := json.NewDecoder(r.Body).Decode(&req)
	sealed, err := unb64(req.Sealed)
	switch {
	case !validSlot(r.PathValue("slot")):
		fail(rw, http.StatusNotFound, "not-found")
		return
	case derr != nil || err != nil || len(sealed) == 0:
		fail(rw, http.StatusBadRequest, "params")
		return
	case len(sealed) > maxBlob:
		fail(rw, http.StatusRequestEntityTooLarge, "sealed-too-large")
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.slot(r.PathValue("slot"))
	select {
	case <-s.written:
		fail(rw, http.StatusConflict, "answered")
		return
	default:
	}
	s.sealed = req.Sealed
	close(s.written)
	reply(rw, http.StatusCreated, struct{}{})
}

// readSlot hands the slot's result to the key it hashes from, waiting up to
// 25 s for the write.
func (w *Worker) readSlot(rw http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	key, err := unb64(token)
	h := sha256.Sum256(key)
	if !validSlot(r.PathValue("slot")) || err != nil || len(key) != 32 || b64(h[:16]) != r.PathValue("slot") {
		fail(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	w.mu.Lock()
	s := w.slot(r.PathValue("slot"))
	w.mu.Unlock()
	select {
	case <-s.written:
	case <-time.After(25 * time.Second):
		rw.WriteHeader(http.StatusNoContent)
		return
	case <-r.Context().Done():
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.sealed == "" {
		fail(rw, http.StatusGone, "read")
		return
	}
	reply(rw, http.StatusOK, map[string]string{"sealed": s.sealed})
	s.sealed = ""
}

// slot is the slot id names, made on first use. The caller holds mu.
func (w *Worker) slot(id string) *slot {
	s := w.slots[id]
	if s == nil {
		s = &slot{written: make(chan struct{})}
		w.slots[id] = s
	}
	return s
}

func validSlot(id string) bool {
	b, err := unb64(id)
	return err == nil && len(b) == 16 && len(id) == 22
}
