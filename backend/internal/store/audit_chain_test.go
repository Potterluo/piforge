package store

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/audit"
)

// The chained hash is only as complete as its field list. If a column is added
// to audit_log without adding it to audit.ChainFields, an edit to that column
// would verify as intact — a failure this test is here to prevent.
func TestAuditChainCoversEveryStoredColumn(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.r.QueryContext(context.Background(), `PRAGMA table_info(audit_log)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	// hashed is the set of columns audit.ChainFields promises to hash. `v` is
	// the encoding version and `has_*` are presence flags for nullable
	// observations; neither is a column.
	hashed := map[string]bool{}
	typ := reflect.TypeOf(audit.ChainFields{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "v" || strings.HasPrefix(tag, "has_") {
			continue
		}
		hashed[tag] = true
	}

	stored := 0
	for rows.Next() {
		var (
			cid       int
			name, ddl string
			notNull   int
			dflt      any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ddl, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		// prev_hash and hash are the chain itself; hashing them into the row
		// would be circular.
		if name == "prev_hash" || name == "hash" {
			continue
		}
		stored++
		if !hashed[name] {
			t.Fatalf("audit_log column %q is not covered by audit.ChainFields; add it (and bump the chain version) or the chain cannot detect an edit to it", name)
		}
		delete(hashed, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info: %v", err)
	}
	for tag := range hashed {
		t.Fatalf("audit.ChainFields has %q but audit_log has no such column; the hash would cover a value the row does not store", tag)
	}
	if stored < 20 {
		t.Fatalf("read only %d columns; the check did not see the table", stored)
	}
}
