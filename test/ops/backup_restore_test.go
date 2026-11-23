package ops

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// backupSet is what the runbook (docs/guides/operations.md) says to keep: the database dump
// and the Storage directory's archive, and the master key.
type backupSet struct {
	dump, archive, key string
}

// takeBackup follows the runbook against a running deployment: one `aruma backup` writes the
// dump and the archive of the Storage files the dump names.
func takeBackup(t *testing.T, d *deployment, dir string) backupSet {
	t.Helper()
	b := backupSet{dump: filepath.Join(dir, "aruma.dump"), archive: filepath.Join(dir, "storage.tar.gz"), key: d.key}
	out := d.mustRun("", "backup", "--out", b.dump, "--storage-out", b.archive)
	if strings.Contains(out, "were not in") {
		t.Errorf("the backup misses files: %s", out)
	}
	return b
}

// archiveStorage is `tar -czf out -C dir .`, which the runbook of v0.4 used for the Storage
// volume after the dump. GNU tar exits with 1 for "file changed as we read it", which is not a
// failed archive.
func archiveStorage(t *testing.T, dir, out string) {
	t.Helper()
	cmd := exec.Command("tar", "-czf", out, "-C", dir, ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Fatalf("tar: %v\n%s", err, b)
		}
		t.Logf("tar: %s", strings.TrimSpace(string(b)))
	}
}

// restoreInto restores a backup into a fresh database and a fresh Storage directory and
// returns the deployment that serves them, on the original's address, with the key given.
func restoreInto(t *testing.T, orig *deployment, b backupSet, key string) *deployment {
	t.Helper()
	r := &deployment{t: t, bin: orig.bin, dbURL: newDatabase(t, "restored"), dir: t.TempDir(), key: key, port: orig.port, mport: orig.mport, log: &lockedBuf{}}
	if out, err := exec.Command("tar", "-xzf", b.archive, "-C", r.dir).CombinedOutput(); err != nil {
		t.Fatalf("untar: %v\n%s", err, out)
	}
	r.mustRun("", "restore", "--in", b.dump, "--yes")
	return r
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.dbURL += sep(d.dbURL) + "pool_max_conns=8" // as the operations guide suggests
	d.mustRun("", "migrate", "up")
	d.mustStart()
	fx := populate(t, d)
	want := normalize(t, capture(t, d, fx))

	b := takeBackup(t, d, t.TempDir())
	// After the backup: a new session, and a session revoked by a password change.
	a := newAPI(t, d.base())
	p := fx.Projects[0]
	later := signIn(a, p, p.Users["alice"].Email, p.Users["alice"].Password).str("refreshToken")
	d.stop() // the disaster: the server and its disk are gone

	r := restoreInto(t, d, b, d.key)
	if out := r.mustRun("", "migrate", "status"); !strings.Contains(out, "up to date") {
		t.Errorf("migrate status after the restore: %s", out)
	}
	r.mustStart()
	got := normalize(t, capture(t, r, fx))
	if diff := diffValues("", want, got); diff != "" {
		t.Errorf("the restored deployment differs from the original: %s", diff)
	}
	verifyBehaviour(t, r, fx, true)
	finishHalfUpload(t, r, fx)
	// A session that began after the backup is not in it: its refresh token is refused, and the
	// user signs in again.
	if s := refresh(newAPI(t, r.base()), p, later); s.Status == 200 {
		t.Errorf("a refresh token issued after the backup works after the restore: %s", s)
	}
	if t.Failed() {
		t.Logf("server log:\n%s", r.log.String())
	}
}

func sep(u string) string {
	if strings.Contains(u, "?") {
		return "&"
	}
	return "?"
}

// TestBackupWhileClientsWrite takes the runbook's backup while clients write to every service,
// Storage uploads finish and objects are overwritten and deleted, and checks the restore:
// everything acknowledged before the backup began is there, no object or live upload has a row
// without its bytes, no file is left without a row, and an upload cut by the backup can be
// finished. Run with -count=5: whether an upload finishes during the backup is a race.
func TestBackupWhileClientsWrite(t *testing.T) {
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.mustRun("", "migrate", "up")
	d.mustStart()
	fx := populate(t, d)
	p := fx.Projects[0]
	ld := startLoad(t, d, fx, p)
	time.Sleep(1500 * time.Millisecond)

	// An upload with two chunks in before the backup, the rest sent while it runs.
	a := newAPI(t, d.base())
	q := fx.Projects[1]
	qtok := q.token(t, a)
	data := det("straddle", 8*chunk)
	sess := startUpload(t, a, q, qtok, "data/straddle.bin", "application/octet-stream", nil)
	sendChunk(t, a, qtok, sess, 0, data[:2*chunk], false)

	cut := time.Now()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for off := 2 * chunk; off < len(data); off += chunk {
			if _, err := a.try("POST", sess, chunkHeaders(qtok, off, off+chunk == len(data)), data[off:off+chunk]); err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	b := takeBackup(t, d, t.TempDir())
	<-sent
	ld.stop()
	d.stop()

	r := restoreInto(t, d, b, d.key)
	r.mustStart()
	ra := newAPI(t, r.base())

	// Acknowledged before the backup began: present after the restore.
	var lost []string
	for i, at := range ld.rtdb {
		if at.Before(cut) {
			if got := ra.do("GET", rtdbURL(p, fmt.Sprintf("load/k%d/i", i), ""), bearer(fx.PAT), nil); got.Status != 200 || strings.TrimSpace(string(got.Body)) != fmt.Sprint(i) {
				lost = append(lost, fmt.Sprintf("rtdb k%d", i))
			}
		}
	}
	for i, at := range ld.docs {
		if at.Before(cut) {
			if got := ra.do("GET", docsBase(p)+fmt.Sprintf("/documents/load/d%d", i), bearer(fx.PAT), nil); got.Status != 200 {
				lost = append(lost, fmt.Sprintf("doc d%d", i))
			}
		}
	}
	tok := p.token(t, ra)
	for name, at := range ld.files {
		if at.Before(cut) {
			if got := ra.do("GET", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape(name)+"?alt=media", bearer(tok), nil); got.Status != 200 {
				lost = append(lost, "object "+name)
			}
		}
	}
	if len(lost) > 0 {
		t.Errorf("%d writes acknowledged before the backup began are missing after the restore: %v", len(lost), lost[:min(len(lost), 10)])
	}
	if len(ld.rtdb) < 20 || len(ld.docs) < 20 || len(ld.files) < 3 {
		t.Errorf("the load wrote too little to prove anything: %d rtdb, %d documents, %d objects", len(ld.rtdb), len(ld.docs), len(ld.files))
	}

	// Rows and files agree, from the start: the archive holds the files the dump names.
	rowsNoFile, filesNoRow := storageConsistency(t, r, true)
	if len(rowsNoFile) > 0 {
		t.Errorf("rows without their file after the restore: %v", rowsNoFile)
	}
	if len(filesNoRow) > 0 {
		t.Errorf("files without a row after the restore: %v", filesNoRow)
	}
	for _, pr := range fx.Projects {
		if n := checkObjectBytes(t, ra, pr); n < 7 {
			t.Errorf("%s: only %d objects after the restore", pr.ID, n)
		}
	}

	// The upload cut by the backup: the session has what the dump saw, or had finished; either
	// way the client gets its whole file.
	sh := bearer(qtok)
	sh["X-Goog-Upload-Protocol"], sh["X-Goog-Upload-Command"] = "resumable", "query"
	qr := ra.do("POST", sess, sh, nil)
	var got int
	fmt.Sscan(qr.Header.Get("X-Goog-Upload-Size-Received"), &got)
	switch st := qr.Header.Get("X-Goog-Upload-Status"); {
	case qr.Status == 200 && st == "active" && got >= 2*chunk && got%chunk == 0:
		t.Logf("the upload cut by the backup resumes at %d", got)
		sendChunk(t, ra, qtok, sess, got, data[got:], true)
	case qr.Status == 200 && st == "final":
		t.Logf("the upload had finished in the backup's snapshot")
	default:
		t.Fatalf("the upload cut by the backup: %s status=%q received=%d", qr, st, got)
	}
	dl := ra.do("GET", "/v0/b/"+q.Bucket+"/o/"+url.PathEscape("data/straddle.bin")+"?alt=media", bearer(qtok), nil)
	if string(dl.Body) != string(data) {
		t.Errorf("the upload cut by the backup is damaged after the restore: %d bytes", len(dl.Body))
	}
	// New writes after the restore get later commit times and generations than anything restored.
	checkMonotonic(t, ra, fx, p)
	if t.Failed() {
		t.Logf("server log:\n%s", r.log.String())
	}
}

func chunkHeaders(tok string, offset int, final bool) map[string]string {
	h := bearer(tok)
	h["X-Goog-Upload-Protocol"] = "resumable"
	h["X-Goog-Upload-Command"] = "upload"
	if final {
		h["X-Goog-Upload-Command"] = "upload, finalize"
	}
	h["X-Goog-Upload-Offset"] = fmt.Sprint(offset)
	return h
}

// TestBackupArchiveLongerThanGCGrace shows why the runbook of v0.4 lost files: it took the dump
// with `aruma backup` and then archived the Storage directory with tar. A file that stops being an
// object after the dump (the object was overwritten or deleted) is deleted gc_grace later, and an
// archive that reaches it later no longer holds it, while the dump still has the row: the
// restored object has a row and no bytes. `aruma backup --storage-out` holds the gc and copies
// the files of the dump's snapshot (storage/core/backup.go), which TestBackupWhileClientsWrite
// and the storage tests check; this test keeps the hazard of the two-step procedure in view.
func TestBackupArchiveLongerThanGCGrace(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the sweeper's 30-second round")
	}
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.extra = []string{"ARUMA_STORAGE_GC_GRACE=1s"}
	d.mustRun("", "migrate", "up")
	d.mustStart()
	fx := populate(t, d)
	p := fx.Projects[0]
	a := newAPI(t, d.base())
	tok := p.token(t, a)

	dir := t.TempDir()
	b := backupSet{dump: filepath.Join(dir, "aruma.dump"), archive: filepath.Join(dir, "storage.tar.gz"), key: d.key}
	d.mustRun("", "backup", "--out", b.dump)

	// After the dump the object is replaced; its first generation's file is now garbage.
	key := objectKey(t, d, "pub/small.txt", p)
	uploadObject(t, a, p, tok, objSpec{"pub/small.txt", "text/plain", 77, nil})
	file := filepath.Join(d.dir, filepath.FromSlash(strings.Join(strings.Split(key, "/")[:2], "/")), key[strings.LastIndex(key, "/")+1:][:2], key[strings.LastIndex(key, "/")+1:])
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := os.Stat(file); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the replaced object's file %s was not collected in 90 seconds", key)
		}
		time.Sleep(500 * time.Millisecond)
	}
	archiveStorage(t, d.dir, b.archive) // an archive that took longer than gc_grace to get here
	d.stop()

	r := restoreInto(t, d, b, d.key)
	r.mustStart()
	ra := newAPI(t, r.base())
	dl := ra.do("GET", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape("pub/small.txt")+"?alt=media", bearer(p.token(t, ra)), nil)
	if dl.Status == 200 {
		t.Fatalf("the object whose file was collected before the archive is served whole: the hazard is gone, update the operations guide and this test")
	}
	t.Logf("as the operations guide warns: the restored object has a row and no bytes: %s", dl)
}

// objectKey returns the blob key of the stored object.
func objectKey(t *testing.T, d *deployment, name string, p *project) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, d.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var key string
	if err := conn.QueryRow(ctx, `SELECT blob_key FROM storage.objects WHERE name = $1 AND project_number = $2::bigint`, name, p.Number).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestRestoreWithAnotherMasterKey: every key derives from ARUMA_MASTER_KEY, so a restore under
// another key cannot serve the data. What it must not do is issue anything: no ID token, no
// access with a CLI token. With the right key the same files serve everything.
func TestRestoreWithAnotherMasterKey(t *testing.T) {
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.mustRun("", "migrate", "up")
	d.mustStart()
	fx := populate(t, d)
	p := fx.Projects[0]
	al := p.Users["alice"]
	b := takeBackup(t, d, t.TempDir())
	d.stop()

	r := restoreInto(t, d, b, newMasterKey())
	if err := r.start(); err != nil {
		t.Logf("the server refuses to start under another master key: %v", err)
	} else {
		a := newAPI(t, r.base())
		if s := signIn(a, p, al.Email, al.Password); s.Status == 200 || s.str("idToken") != "" {
			t.Errorf("sign-in under another master key issued a token: %s", s)
		}
		if s := refresh(a, p, al.Refresh); s.Status == 200 {
			t.Errorf("a refresh token of the old key works under another one: %s", s)
		}
		if s := a.do("GET", "/console/v1/projects", bearer(fx.PAT), nil); s.Status == 200 {
			t.Errorf("a CLI token of the old key works under another one")
		}
		// The failure is loud in the log, not silent.
		if !strings.Contains(r.log.String(), "cannot open envelope") {
			t.Errorf("the log does not say why sign-in fails:\n%s", r.log.String())
		}
		r.stop()
		// The server under the wrong key changed nothing: the same database under the right key
		// serves everything.
		r.key = d.key
		r.mustStart()
		if s := signIn(newAPI(t, r.base()), p, al.Email, al.Password); s.Status != 200 {
			t.Errorf("sign-in under the right key, after a start under another one: %s", s)
		}
		if s := refresh(newAPI(t, r.base()), p, al.Refresh); s.Status != 200 {
			t.Errorf("a refresh token under the right key, after a start under another one: %s", s)
		}
		r.stop()
	}

	// The same files under the right key.
	r2 := restoreInto(t, d, b, d.key)
	r2.mustStart()
	a := newAPI(t, r2.base())
	if s := signIn(a, p, al.Email, al.Password); s.Status != 200 {
		t.Errorf("sign-in under the original key: %s", s)
	}
}

// TestRestoreOverExistingDatabase: restoring replaces what the database holds, in one
// transaction, so running it again, or over a database that has moved on, ends in the backup's
// state.
func TestRestoreOverExistingDatabase(t *testing.T) {
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.mustRun("", "migrate", "up")
	d.mustStart()
	fx := populate(t, d)
	p := fx.Projects[0]
	b := takeBackup(t, d, t.TempDir())
	// The database moves on after the backup: a user, a document.
	a := newAPI(t, d.base())
	signUpNew(t, a, p, "moved-on@"+p.ID+".example")
	d.stop()

	d.mustRun("", "restore", "--in", b.dump, "--yes") // over the live database, as the runbook says: server stopped
	d.mustRun("", "restore", "--in", b.dump, "--yes") // and again
	d.mustStart()
	a = newAPI(t, d.base())
	if s := signIn(a, p, "moved-on@"+p.ID+".example", "after-pass-1"); s.Status == 200 {
		t.Errorf("a user created after the backup survived the restore")
	}
	if s := signIn(a, p, p.Users["alice"].Email, p.Users["alice"].Password); s.Status != 200 {
		t.Errorf("sign-in after restoring over the same database: %s", s)
	}
	if out, err := d.run("", "restore", "--in", filepath.Join(t.TempDir(), "missing.dump"), "--yes"); err == nil {
		t.Errorf("restore of a missing file succeeded: %s", out)
	}
	if out, err := d.run("", "restore", "--in", b.dump); err == nil || !strings.Contains(out, "--yes") {
		t.Errorf("restore without --yes: %v %s", err, out)
	}
}

// TestRestoreWithoutTheStorageArchive: a database restored next to an empty Storage directory
// holds objects whose files are gone. The server must refuse to start and say what to do, not
// serve rows without bytes.
func TestRestoreWithoutTheStorageArchive(t *testing.T) {
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.mustRun("", "migrate", "up")
	d.mustStart()
	populate(t, d)
	b := takeBackup(t, d, t.TempDir())
	d.stop()

	r := &deployment{t: t, bin: d.bin, dbURL: newDatabase(t, "restored"), dir: t.TempDir(), key: d.key, port: d.port, mport: d.mport, log: &lockedBuf{}}
	r.mustRun("", "restore", "--in", b.dump, "--yes")
	err := r.start()
	if err == nil {
		t.Fatal("the server started on a restored database and an empty Storage directory")
	}
	if !strings.Contains(err.Error(), "INSTANCE") || !strings.Contains(err.Error(), "data directory") {
		t.Errorf("the refusal does not say what is wrong:\n%v", err)
	}
}
