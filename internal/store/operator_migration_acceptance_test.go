package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOperatorAcceptanceMigratesExistingDatabaseIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	createOperatorLegacyDatabase(t, path)
	for i := 0; i < 2; i++ {
		checkOperatorMigration(t, path)
	}
}

func createOperatorLegacyDatabase(t *testing.T, path string) {
	t.Helper()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE message_metadata(message_id TEXT PRIMARY KEY,seq INTEGER NOT NULL UNIQUE,sender_session TEXT NOT NULL,in_reply_to TEXT NOT NULL,ack_requested INTEGER NOT NULL);
 INSERT INTO message_metadata VALUES('legacy',9,'old-session','',1);
 CREATE TABLE durable_agents(agent_id TEXT PRIMARY KEY);
 INSERT INTO durable_agents VALUES('legacy-agent');`)
	db.Close()
	if e != nil {
		t.Fatal(e)
	}

}

func checkOperatorMigration(t *testing.T, path string) {
	t.Helper()
	st, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	requireOperatorLegacyUnknown(t, st)
	for _, table := range []string{"operator_audit", "operator_requests"} {
		var n int
		if e := st.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
			t.Fatal(e)
		}
	}
}

func requireOperatorLegacyUnknown(t *testing.T, st *Store) {
	t.Helper()
	var origin sql.NullString
	var version sql.NullInt64
	if e := st.db.QueryRow("SELECT origin FROM message_metadata WHERE message_id='legacy'").Scan(&origin); e != nil {
		t.Fatal(e)
	}
	if e := st.db.QueryRow("SELECT protocol_version FROM durable_agents WHERE agent_id='legacy-agent'").Scan(&version); e != nil {
		t.Fatal(e)
	}
	if origin.Valid || version.Valid {
		t.Fatal("migration invented provenance or capability for legacy rows")
	}
}
