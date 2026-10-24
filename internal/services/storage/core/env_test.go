package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/store/storetest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The test bed of S2-D's tests: a Service on a fresh Postgres database with two projects, over
// an in-memory store that stands for the disk (memStore) until S2-A's Disk is merged, a lock of
// its own (memLock), a clock the test moves, fake users and a fake control plane.

const (
	projectA = 482915736204
	projectB = 582915736204
	ownerA   = "00000000-0000-7000-8000-000000000001"
)

type env struct {
	t        *testing.T
	s        *Service
	pool     *pgxpool.Pool
	store    *memStore
	clock    *clock
	users    *fakeUsers
	projects *testProjects
	logs     *syncBuffer
	a, b     Bucket
	opts     Options
}

func newEnv(t *testing.T, tune ...func(*Options)) *env {
	t.Helper()
	pool := storetest.NewPool(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO control.accounts (id, email, password_hash) VALUES ('` + ownerA + `', 'o@example.com', 'x')`,
		`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
		 VALUES (482915736204, 'chat-app', 'Chat', '` + ownerA + `', '1_a', 'aruma.test'),
		        (582915736204, 'other-app', 'Other', '` + ownerA + `', '1_b', 'aruma.test')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, pool: pool, store: newMemStore(), clock: &clock{t: time.Now()}, users: &fakeUsers{},
		projects: &testProjects{owner: ownerA}, logs: &syncBuffer{},
		a: Bucket{Project: projectA, Name: "chat-app.aruma.test"}, b: Bucket{Project: projectB, Name: "other-app.aruma.test"}}
	lim := DefaultLimits()
	lim.MinFreeBytes = 1 << 20
	e.opts = Options{Limits: lim, Keys: testKeys(t), Projects: e.projects, Inflight: rules.NewInflight(1 << 20),
		Users: e.users, DataDir: "/srv/aruma/storage", GCGrace: time.Minute, Now: e.clock.now}
	for _, f := range tune {
		f(&e.opts)
	}
	acquireLock = memAcquire(e.store)
	t.Cleanup(func() {
		acquireLock = blob.AcquireLock
		testHook = nil
		commitTx = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
		xactStatusOf = nil
	})
	e.s = e.service()
	if err := e.s.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.s.Close() })
	return e
}

// service builds a Service on the env's database and store: a process starting.
func (e *env) service() *Service {
	e.t.Helper()
	s, err := New(context.Background(), e.pool, e.store, e.opts, slog.New(slog.NewTextHandler(e.logs, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	// Until S2-E binds the rules: every principal passes, unless a test decides otherwise.
	s.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return nil }
	return s
}

// restart is a crash and a new process: the lock goes with the old one, the files the store
// synced stay (and the appended bytes it did not, the worst case for the next request), and the
// new process boots.
func (e *env) restart() *Service {
	e.t.Helper()
	e.store.crash()
	e.s = e.service()
	if err := e.s.Boot(context.Background()); err != nil {
		e.t.Fatalf("boot after the crash: %v", err)
	}
	return e.s
}

// crashAt makes the write order die at point: a panic the test recovers, as a process that
// stops at that instant.
func crashAt(point string) {
	testHook = func(p string) {
		if p == point {
			testHook = nil
			panic(errCrash)
		}
	}
}

var errCrash = errors.New("crash")

// crashed runs f and reports whether it crashed at the point crashAt named.
func crashed(f func()) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			if p != errCrash {
				panic(p)
			}
			ok = true
		}
	}()
	f()
	return false
}

func (e *env) count(sql string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// usage returns a project's account as stored.
func (e *env) usage(project int64) (used, reserved, anonymous, objects int64) {
	e.t.Helper()
	err := e.pool.QueryRow(context.Background(), `SELECT used_bytes, reserved_bytes, anonymous_reserved_bytes, object_count
		FROM storage.usage WHERE project_number = $1`, project).Scan(&used, &reserved, &anonymous, &objects)
	if err != nil {
		return 0, 0, 0, 0
	}
	return
}

// checkUsage fails unless a project's account holds exactly what its rows say: the invariant
// every path keeps.
func (e *env) checkUsage(project int64) {
	e.t.Helper()
	used, reserved, anonymous, objects := e.usage(project)
	wantUsed := e.count(`SELECT COALESCE(sum(size), 0) FROM storage.objects WHERE project_number = $1`, project)
	wantObjects := e.count(`SELECT count(*) FROM storage.objects WHERE project_number = $1`, project)
	wantReserved := e.count(`SELECT COALESCE(sum(reserved_bytes), 0) FROM storage.upload_sessions WHERE project_number = $1 AND state = 'active'`, project)
	wantAnon := e.count(`SELECT COALESCE(sum(reserved_bytes), 0) FROM storage.upload_sessions WHERE project_number = $1 AND state = 'active' AND principal_kind = 'anonymous'`, project)
	if used != wantUsed || objects != wantObjects || reserved != wantReserved || anonymous != wantAnon {
		e.t.Errorf("usage of %d: used %d objects %d reserved %d anonymous %d; the rows say %d, %d, %d, %d",
			project, used, objects, reserved, anonymous, wantUsed, wantObjects, wantReserved, wantAnon)
	}
}

func asAdmin() Principal { return Principal{Kind: PrincipalAdmin, ServiceAccount: "sa@chat-app"} }

func asUser(uid string) Principal {
	t := time.Now().Add(-time.Minute)
	return Principal{Kind: PrincipalUser, UID: uid, Token: map[string]any{"sub": uid}, AuthTime: t, IssuedAt: t}
}

func uploadSpec(name string, sf Surface) UploadSpec {
	return UploadSpec{Name: name, Surface: sf, Declared: -1}
}

// insert stores name with body through Insert.
func (e *env) insert(b Bucket, name string, body string) Object {
	e.t.Helper()
	o, err := e.s.Insert(context.Background(), b, asAdmin(), uploadSpec(name, SurfaceGCS), strings.NewReader(body), int64(len(body)))
	if err != nil {
		e.t.Fatalf("insert %s: %v", name, err)
	}
	return o
}

// read returns an object's bytes, through its row and its file.
func (e *env) read(b Bucket, name string) string {
	e.t.Helper()
	_, row, err := e.s.storedObject(context.Background(), b, name)
	if err != nil || row == nil {
		e.t.Fatalf("read %s: %v, %v", name, row, err)
	}
	r, err := e.s.store.Open(row.BlobKey)
	if err != nil {
		e.t.Fatalf("open %s's file: %v", name, err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	return string(got)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeUsers is Auth's live user state: every user exists unless marked.
type fakeUsers struct {
	mu       sync.Mutex
	disabled map[string]bool
	err      error
}

func (f *fakeUsers) State(ctx context.Context, project int64, uid string) (idtoken.UserState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return idtoken.UserState{}, f.err
	}
	return idtoken.UserState{Exists: true, Disabled: f.disabled[uid], Created: time.Unix(0, 0)}, nil
}

func (f *fakeUsers) disable(uid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disabled == nil {
		f.disabled = map[string]bool{}
	}
	f.disabled[uid] = true
}

// testProjects is the control plane: project A's owner is owner, until a test changes it.
type testProjects struct {
	mu     sync.Mutex
	owner  string
	audits []string
}

func (p *testProjects) ProjectForBucket(ctx context.Context, bucket string) (tcore.Project, error) {
	return tcore.Project{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
}

func (p *testProjects) GetProject(ctx context.Context, pr *authn.Principal, ref string) (tcore.Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pr.Kind != authn.KindAccount || pr.AccountID.String() != p.owner || ref != fmt.Sprint(projectA) {
		return tcore.Project{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	return tcore.Project{Number: projectA, State: "ACTIVE", StorageBucket: "chat-app.aruma.test"}, nil
}

func (p *testProjects) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	return nil, errors.New("no rules")
}

func (p *testProjects) Audit(ctx context.Context, pr *authn.Principal, project int64, action, target string, details map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.audits = append(p.audits, action+" "+target)
	return nil
}

func (p *testProjects) setOwner(o string) {
	p.mu.Lock()
	p.owner = o
	p.mu.Unlock()
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// memStore is blob.Store in memory: the disk of these tests until S2-A's Disk is merged. A file
// is visible from its Create, as on a disk; synced is the length a crash keeps.
type memStore struct {
	mu        sync.Mutex
	files     map[string]*memFile
	free      int64
	instance  *string
	deleteErr map[string]error
	// purgeCut, when positive, makes DeletePrefix stop with an error after that many files.
	purgeCut int
	// appendErr, when set, makes Append fail having written half of what it read.
	appendErr error
	// lock is the process's hold on the directory, which a crash releases.
	lock *memLock
}

type memFile struct {
	data   []byte
	synced int
	mod    time.Time
}

func newMemStore() *memStore { return &memStore{files: map[string]*memFile{}, free: 1 << 40} }

func (m *memStore) check(key string) error {
	if !blob.ValidKey(key) {
		return fmt.Errorf("blob: invalid key %q", key)
	}
	return nil
}

func (m *memStore) Create(key string) (blob.Writer, error) {
	if err := m.check(key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[key]; ok {
		return nil, fs.ErrExist
	}
	f := &memFile{mod: time.Now()}
	m.files[key] = f
	return &memWriter{m: m, key: key, f: f}, nil
}

func (m *memStore) Open(key string) (blob.Reader, error) {
	if err := m.check(key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &memBlobReader{Reader: bytes.NewReader(slices.Clone(f.data))}, nil
}

func (m *memStore) Delete(key string) error {
	if err := m.check(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.deleteErr[key]; err != nil {
		return err
	}
	delete(m.files, key)
	return nil
}

func (m *memStore) Promote(staging, key string) error {
	if err := errors.Join(m.check(staging), m.check(key)); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[staging]
	if !ok {
		if _, done := m.files[key]; done {
			// S2-A's resumable Promote: the move happened before; the file is made durable.
			m.files[key].synced = len(m.files[key].data)
			return errPromotedEarlier
		}
		return fs.ErrNotExist
	}
	if _, ok := m.files[key]; ok {
		return fs.ErrExist
	}
	delete(m.files, staging)
	f.synced = len(f.data) // durable on return
	m.files[key] = f
	return nil
}

func (m *memStore) CreateStaging(key string) (blob.Staging, error) {
	w, err := m.Create(key)
	if err != nil {
		return nil, err
	}
	return &memStaging{m: m, f: w.(*memWriter).f}, nil
}

func (m *memStore) OpenStaging(key string) (blob.Staging, error) {
	if err := m.check(key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &memStaging{m: m, f: f}, nil
}

func (m *memStore) Walk(prefix string, fn func(key string, modTime time.Time) error) error {
	m.mu.Lock()
	var keys []string
	mods := map[string]time.Time{}
	for k, f := range m.files {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
			mods[k] = f.mod
		}
	}
	m.mu.Unlock()
	slices.Sort(keys)
	for _, k := range keys {
		if err := fn(k, mods[k]); err != nil {
			return err
		}
	}
	return nil
}

func (m *memStore) DeletePrefix(prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.files {
		if strings.HasPrefix(k, prefix) {
			if m.purgeCut > 0 && n == m.purgeCut {
				m.purgeCut = 0
				return errors.New("blob: interrupted")
			}
			delete(m.files, k)
			n++
		}
	}
	return nil
}

func (m *memStore) FreeBytes() (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	used := int64(0)
	for _, f := range m.files {
		used += int64(len(f.data))
	}
	return m.free - used, nil
}

// crash drops what was not synced from staging files, keeping it from a fraction of them: a
// power loss may keep appended bytes or not. Object files written but not committed stay
// visible, as a partial file on a disk.
func (m *memStore) crash() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock != nil {
		_ = m.lock.Release()
	}
	for k, f := range m.files {
		if strings.HasPrefix(k, "s/") && len(k)%2 == 0 {
			f.data = f.data[:f.synced]
		}
	}
}

func (m *memStore) keys(prefix string) []string {
	var out []string
	_ = m.Walk(prefix, func(k string, _ time.Time) error { out = append(out, k); return nil })
	return out
}

func (m *memStore) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[key]
	return ok
}

type memWriter struct {
	m   *memStore
	key string
	f   *memFile
}

func (w *memWriter) Write(p []byte) (int, error) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.f.data = append(w.f.data, p...)
	return len(p), nil
}

func (w *memWriter) Commit() error {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.f.synced = len(w.f.data)
	return nil
}

func (w *memWriter) Abort() error { return w.m.Delete(w.key) }

type memBlobReader struct{ *bytes.Reader }

func (r *memBlobReader) Close() error { return nil }

type memStaging struct {
	m *memStore
	f *memFile
}

func (s *memStaging) Append(r io.Reader, limit int64) (int64, error) {
	buf := make([]byte, 32<<10)
	var n int64
	for n < limit {
		k, err := r.Read(buf[:min(int64(len(buf)), limit-n)])
		if k > 0 {
			s.m.mu.Lock()
			fail := s.m.appendErr
			if fail != nil {
				// A device that fails in the middle of a write: half of what was read lands.
				k /= 2
			}
			s.f.data = append(s.f.data, buf[:k]...)
			s.m.mu.Unlock()
			n += int64(k)
			if fail != nil {
				return n, fail
			}
		}
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (s *memStaging) Truncate(n int64) error {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	s.f.data = s.f.data[:n]
	s.f.synced = min(s.f.synced, int(n))
	return nil
}

func (s *memStaging) Sync() error {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	s.f.synced = len(s.f.data)
	return nil
}

func (s *memStaging) Size() (int64, error) {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return int64(len(s.f.data)), nil
}

func (s *memStaging) Close() error { return nil }

// memAcquire is AcquireLock over a memStore: one holder at a time, the others wait and log, and
// the INSTANCE marker lives in the store.
func memAcquire(m *memStore) func(ctx context.Context, root string, log func(time.Duration)) (blob.Lock, error) {
	held := make(chan struct{}, 1)
	return func(ctx context.Context, root string, log func(time.Duration)) (blob.Lock, error) {
		start := time.Now()
		for {
			select {
			case held <- struct{}{}:
				l := &memLock{m: m, held: held}
				m.mu.Lock()
				m.lock = l
				m.mu.Unlock()
				return l, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
				log(time.Since(start))
			}
		}
	}
}

type memLock struct {
	m    *memStore
	held chan struct{}
	once sync.Once
}

func (l *memLock) Instance() (string, bool, error) {
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	if l.m.instance == nil {
		return "", false, nil
	}
	return *l.m.instance, true, nil
}

func (l *memLock) SetInstance(id string) error {
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	l.m.instance = &id
	return nil
}

func (l *memLock) Release() error {
	l.once.Do(func() { <-l.held })
	return nil
}

// metricValue reads a counter or gauge, the one of a vector whose label values are labels.
func metricValue(t *testing.T, c prometheus.Collector, labels ...string) float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			var values []string
			for _, l := range m.GetLabel() {
				values = append(values, l.GetValue())
			}
			if !slices.Equal(values, labels) {
				continue
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	return 0
}
