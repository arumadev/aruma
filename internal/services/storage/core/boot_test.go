package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Boot (ADR-022 §5.1): the lock, the binding of the directory to the database, the candidates
// and the quota reconciled, all before the listener opens.

// A second instance on the same directory waits for the lock, saying so, until the first closes.
func TestBootWaitsForTheLock(t *testing.T) {
	e := newEnv(t)
	second := e.service()
	done := make(chan error, 1)
	go func() { done <- second.Boot(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("the second instance booted while the first holds the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if !strings.Contains(e.logs.String(), "waiting for the data directory's lock") {
		t.Errorf("the wait is not logged: %s", e.logs.String())
	}
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the second instance after the first closed: %v", err)
	}
	if second.instance != e.s.instance || second.instance == "" {
		t.Errorf("the instances are %q and %q", second.instance, e.s.instance)
	}
	if err := second.Boot(context.Background()); err == nil {
		t.Error("Boot twice")
	}
	if err := second.Close(); err != nil || second.Close() != nil {
		t.Error("Close is not idempotent")
	}
	// Giving up the wait, as a shutdown during it does.
	third := e.service()
	held := e.service()
	if err := held.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := third.Boot(ctx); err == nil {
		t.Error("a boot whose context ends still runs")
	}
}

// The directory and the database are bound; a mismatch refuses to boot, and a half written on a
// first boot is adopted only while the directory holds no file.
func TestBootInstanceBinding(t *testing.T) {
	ctx := context.Background()
	other := uuid.NewString()
	cases := []struct {
		name     string
		file     bool   // an INSTANCE file
		row      bool   // a storage.instance row
		mismatch bool   // the two differ
		data     bool   // p/ or s/ holds a file
		refuse   string // a piece of the refusal, "" to boot
	}{
		{name: "a first boot"},
		{name: "the file without its row, on an empty directory", file: true},
		{name: "the row without its file, on an empty directory", row: true},
		{name: "both, the same", file: true, row: true, data: true},
		{name: "another database's directory", file: true, row: true, mismatch: true, refuse: "belongs to another database"},
		{name: "the file without its row, with data", file: true, data: true, refuse: "no storage.instance row"},
		{name: "the row without its file, with data", row: true, data: true, refuse: "holds files but no INSTANCE"},
		{name: "neither, with data", data: true, refuse: "nothing binds them"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.s.Close(); err != nil {
				t.Fatal(err)
			}
			// Start from nothing, then lay out the case.
			e.store.instance = nil
			if _, err := e.pool.Exec(ctx, `DELETE FROM storage.instance`); err != nil {
				t.Fatal(err)
			}
			id := uuid.NewString()
			if c.file {
				e.store.instance = &id
			}
			if c.row {
				rowID := id
				if c.mismatch {
					rowID = other
				}
				if _, err := e.pool.Exec(ctx, `INSERT INTO storage.instance (id) VALUES ($1)`, rowID); err != nil {
					t.Fatal(err)
				}
			}
			if c.data {
				w, _ := e.store.Create("p/482915736204/0b0d6d0e-6c3a-4b8e-9d5e-0e0f10111213")
				_ = w.Commit()
			}
			s := e.service()
			err := s.Boot(ctx)
			if c.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), c.refuse) {
					t.Fatalf("Boot = %v, want a refusal saying %q", err, c.refuse)
				}
				// The refusal gives the lock back.
				if s.lock != nil {
					t.Error("a refused boot holds the lock")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var row string
			if err := e.pool.QueryRow(ctx, `SELECT id::text FROM storage.instance`).Scan(&row); err != nil || e.store.instance == nil ||
				*e.store.instance != row || s.instance != row {
				t.Errorf("after the boot: file %v, row %q, %v", e.store.instance, row, err)
			}
			if c.file && row != id || c.row && row != id {
				t.Errorf("the half that existed was not adopted: %s, want %s", row, id)
			}
		})
	}
}

// A first boot that crashes between the INSTANCE file and the row adopts the file next time.
func TestBootCrashBetweenFileAndRow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.s.Close()
	e.store.instance = nil
	if _, err := e.pool.Exec(ctx, `DELETE FROM storage.instance`); err != nil {
		t.Fatal(err)
	}
	crashAt("boot.instance_file")
	if !crashed(func() { _ = e.service().Boot(ctx) }) {
		t.Fatal("no crash")
	}
	file := *e.store.instance
	s := e.restart()
	if s.instance != file || e.count(`SELECT count(*) FROM storage.instance WHERE id::text = $1`, file) != 1 {
		t.Errorf("the INSTANCE %s was not adopted: %s", file, s.instance)
	}
}

// Boot lists every file as a candidate before anything is written; Run deletes those no row
// names, and a key that a session moved to after the boot is never one of them.
func TestBootCandidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	kept := e.insert(e.a, "kept", "x")
	_, keptRow, _ := e.s.storedObject(ctx, e.a, "kept")
	id := e.start(e.a, asUser("u1"), uploadSpec("live", SurfaceFirebase))
	if _, err := e.chunk(e.a, "live", id, 0, "abc", false); err != nil {
		t.Fatal(err)
	}
	orphan := "p/482915736204/0b0d6d0e-6c3a-4b8e-9d5e-0e0f10111213"
	staleStaging := "s/582915736204/1b0d6d0e-6c3a-4b8e-9d5e-0e0f10111213"
	for _, k := range []string{orphan, staleStaging} {
		w, _ := e.store.Create(k)
		_ = w.Commit()
	}
	s := e.restart()
	if len(s.candidates) != 4 {
		t.Fatalf("candidates %q", s.candidates)
	}
	// After the boot and before Run: the session finalizes, its file moves to a new key.
	if _, err := e.chunk(e.a, "live", id, 3, "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	if e.store.has(orphan) || e.store.has(staleStaging) {
		t.Error("an orphan stays")
	}
	if !e.store.has(keptRow.BlobKey) || e.read(e.a, "live") != "abc" || kept.Size != 1 {
		t.Error("the sweep deleted a file a row names")
	}
	if v := metricValue(t, s.metrics.SweepDeleted); v != 2 {
		t.Errorf("%v orphans counted", v)
	}
}

// Boot reconciles the quota: what a crash left reserved goes, and used follows the objects.
func TestBootReconcilesUsage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.insert(e.a, "a", "abcd")
	id := e.start(e.a, Principal{Kind: PrincipalAnonymous}, uploadSpec("b", SurfaceFirebase))
	if _, err := e.chunk(e.a, "b", id, 0, "xy", false); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE storage.usage SET used_bytes = 999, reserved_bytes = 5000, anonymous_reserved_bytes = 100, object_count = 7`,
		`INSERT INTO storage.usage (project_number, reserved_bytes) VALUES (582915736204, 77)`,
	} {
		if _, err := e.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	s := e.restart()
	if used, reserved, anonymous, objects := e.usage(projectA); used != 4 || reserved != 2 || anonymous != 2 || objects != 1 {
		t.Errorf("after the boot: %d used, %d reserved, %d anonymous, %d objects", used, reserved, anonymous, objects)
	}
	if _, reserved, _, _ := e.usage(projectB); reserved != 0 {
		t.Errorf("project B keeps %d reserved", reserved)
	}
	if v := metricValue(t, s.metrics.QuotaReconciledBytes); v != 995+4998+77 {
		t.Errorf("%v bytes reconciled", v)
	}
}
