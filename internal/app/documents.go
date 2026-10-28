package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/metrics"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/server"
	docapi "github.com/arumadev/aruma/internal/services/documents/api"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	"github.com/arumadev/aruma/internal/services/documents/query"
	docrest "github.com/arumadev/aruma/internal/services/documents/rest"
	"github.com/arumadev/aruma/internal/services/documents/webchannel"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// Documents is the Document Database as app wires it (ADR-024 to ADR-029).
//
// Its store opens in Boot, which Serve calls before the listener opens. A caller that serves
// App.Server's handler itself, without Serve (a test, a harness), must call Boot first, and
// Close when done: until Boot, every Document Database call answers UNAVAILABLE with
// errNotBooted's text. Boot is not run from the request path, since it may wait 30 seconds for
// the single-instance lock.
type Documents struct {
	// API is the service layer every transport and the dashboard's routes call.
	API   *docapi.Service
	GRPC  *docgrpc.Handler
	REST  *docrest.Handler
	store *bootStore
	pool  *pgxpool.Pool
	cfg   config.DocumentsLimits
	txns  documents.TxnConfig
	log   *slog.Logger

	instance *documents.Instance
	// instanceMu orders Boot's admission of streams with the lock's callbacks, which the guard
	// may run as soon as StartInstance returns.
	instanceMu sync.Mutex
	// hub is the Listen hub, the store's Notifier (ADR-026 §2), rebuilt when the
	// single-instance lock is taken again after its loss (ADR-026 §5).
	hub *hubs
	// usage counts the dashboard's usage; Boot starts its flushes, and Close's last one keeps
	// the counts across a restart.
	usage *documents.Usage
	// stopJobs ends the jobs Boot started (the retention, the index builds, the transactions'
	// sweep), and jobsDone
	// closes when they have.
	stopJobs context.CancelFunc
	jobsDone chan struct{}
}

// newDocuments wires the Document Database's surfaces on the control plane: its service
// accounts verify the Admin SDKs' self-signed JWTs, and every key or account deleted is
// forgotten by the verifier's cache at once (ADR-029 §3.8). A purged project's documents go
// with it (ADR-024 §1.1). Transaction ids are authenticated with a key derived from the master
// key, so that one issued before a restart is answered as expired, not forged (ADR-025 §7.5).
func newDocuments(cfg *config.Config, svc *core.Service, pool *pgxpool.Pool, keys *crypto.Keys, m *metrics.Metrics,
	idTokens idtoken.Verifier, revocations idtoken.Revocations, rulesInflight *rules.Inflight, logger *slog.Logger) *Documents {
	svc.OnProjectPurged(func(ctx context.Context, project int64) error {
		return documents.PurgeProject(ctx, pool, project)
	})
	publicURL := strings.TrimSuffix(cfg.PublicBaseURL().String(), "/")
	jwts := authn.NewServiceAccountJWTVerifier(svc, publicURL, logger)
	svc.OnServiceAccountRevoked(jwts)
	resolver := authn.NewDocuments(authn.DocumentsOptions{
		Verifier:           svc,
		ServiceAccountJWTs: jwts,
		Owners:             svc,
		PublicURL:          publicURL,
		TrustedProxies:     cfg.TrustedProxyPrefixes(),
		LoopbackDeployment: cfg.LoopbackDeployment(),
		DevOwnerToken:      cfg.Documents.DevOwnerToken,
		Logger:             logger,
	})
	store := &bootStore{}
	// Config has checked the setting: a value ParseMode refuses never reaches here.
	enforcement, _ := index.ParseMode(cfg.Documents.IndexEnforcement)
	l := cfg.Documents.Limits
	// The Listen hub serves the streams from the start; Boot binds it to the store it opens.
	hub := newHubs(listen.Options{LagEvents: l.StreamLagEvents, SnapshotRows: l.SnapshotRows, Log: logger})
	usage := documents.NewUsage(pool, nil, logger)
	api := docapi.New(docapi.Options{
		Store: store, Auth: resolver, Projects: svc, DevOpenRules: cfg.Documents.DevOpenRules,
		Rulesets: svc, IDTokens: idTokens, RulesInflight: rulesInflight,
		IndexEnforcement: enforcement, Listen: hub, Usage: usage, Logger: logger,
	})
	rpcMetrics := docgrpc.NewMetrics()
	m.Registry.MustRegister(rpcMetrics.Collectors()...)
	m.Registry.MustRegister(docgrpc.TransactionCollectors(store.transactionStats)...)
	m.Registry.MustRegister(query.Collectors()...)
	streams := docgrpc.New(docgrpc.Options{
		Service:        api,
		TrustedProxies: cfg.TrustedProxyPrefixes(),
		Limits: docgrpc.Limits{
			MessageBytes: l.MessageBytes, StreamFirstMessage: l.StreamFirstMessage,
			StreamIdle: l.StreamIdle, StreamWriteStall: l.StreamWriteStall, ServerStreamMax: l.ServerStreamMax, StreamsPerIP: l.StreamsPerIP,
			StreamsPerProject: l.StreamsPerProject, StreamOutboxBytes: l.StreamOutboxBytes, TargetsPerStream: l.TargetsPerStream,
		},
		Metrics:     rpcMetrics,
		IDTokens:    idTokens,
		Revocations: revocations,
		Logger:      logger,
	})
	// WebChannel runs the same streams, under the same limits (ADR-027 §5).
	channel := webchannel.New(webchannel.Options{
		Streams: streams, Keys: svc, Logger: logger,
		Limits: webchannel.Limits{
			MessageBytes: l.MessageBytes, SessionBytes: l.StreamOutboxBytes, SessionsPerIP: l.StreamsPerIP,
			WriteStall: l.StreamWriteStall,
		},
	})
	return &Documents{
		API:   api,
		GRPC:  streams,
		REST:  docrest.New(docrest.Options{Service: api, Channel: channel, Logger: logger}),
		store: store, pool: pool, cfg: l, log: logger, hub: hub, usage: usage,
		txns: documents.TxnConfig{
			Key:             keys.DocumentsTransactionKey(),
			WaitTimeout:     l.TxnWaitTimeout,
			MaxLifetime:     l.TxnMaxLifetime,
			MaxOpen:         l.MaxOpenTxns,
			MaxOpenUnlocked: l.MaxOpenUnlockedTxns,
			MaxOpenPerUser:  l.MaxOpenTxnsPerUser,
			MaxEnded:        l.MaxEndedTxns,
			MaxLocks:        l.MaxLocksPerTxn,
		},
	}
}

// RuleDocuments is what firestore.get() and firestore.exists() read in Storage's rules (ADR-021
// §3): the default database of the project each evaluation names, at the latest commit, through
// the store Boot opens.
func (d *Documents) RuleDocuments() rules.Documents { return docapi.Lookups(d.store) }

// mount puts the surfaces on the entry point.
func (d *Documents) mount(h *server.Handlers) {
	h.Documents, h.DocumentsREST, h.DocumentsOwns = d.GRPC, d.REST, docrest.Owns
}

// Boot takes the single-instance lock, trying for up to 30 seconds (ADR-024 §7), opens the
// store, starts the retention of versions and the changelog (ADR-024 §8), the index build job
// (ADR-024 §2.5) and the TTL sweeper (ADR-028 §2), which run while the lock is held, and the transactions' sweep (ADR-024 §6.3),
// and gives every project its
// '(default)' database. Losing the lock drops every open transaction and closes every Listen
// and Write stream with UNAVAILABLE, and the Listen hub, before o.OnLost runs; taking it again
// builds a new hub and admits streams again, before o.OnAcquired runs (ADR-026 §5).
// Without the lock the rest of Aruma serves and the Document Database answers UNAVAILABLE, and
// admits no stream, until the guard takes it. It runs before the listener opens; Close stops what it started.
func (d *Documents) Boot(ctx context.Context, o documents.InstanceOptions) error {
	o.Log = d.log
	onLost := o.OnLost
	o.OnLost = func() {
		d.instanceMu.Lock()
		defer d.instanceMu.Unlock()
		if s := d.store.s.Load(); s != nil {
			s.DropTransactions()
		}
		d.GRPC.InstanceLost()
		d.hub.close()
		if onLost != nil {
			onLost()
		}
	}
	onAcquired := o.OnAcquired
	o.OnAcquired = func() {
		d.instanceMu.Lock()
		defer d.instanceMu.Unlock()
		d.hub.rebuild()
		d.GRPC.InstanceRegained()
		if onAcquired != nil {
			onAcquired()
		}
	}
	// No stream joins before the lock is first taken (review streams-admitted-before-first-lock).
	d.GRPC.InstanceLost()
	d.instanceMu.Lock()
	d.instance = documents.StartInstance(ctx, d.pool.Config().ConnConfig, o)
	if d.instance.Held() {
		d.GRPC.InstanceRegained()
	}
	d.instanceMu.Unlock()
	store := documents.NewPGStore(d.pool, documents.Options{
		CommitConcurrency: d.cfg.CommitConcurrency, Instance: d.instance, Transactions: d.txns,
		IndexLimits: documents.IndexLimits{Composites: d.cfg.CompositeIndexes, FieldOverrides: d.cfg.FieldOverrides},
		Notifier:    d.hub,
	})
	d.store.set(store)
	d.hub.bind(store)
	var jctx context.Context
	jctx, d.stopJobs = context.WithCancel(ctx)
	d.jobsDone = make(chan struct{})
	var jobs sync.WaitGroup
	jobs.Go(func() { store.RunRetention(jctx, documents.RetentionOptions{Log: d.log}) })
	jobs.Go(func() { store.RunTransactions(jctx) })
	jobs.Go(func() { d.usage.Run(jctx, 0) })
	jobs.Go(func() { store.RunIndexBuilds(jctx, documents.IndexBuildOptions{Log: d.log}) })
	jobs.Go(func() {
		store.RunTTL(jctx, documents.TTLOptions{Every: d.cfg.TTLSweepInterval, Batch: d.cfg.TTLSweepBatch, Log: d.log})
	})
	go func() {
		jobs.Wait()
		close(d.jobsDone)
	}()
	if !d.instance.Held() {
		return nil
	}
	return ensureDatabases(ctx, d.pool, store)
}

// Close stops the retention job and the transactions' sweep, closes the Listen hub, and
// releases the lock.
func (d *Documents) Close() {
	if d.stopJobs != nil {
		d.stopJobs()
		<-d.jobsDone
	}
	if d.hub != nil {
		d.hub.close()
	}
	if d.instance != nil {
		d.instance.Close()
	}
}

// hubs is the Listen hub of the moment: the store's Notifier and the streams' source. The lock's
// loss closes it, and taking the lock again builds a new one bound to the same store.
type hubs struct {
	opts listen.Options
	cur  atomic.Pointer[listen.Hub]

	mu     sync.Mutex // orders bind and rebuild
	reader listen.Reader
}

var (
	_ documents.Notifier = (*hubs)(nil)
	_ docapi.ListenHub   = (*hubs)(nil)
)

func newHubs(o listen.Options) *hubs {
	h := &hubs{opts: o}
	h.cur.Store(listen.NewHub(o))
	return h
}

func (h *hubs) Committed(db documents.DB, seq int64, at time.Time, changes []documents.Change) {
	h.cur.Load().Committed(db, seq, at, changes)
}

func (h *hubs) Unknown(db documents.DB, seq int64) { h.cur.Load().Unknown(db, seq) }

func (h *hubs) Subscribe(ctx context.Context, db documents.DB) (*listen.Subscriber, error) {
	return h.cur.Load().Subscribe(ctx, db)
}

// bind gives the hub, and every later one, its reader.
func (h *hubs) bind(r listen.Reader) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reader = r
	h.cur.Load().Bind(r)
}

// rebuild replaces the hub with a new one.
func (h *hubs) rebuild() {
	h.mu.Lock()
	next := listen.NewHub(h.opts)
	if h.reader != nil {
		next.Bind(h.reader)
	}
	old := h.cur.Swap(next)
	h.mu.Unlock()
	old.Close()
}

// close ends the hub's subscriptions.
func (h *hubs) close() { h.cur.Load().Close() }

// ensureDatabases creates the '(default)' database of every active project that has none.
// Core creates it on first use as well, so a project made later needs nothing here.
func ensureDatabases(ctx context.Context, pool *pgxpool.Pool, store *documents.PGStore) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rows, err := pool.Query(ctx, `SELECT project_number FROM control.projects WHERE state = 'ACTIVE'`)
	if err != nil {
		return err
	}
	var projects []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range projects {
		if err := store.EnsureDatabase(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// errNotBooted answers every call before Boot. Our text.
var errNotBooted = &documents.Error{Code: documents.Unavailable,
	Message: "The Document Database has not started on this server: the process serving it did not call Boot."}

// bootStore is the Document Database's store once Boot opened it; before, every call is
// errNotBooted.
type bootStore struct {
	s atomic.Pointer[documents.PGStore]
}

var _ documents.Store = (*bootStore)(nil)

func (b *bootStore) set(s *documents.PGStore) { b.s.Store(s) }

func (b *bootStore) get() (*documents.PGStore, error) {
	s := b.s.Load()
	if s == nil {
		return nil, errNotBooted
	}
	return s, nil
}

func (b *bootStore) Database(ctx context.Context, db documents.DB) (documents.Database, error) {
	s, err := b.get()
	if err != nil {
		return documents.Database{}, err
	}
	return s.Database(ctx, db)
}

func (b *bootStore) Get(ctx context.Context, db documents.DB, p documents.Path, o documents.ReadOptions) (documents.Document, time.Time, error) {
	s, err := b.get()
	if err != nil {
		return documents.Document{}, time.Time{}, err
	}
	return s.Get(ctx, db, p, o)
}

func (b *bootStore) BatchGet(ctx context.Context, db documents.DB, paths []documents.Path, o documents.ReadOptions) ([]documents.Document, time.Time, error) {
	s, err := b.get()
	if err != nil {
		return nil, time.Time{}, err
	}
	return s.BatchGet(ctx, db, paths, o)
}

func (b *bootStore) ListDocuments(ctx context.Context, db documents.DB, r documents.ListRequest) (documents.ListResult, error) {
	s, err := b.get()
	if err != nil {
		return documents.ListResult{}, err
	}
	return s.ListDocuments(ctx, db, r)
}

func (b *bootStore) ListCollectionIDs(ctx context.Context, db documents.DB, r documents.ListCollectionIDsRequest) (documents.CollectionIDs, error) {
	s, err := b.get()
	if err != nil {
		return documents.CollectionIDs{}, err
	}
	return s.ListCollectionIDs(ctx, db, r)
}

func (b *bootStore) Commit(ctx context.Context, db documents.DB, writes []documents.Write) (documents.CommitResult, error) {
	s, err := b.get()
	if err != nil {
		return documents.CommitResult{}, err
	}
	return s.Commit(ctx, db, writes)
}

// transactionStats are the transactions' counters; zero before Boot.
func (b *bootStore) transactionStats() documents.TxnStats {
	if s := b.s.Load(); s != nil {
		return s.TransactionStats()
	}
	return documents.TxnStats{}
}

func (b *bootStore) BeginTransaction(ctx context.Context, db documents.DB, by documents.Principal, o documents.TransactionOptions) ([]byte, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.BeginTransaction(ctx, db, by, o)
}

func (b *bootStore) CommitTransaction(ctx context.Context, db documents.DB, t documents.TransactionRef, writes []documents.Write) (documents.CommitResult, error) {
	s, err := b.get()
	if err != nil {
		return documents.CommitResult{}, err
	}
	return s.CommitTransaction(ctx, db, t, writes)
}

func (b *bootStore) Rollback(ctx context.Context, db documents.DB, t documents.TransactionRef) error {
	s, err := b.get()
	if err != nil {
		return err
	}
	return s.Rollback(ctx, db, t)
}

func (b *bootStore) Scan(ctx context.Context, db documents.DB, r documents.ScanRequest) (documents.ListResult, error) {
	s, err := b.get()
	if err != nil {
		return documents.ListResult{}, err
	}
	return s.Scan(ctx, db, r)
}

func (b *bootStore) IndexCatalog(ctx context.Context, db documents.DB) (*index.Catalog, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.IndexCatalog(ctx, db)
}

func (b *bootStore) BeginIndexRead(ctx context.Context, db documents.DB) (*documents.IndexRead, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.BeginIndexRead(ctx, db)
}
