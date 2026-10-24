package migrations_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// These tests pin the guarantees the schema itself enforces, independent of the service layer.

func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO control.accounts (id, email, password_hash) VALUES
		('00000000-0000-7000-8000-000000000001', 'Owner@Example.com', 'x')`)
	mustExec(t, pool, `INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
		VALUES (482915736204, 'chat-app', 'Chat App', '00000000-0000-7000-8000-000000000001', '1_a', 'aruma.test')`)
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

func mustFail(t *testing.T, pool *pgxpool.Pool, why, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err == nil {
		t.Errorf("expected failure: %s\n%s", why, sql)
	}
}

func TestSchemaConstraints(t *testing.T) {
	pool := storetest.NewPool(t)
	seed(t, pool)

	mustFail(t, pool, "email is unique case-insensitively",
		`INSERT INTO control.accounts (email, password_hash) VALUES ('owner@example.COM', 'x')`)
	mustFail(t, pool, "locale is ar or en",
		`INSERT INTO control.accounts (email, password_hash, locale) VALUES ('b@example.com', 'x', 'fr')`)

	for _, id := range []string{"short", "Upper-case", "1starts-digit", "ends-with-dash-", "has_underscore", "this-project-id-is-far-too-long-ok"} {
		mustFail(t, pool, "invalid project_id "+id,
			`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
			 VALUES (582915736204, $1, 'x', '00000000-0000-7000-8000-000000000001', '1_a', 'd')`, id)
	}
	mustFail(t, pool, "project_number has 12 digits",
		`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
		 VALUES (12345, 'other-app', 'x', '00000000-0000-7000-8000-000000000001', '1_a', 'd')`)
	mustFail(t, pool, "project_id is never reused",
		`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
		 VALUES (582915736204, 'chat-app', 'x', '00000000-0000-7000-8000-000000000001', '1_a', 'd')`)

	mustFail(t, pool, "AIza prefix is reserved for Google",
		`INSERT INTO control.api_keys (project_number, key_string, etag) VALUES (482915736204, 'AIzaQ7mX2kLp9Vt4nRb8sWc1yZd6fHg3jKe5aN0', '1_a')`)
	mustFail(t, pool, "key is 39 characters",
		`INSERT INTO control.api_keys (project_number, key_string, etag) VALUES (482915736204, 'AruShort', '1_a')`)
	mustExec(t, pool,
		`INSERT INTO control.api_keys (uid, project_number, key_string, etag) VALUES
		 ('00000000-0000-7000-8000-0000000000a1', 482915736204, 'AruQ7mX2kLp9Vt4nRb8sWc1yZd6fHg3jKe5aN0o', '1_a')`)

	mustExec(t, pool, `INSERT INTO control.apps (app_id, project_number, platform, package_name, etag)
		VALUES ('1:482915736204:android:3f9a1c7e2b4d8a60', 482915736204, 'ANDROID', 'com.ahmad.chat', '1_a')`)
	mustFail(t, pool, "package is unique per project",
		`INSERT INTO control.apps (app_id, project_number, platform, package_name, etag)
		 VALUES ('1:482915736204:android:0000000000000001', 482915736204, 'ANDROID', 'com.ahmad.chat', '1_a')`)
	mustFail(t, pool, "app_id format",
		`INSERT INTO control.apps (app_id, project_number, platform, etag)
		 VALUES ('482915736204:web:abc', 482915736204, 'WEB', '1_a')`)
	mustFail(t, pool, "web apps carry no package",
		`INSERT INTO control.apps (app_id, project_number, platform, package_name, etag)
		 VALUES ('1:482915736204:web:0000000000000002', 482915736204, 'WEB', 'com.x', '1_a')`)
	mustFail(t, pool, "iOS apps need a bundle id",
		`INSERT INTO control.apps (app_id, project_number, platform, etag)
		 VALUES ('1:482915736204:ios:0000000000000003', 482915736204, 'IOS', '1_a')`)

	// AC-13: package and bundle ids are immutable.
	mustFail(t, pool, "package_name is immutable",
		`UPDATE control.apps SET package_name = 'com.other' WHERE app_id = '1:482915736204:android:3f9a1c7e2b4d8a60'`)
	mustExec(t, pool,
		`UPDATE control.apps SET display_name = 'Chat' WHERE app_id = '1:482915736204:android:3f9a1c7e2b4d8a60'`)

	mustFail(t, pool, "SHA-256 needs 64 hex",
		`INSERT INTO control.app_sha_certificates (app_id, sha_hash, cert_type)
		 VALUES ('1:482915736204:android:3f9a1c7e2b4d8a60', repeat('a', 40), 'SHA_256')`)

	mustExec(t, pool, `INSERT INTO control.audit_log (actor_type, action, result) VALUES ('system', 'test', 'ok')`)
	mustFail(t, pool, "audit_log rejects UPDATE", `UPDATE control.audit_log SET action = 'x'`)
	mustFail(t, pool, "audit_log rejects DELETE", `DELETE FROM control.audit_log`)
	mustFail(t, pool, "audit_log rejects TRUNCATE", `TRUNCATE control.audit_log`)
}

func TestUniqueViolationIsDetected(t *testing.T) {
	pool := storetest.NewPool(t)
	seed(t, pool)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO control.accounts (email, password_hash) VALUES ('OWNER@example.com', 'x')`)
	if c, ok := store.UniqueViolation(err); !ok || c != "accounts_email_lower_key" {
		t.Fatalf("UniqueViolation = %q, %v (err %v)", c, ok, err)
	}
}

// ADR-022 §3, ADR-017: the storage schema's own guarantees.
func TestStorageSchemaConstraints(t *testing.T) {
	pool := storetest.NewPool(t)
	seed(t, pool)
	const key = "p/482915736204/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f"
	insert := `INSERT INTO storage.objects (project_number, name, generation, blob_key, size, md5, crc32c, time_created, updated)
		VALUES ($1, $2, 1784647629568292, $3, 0, decode('d41d8cd98f00b204e9800998ecf8427e', 'hex'), 0, now(), now())`
	mustFail(t, pool, "an object belongs to a project", insert, 582915736204, "a.txt", key)
	mustFail(t, pool, "a file key is never an object name", insert, 482915736204, "a.txt", "p/482915736204/../../etc/passwd")
	mustFail(t, pool, "an object's file is under p/", insert, 482915736204, "a.txt", "s/482915736204/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f")
	mustFail(t, pool, "a name has a byte", insert, 482915736204, "", key)
	mustFail(t, pool, "a name has at most 1024 bytes", insert, 482915736204, strings.Repeat("é", 513), key)
	mustExec(t, pool, insert, 482915736204, "a.txt", key)
	mustFail(t, pool, "a file backs one object", insert, 482915736204, "b.txt", key)

	session := `INSERT INTO storage.upload_sessions (id_hash, project_number, name, surface, principal_kind, staging_key, state, final_status, expires_at)
		VALUES ($1, 482915736204, 'a.txt', 'firebase', 'anonymous', $2, $3, $4, now() + interval '7 days')`
	mustFail(t, pool, "only a final session has an outcome", session, bytes.Repeat([]byte{1}, 32),
		"s/482915736204/1b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f", "active", "permission_denied")
	mustFail(t, pool, "an upload id hash is 32 bytes", session, []byte{1},
		"s/482915736204/1b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f", "active", nil)
	mustExec(t, pool, session, bytes.Repeat([]byte{1}, 32), "s/482915736204/1b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f", "final", "permission_denied")

	mustFail(t, pool, "anonymous bytes are part of the reserved bytes",
		`INSERT INTO storage.usage (project_number, reserved_bytes, anonymous_reserved_bytes) VALUES (482915736204, 1, 2)`)
	mustExec(t, pool, `INSERT INTO storage.instance (id) VALUES ('00000000-0000-4000-8000-000000000001')`)
	mustFail(t, pool, "one instance row", `INSERT INTO storage.instance (id) VALUES ('00000000-0000-4000-8000-000000000002')`)
}
