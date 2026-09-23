package store

import (
	"encoding/json"
	"fmt"

	"github.com/notaharness/beam/internal/stream"
)

// Queue lists, for msg.queue (docs/06): outbound mail not yet acked, inbound
// mail no application has taken, inbound mail an application refused
// (deferred, with its reason), and outbound mail the recipient refused for
// good.
const (
	Outbound       = "outbound"
	Inbound        = "inbound"
	Refused        = "refused"
	QuarantineList = "quarantine"
)

var queueQueries = map[string]string{
	Outbound:       `SELECT rowid, envelope, '' FROM outbound WHERE (? = '' OR peer = ?)`,
	Inbound:        `SELECT rowid, envelope, '' FROM inbound WHERE deferred IS NULL AND (? = '' OR peer = ?)`,
	Refused:        `SELECT rowid, envelope, deferred FROM inbound WHERE deferred IS NOT NULL AND (? = '' OR peer = ?)`,
	QuarantineList: `SELECT rowid, envelope, reason FROM quarantine WHERE (? = '' OR peer = ?)`,
}

// MaxPage bounds a page's items, serialized, so that a msg.queue reply fits
// a control line with room for the reply around them.
const MaxPage = 1<<20 - 4<<10

// QueueItem is one listed envelope.
type QueueItem struct {
	Envelope json.RawMessage `json:"envelope"`
	Reason   string          `json:"reason,omitempty"`
}

// Queue returns up to limit envelopes of list which for peer ("" for every
// peer) after cursor, and the cursor that continues it (0 at the end). A
// page holds at least one item and stops before one that would take it past
// MaxPage.
func (s *Store) Queue(which, peer string, cursor int64, limit int) ([]QueueItem, int64, error) {
	q, ok := queueQueries[which]
	if !ok {
		return nil, 0, fmt.Errorf("no queue %q", which)
	}
	rows, err := s.db.Query(q+` AND rowid > ? ORDER BY rowid LIMIT ?`, peer, peer, cursor, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []QueueItem
	var last int64
	size := 1 // [ ]
	for rows.Next() {
		var it QueueItem
		var id int64
		var env string
		if err := rows.Scan(&id, &env, &it.Reason); err != nil {
			return nil, 0, err
		}
		it.Envelope = json.RawMessage(env)
		b, _ := stream.Marshal(it) // as msg.queue sends it
		if len(items) == limit || len(items) > 0 && size+len(b)+1 > MaxPage {
			return items, last, rows.Err()
		}
		items, last, size = append(items, it), id, size+len(b)+1
	}
	return items, 0, rows.Err()
}

// QueueCounts are a peer's mail counts: outbound not yet acked, inbound not
// yet taken, and inbound an application refused.
func (s *Store) QueueCounts(peer string) (outbound, inbound, refused int, err error) {
	err = s.db.QueryRow(`SELECT
		(SELECT count(*) FROM outbound WHERE peer = ?),
		(SELECT count(*) FROM inbound WHERE peer = ? AND deferred IS NULL),
		(SELECT count(*) FROM inbound WHERE peer = ? AND deferred IS NOT NULL)`, peer, peer, peer).Scan(&outbound, &inbound, &refused)
	return
}
