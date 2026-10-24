package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"hash/crc32"
	"testing"
	"time"
)

// The GC, the periodic sweep, Run, and the hashes' saved state.

// A replaced file waits gc_grace in blob_gc, covering a reader between its row and its file;
// then it goes, and its row with it. A delete that fails keeps the row for a later round.
func TestCollectGarbage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.insert(e.a, "a", "1")
	_, old, _ := e.s.storedObject(ctx, e.a, "a")
	e.insert(e.a, "a", "2")
	e.insert(e.a, "b", "1")
	_, oldB, _ := e.s.storedObject(ctx, e.a, "b")
	e.insert(e.a, "b", "2")
	if n, err := e.s.collectGarbage(ctx); err != nil || n != 0 || !e.store.has(old.BlobKey) {
		t.Fatalf("within gc_grace: %d, %v", n, err)
	}
	e.store.deleteErr = map[string]error{oldB.BlobKey: errors.New("the file is open (Windows)")}
	e.clock.add(time.Minute + time.Second)
	if n, err := e.s.collectGarbage(ctx); err != nil || n != 1 || e.store.has(old.BlobKey) {
		t.Fatalf("after gc_grace: %d, %v", n, err)
	}
	if e.count(`SELECT count(*) FROM storage.blob_gc`) != 1 || !e.store.has(oldB.BlobKey) {
		t.Error("the failed delete lost its row")
	}
	e.store.deleteErr = nil
	if n, err := e.s.collectGarbage(ctx); err != nil || n != 1 || e.count(`SELECT count(*) FROM storage.blob_gc`) != 0 {
		t.Errorf("the retry: %d, %v", n, err)
	}
	// A key a row points to is never collected, whatever queued it.
	_, cur, _ := e.s.storedObject(ctx, e.a, "a")
	if _, err := e.pool.Exec(ctx, `INSERT INTO storage.blob_gc (blob_key, project_number, queued_at) VALUES ($1, $2, now() - interval '1 hour')`,
		cur.BlobKey, projectA); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.collectGarbage(ctx); err != nil || n != 0 || e.read(e.a, "a") != "2" {
		t.Errorf("a live key in blob_gc: %d, %v", n, err)
	}
	if err := e.s.reportGauges(ctx); err != nil || metricValue(t, e.s.metrics.GCQueue) != 1 {
		t.Errorf("the gauges: %v", err)
	}
}

// The periodic sweep reports and deletes nothing.
func TestReportInconsistencies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.insert(e.a, "a", "1")
	e.insert(e.a, "gone", "1")
	_, gone, _ := e.s.storedObject(ctx, e.a, "gone")
	if err := e.store.Delete(gone.BlobKey); err != nil {
		t.Fatal(err)
	}
	stray := "p/482915736204/0b0d6d0e-6c3a-4b8e-9d5e-0e0f10111213"
	w, _ := e.store.Create(stray)
	_ = w.Commit()
	e.store.files[stray].mod = time.Now().Add(-2 * time.Hour)
	fresh := "p/482915736204/1b0d6d0e-6c3a-4b8e-9d5e-0e0f10111213"
	w, _ = e.store.Create(fresh) // a write in flight
	_ = w.Commit()
	if err := e.s.reportInconsistencies(ctx); err != nil {
		t.Fatal(err)
	}
	if v := metricValue(t, e.s.metrics.SweepReported, "file_without_row"); v != 1 {
		t.Errorf("%v files without a row reported", v)
	}
	if v := metricValue(t, e.s.metrics.SweepReported, "row_without_file"); v != 1 {
		t.Errorf("%v rows without a file reported", v)
	}
	if !e.store.has(stray) || !e.store.has(fresh) {
		t.Error("the periodic sweep deleted a file")
	}
}

// Run sweeps the boot's orphans, works its rounds, and returns when its context ends.
func TestRun(t *testing.T) {
	e := newEnv(t)
	e.insert(e.a, "a", "1")
	orphan := "s/482915736204/0b0d6d0e-6c3a-4b8e-9d5e-0e0f10111213"
	w, _ := e.store.Create(orphan)
	_ = w.Commit()
	s := e.restart()
	id := e.start(e.a, asUser("u1"), uploadSpec("b", SurfaceFirebase))
	e.clock.add(11 * time.Minute) // the empty session expires
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for (e.store.has(orphan) || e.count(`SELECT count(*) FROM storage.upload_sessions`) != 0) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
	if e.store.has(orphan) {
		t.Error("Run left the boot's orphan")
	}
	if _, err := e.s.QueryUpload(context.Background(), e.a, "b", id); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("the expired session: %v", err)
	}
}

func TestHashState(t *testing.T) {
	data := bytes.Repeat([]byte("the quick brown fox "), 1000)
	for _, cut := range []int{0, 1, 63, 64, 65, 1000, len(data)} {
		h := newHasher()
		h.Write(data[:cut])
		state, err := h.marshal()
		if err != nil {
			t.Fatal(err)
		}
		r, err := restoreHasher(state)
		if err != nil {
			t.Fatal(err)
		}
		r.Write(data[cut:])
		crc, sum := r.sums()
		if crc != crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)) || sum != md5.Sum(data) {
			t.Errorf("restored at %d: crc %x md5 %x", cut, crc, sum)
		}
	}
	if h := newHasher(); func() bool { h.Write([]byte("123456789")); c, _ := h.sums(); return c != 0xE3069283 }() {
		t.Error("not Castagnoli")
	}
	if _, err := restoreHasher([]byte{9, 0, 1}); err == nil {
		t.Error("an unknown version restores")
	}
	if _, err := restoreHasher([]byte{1, 0, 200, 1}); err == nil {
		t.Error("a truncated state restores")
	}
	if h, err := restoreHasher(nil); err != nil || h == nil {
		t.Error("no state is a fresh hasher")
	}
}

func TestChecksumClaims(t *testing.T) {
	a, b := uint32(1), uint32(2)
	m1, m2 := md5.Sum([]byte("1")), md5.Sum([]byte("2"))
	if c, err := mergeChecksums(Checksums{CRC32C: &a}, Checksums{MD5: &m1}); err != nil || c.CRC32C != &a || c.MD5 != &m1 {
		t.Errorf("merge: %+v, %v", c, err)
	}
	if _, err := mergeChecksums(Checksums{CRC32C: &a}, Checksums{CRC32C: &b}); err != ErrChecksumMismatch {
		t.Error("contradicting crc32c claims merge")
	}
	if _, err := mergeChecksums(Checksums{MD5: &m1}, Checksums{MD5: &m2}); err != ErrChecksumMismatch {
		t.Error("contradicting md5 claims merge")
	}
	if checkChecksums(Checksums{}, 1, m1) != nil || checkChecksums(Checksums{CRC32C: &a}, 1, m1) != nil ||
		checkChecksums(Checksums{MD5: &m2}, 1, m1) != ErrChecksumMismatch {
		t.Error("checkChecksums")
	}
}
