package announcementexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"hash"
	"path/filepath"
	"time"

	"github.com/agent-primitives/canvas/internal/announcement"
)

// Evidence describes this store's durable facts, not native approval, live
// counter invariance, or continuity of any remote Sessions journal.
type Evidence struct {
	Version     int    `json:"version"`
	StoreID     string `json:"storeID"`
	Invocations int64  `json:"invocations"`
	Outcomes    int64  `json:"outcomes"`
	Digest      string `json:"digest"`
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validSnapshot(raw []byte, operation string) bool {
	s, err := announcement.Decode(raw)
	if err != nil || s.Operation != operation {
		return false
	}
	encoded, err := announcement.Encode(s)
	return err == nil && bytes.Equal(raw, encoded)
}
func hashRow(h hash.Hash, fields ...any) error { return json.NewEncoder(h).Encode(fields) }

func validateRecords(ctx context.Context, q queryer, id string) (Evidence, error) {
	ev := Evidence{Version: 1, StoreID: id}
	h := sha256.New()
	_ = hashRow(h, ev.Version, id)
	// Bound blobs before Scan allocates them; hostile/corrupt storage is not input
	// to the JSON decoder until it has passed the same envelope size bound.
	for _, spec := range []struct{ table, column string }{{"bindings", "snapshot"}, {"invocations", "snapshot"}, {"outcomes", "receipt"}} {
		var bad int
		if q.QueryRowContext(ctx, "SELECT count(*) FROM "+spec.table+" WHERE length("+spec.column+") > 409600").Scan(&bad) != nil || bad != 0 {
			return Evidence{}, ErrUnavailable
		}
	}
	fk, err := q.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	bad := fk.Next()
	err = fk.Err()
	fk.Close()
	if bad || err != nil {
		return Evidence{}, ErrUnavailable
	}
	rows, err := q.QueryContext(ctx, `SELECT session_uid,operation,snapshot FROM bindings ORDER BY session_uid,operation`)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	for rows.Next() {
		var uid, op string
		var raw []byte
		if rows.Scan(&uid, &op, &raw) != nil || uid == "" || !validSnapshot(raw, op) {
			rows.Close()
			return Evidence{}, ErrUnavailable
		}
		_ = hashRow(h, "binding", uid, op, raw)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	var unadmitted int
	if q.QueryRowContext(ctx, `SELECT count(*) FROM bindings b WHERE NOT EXISTS(SELECT 1 FROM invocations i WHERE i.session_uid=b.session_uid AND i.operation=b.operation)`).Scan(&unadmitted) != nil || unadmitted != 0 {
		return Evidence{}, ErrUnavailable
	}
	rows, err = q.QueryContext(ctx, `SELECT id,session_uid,operation,snapshot FROM invocations ORDER BY id`)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	for rows.Next() {
		var n int64
		var uid, op string
		var raw []byte
		if rows.Scan(&n, &uid, &op, &raw) != nil || n < 1 || uid == "" || !validSnapshot(raw, op) {
			rows.Close()
			return Evidence{}, ErrUnavailable
		}
		_ = hashRow(h, "invocation", n, uid, op, raw)
		ev.Invocations++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	rows, err = q.QueryContext(ctx, `SELECT o.session_uid,o.operation,o.receipt,b.snapshot FROM outcomes o JOIN bindings b USING(session_uid,operation) ORDER BY o.session_uid,o.operation`)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	for rows.Next() {
		var uid, op string
		var raw, bound []byte
		var output map[string]any
		if rows.Scan(&uid, &op, &raw, &bound) != nil || json.Unmarshal(raw, &output) != nil {
			rows.Close()
			return Evidence{}, ErrUnavailable
		}
		receipt, e := announcement.DecodeReceipt(output)
		if e != nil {
			rows.Close()
			return Evidence{}, ErrUnavailable
		}
		snapshot, e := announcement.Encode(receipt.Snapshot)
		canonical, _ := json.Marshal(receipt)
		if e != nil || receipt.Snapshot.Operation != op || !bytes.Equal(snapshot, bound) || !bytes.Equal(raw, canonical) {
			rows.Close()
			return Evidence{}, ErrUnavailable
		}
		_ = hashRow(h, "outcome", uid, op, raw)
		ev.Outcomes++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	ev.Digest = hex.EncodeToString(h.Sum(nil))
	return ev, nil
}

// ReadEvidence never creates, initializes, recovers, seals or repairs the source.
// It takes no writer lifecycle flock. A busy/hot/uncertain store is unavailable,
// not an invented zero, and the bounded read transaction cannot poison a writer.
func ReadEvidence(ctx context.Context, root string) (Evidence, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	exists, err := inspect(root)
	if err != nil || !exists {
		return Evidence{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	db, err := database(filepath.Join(root, "executor.db"), true)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	defer tx.Rollback()
	id, err := validateSchema(ctx, tx)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	ev, err := validateRecords(ctx, tx, id)
	if err != nil {
		return Evidence{}, ErrUnavailable
	}
	if err = tx.Commit(); err != nil {
		return Evidence{}, ErrUnavailable
	}
	return ev, nil
}
