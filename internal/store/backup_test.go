package store_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/store/storetest"
)

func pgTools(t *testing.T) store.PGTools {
	t.Helper()
	tools := store.PGTools{Dir: os.Getenv("ARUMA_PG_BIN")}
	if tools.Dir == "" {
		if _, err := exec.LookPath("pg_dump"); err != nil {
			if os.Getenv("ARUMA_TEST_REQUIRE_DB") != "" {
				t.Fatal("pg_dump not found; set ARUMA_PG_BIN")
			}
			t.Skip("pg_dump not found; set ARUMA_PG_BIN")
		}
	}
	return tools
}

// AC-31: a backup restored over a changed database brings everything back as it was.
func TestBackupRestore(t *testing.T) {
	tools := pgTools(t)
	ctx := context.Background()
	url := storetest.NewDB(t)
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO control.accounts (id, email, password_hash) VALUES ('00000000-0000-7000-8000-000000000001', 'a@example.com', 'h')`)
	exec(`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
	      VALUES (482915736204, 'chat-app', 'Chat', '00000000-0000-7000-8000-000000000001', '1_a', 'd')`)
	exec(`INSERT INTO control.audit_log (actor_type, action, result) VALUES ('system', 'before', 'ok')`)

	file := filepath.Join(t.TempDir(), "aruma.dump")
	if err := store.Backup(ctx, tools, url, file); err != nil {
		t.Fatal(err)
	}

	exec(`UPDATE control.projects SET display_name = 'Changed'`)
	exec(`INSERT INTO control.accounts (email, password_hash) VALUES ('b@example.com', 'h')`)
	exec(`INSERT INTO control.audit_log (actor_type, action, result) VALUES ('system', 'after', 'ok')`)
	pool.Close()

	if err := store.Restore(ctx, tools, url, file); err != nil {
		t.Fatal(err)
	}
	pool2, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	var name string
	var accounts, audit int
	pool2.QueryRow(ctx, `SELECT display_name FROM control.projects`).Scan(&name)
	pool2.QueryRow(ctx, `SELECT count(*) FROM control.accounts`).Scan(&accounts)
	pool2.QueryRow(ctx, `SELECT count(*) FROM control.audit_log`).Scan(&audit)
	if name != "Chat" || accounts != 1 || audit != 1 {
		t.Fatalf("after restore: name=%q accounts=%d audit=%d", name, accounts, audit)
	}
	if err := store.CheckCurrent(ctx, pool2); err != nil {
		t.Fatalf("schema after restore: %v", err)
	}
	// Triggers survive the round trip: the audit log is still append-only.
	if _, err := pool2.Exec(ctx, `DELETE FROM control.audit_log`); err == nil {
		t.Fatal("audit log lost its append-only guard")
	}
}

func TestRestoreRejectsMissingFile(t *testing.T) {
	if err := store.Restore(context.Background(), store.PGTools{}, "postgres://x/y", "/nonexistent.dump"); err == nil {
		t.Fatal("expected error")
	}
}
