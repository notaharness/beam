package directory_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/directory"
)

// A worker is untrusted for availability, not for a reader's memory: Read
// stops at a page larger than the worker's, a directory larger than its cap,
// a cursor that does not advance over a full page, and a body larger than a
// full page can be.
func TestReadBounded(t *testing.T) {
	entries := func(n int, from int64) []directory.Entry {
		es := make([]directory.Entry, n)
		for i := range es {
			es[i].Seq = from + int64(i)
		}
		return es
	}
	for _, tc := range []struct {
		name     string
		page     func(since int64) any
		requests int64
	}{
		{"a page over 500", func(int64) any {
			return directory.Page{Entries: entries(directory.PageSize+1, 1)}
		}, 1},
		{"over 5,000 in all", func(since int64) any {
			return directory.Page{Entries: entries(directory.PageSize, since+1), Next: since + directory.PageSize}
		}, directory.MaxEntries/directory.PageSize + 1},
		{"a cursor that stays", func(int64) any {
			return directory.Page{Entries: entries(directory.PageSize, 1), Next: 1}
		}, 2},
		{"a cursor over a page not full", func(since int64) any {
			return directory.Page{Next: since + 1}
		}, 1},
		{"a body over a page's", func(int64) any {
			return directory.Page{CredentialPublicKey: strings.Repeat("A", directory.PageSize<<14)}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
				json.NewEncoder(w).Encode(tc.page(since))
			}))
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if p, err := (directory.Client{URL: s.URL}).Read(ctx, []byte("token")); err == nil || ctx.Err() != nil {
				t.Errorf("read %d entries in %d requests, error %v", len(p.Entries), requests.Load(), err)
			}
			if n := requests.Load(); n != tc.requests {
				t.Errorf("%d requests, want %d", n, tc.requests)
			}
		})
	}
}
