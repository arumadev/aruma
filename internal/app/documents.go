package app

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/metrics"
	"github.com/arumadev/aruma/internal/server"
	docapi "github.com/arumadev/aruma/internal/services/documents/api"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	docrest "github.com/arumadev/aruma/internal/services/documents/rest"
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
	GRPC  *docgrpc.Handler
	REST  *docrest.Handler
	store *bootStore
	pool  *pgxpool.Pool
	cfg   config.DocumentsLimits
	log   *slog.Logger

	instance *documents.Instance
	// stopRetention ends the retention job Boot started, and retentionDone closes when it has.
	stopRetention context.CancelFunc
	retentionDone chan struct{}
}

// newDocuments wires the Document Database's surfaces on the control plane: its service
// accounts verify the Admin SDKs' self-signed JWTs, and every key or account deleted is
// forgotten by the verifier's cache at once (ADR-029 §3.8). A purged project's documents go
// with it (ADR-024 §1.1).
func newDocuments(cfg *config.Config, svc *core.Service, pool *pgxpool.Pool, m *metrics.Metrics, logger *slog.Logger) *Documents {
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
	api := docapi.New(docapi.Options{
		Store: store, Auth: resolver, Projects: svc, DevOpenRules: cfg.Documents.DevOpenRules, Logger: logger,
	})
	l := cfg.Documents.Limits
	rpcMetrics := docgrpc.NewMetrics()
	m.Registry.MustRegister(rpcMetrics.Collectors()...)
	return &Documents{
		GRPC: docgrpc.New(docgrpc.Options{
			Service:        api,
			TrustedProxies: cfg.TrustedProxyPrefixes(),
			Limits: docgrpc.Limits{
				MessageBytes: l.MessageBytes, StreamFirstMessage: l.StreamFirstMessage,
				StreamWriteStall: l.StreamWriteStall, ServerStreamMax: l.ServerStreamMax, StreamsPerIP: l.StreamsPerIP,
			},
			Metrics: rpcMetrics,
			Logger:  logger,
		}),
		REST:  docrest.New(docrest.Options{Service: api, Logger: logger}),
		store: store, pool: pool, cfg: l, log: logger,
	}
}

// mount puts the surfaces on the entry point.
func (d *Documents) mount(h *server.Handlers) {
	h.Documents, h.DocumentsREST, h.DocumentsOwns = d.GRPC, d.REST, docrest.Owns
}

// Boot takes the single-instance lock, trying for up to 30 seconds (ADR-024 §7), opens the
// store, starts the retention of versions and the changelog (ADR-024 §8), which runs while the
// lock is held, and gives every project its '(default)' database. Without the lock the rest of
// Aruma serves and the Document Database answers UNAVAILABLE until the guard takes it. It runs
// before the listener opens; Close stops what it started.
func (d *Documents) Boot(ctx context.Context, o documents.InstanceOptions) error {
	o.Log = d.log
	d.instance = documents.StartInstance(ctx, d.pool.Config().ConnConfig, o)
	store := documents.NewPGStore(d.pool, documents.Options{CommitConcurrency: d.cfg.CommitConcurrency, Instance: d.instance})
	d.store.set(store)
	var rctx context.Context
	rctx, d.stopRetention = context.WithCancel(ctx)
	d.retentionDone = make(chan struct{})
	go func() {
		defer close(d.retentionDone)
		store.RunRetention(rctx, documents.RetentionOptions{Log: d.log})
	}()
	if !d.instance.Held() {
		return nil
	}
	return ensureDatabases(ctx, d.pool, store)
}

// Close stops the retention job and releases the lock.
func (d *Documents) Close() {
	if d.stopRetention != nil {
		d.stopRetention()
		<-d.retentionDone
	}
	if d.instance != nil {
		d.instance.Close()
	}
}

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
