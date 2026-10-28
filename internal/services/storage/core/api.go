package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/ratelimit"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The contract of S2-0 (storage-layout.md, "the S2-0 contract"): every exported type and every method's
// signature, fixed until S6 ends. A method a later task writes panics with its owner's name, in
// the file that task owns. Changing anything here goes through the integrator.

// PrincipalKind is who a request acts as.
type PrincipalKind uint8

const (
	// PrincipalAnonymous is a client without a credential: the rules see request.auth = null.
	PrincipalAnonymous PrincipalKind = iota
	// PrincipalUser is a client signed in with an Aruma Auth ID token.
	PrincipalUser
	// PrincipalAdmin is a service account or the dashboard's owner. It bypasses the rules, as in
	// Firebase (ADR-021 §7).
	PrincipalAdmin
)

// Principal is who a request acts as. An upload session binds the one its start carried, and
// finalizes with it (ADR-022 §7).
type Principal struct {
	Kind PrincipalKind
	// UID and Token are a user's: the uid (the token's sub) and the verified token's claims, for
	// request.auth. AuthTime and IssuedAt are the token's auth_time and iat, for the user's live
	// state at finalize (idtoken.UserState.Admits).
	UID      string
	Token    map[string]any
	AuthTime time.Time
	IssuedAt time.Time
	// ServiceAccount names an admin service account, for the audit log.
	ServiceAccount string
	// ConsoleAccount is the id of the dashboard account an admin request came from: checked again
	// at finalize (the account must still own the project) and audited (ADR-022 §15).
	ConsoleAccount string
}

// Surface is the API a request came through: it decides the defaults stored, how tokens show,
// and whether the rules apply.
type Surface uint8

const (
	SurfaceFirebase Surface = iota + 1 // Firebase REST, /v0
	SurfaceGCS                         // the GCS JSON API, for Admin SDKs
	SurfaceConsole                     // the dashboard
)

func (s Surface) String() string {
	switch s {
	case SurfaceFirebase:
		return "firebase"
	case SurfaceGCS:
		return "gcs"
	case SurfaceConsole:
		return "console"
	}
	return "invalid"
}

// Bucket is a resolved bucket: the default bucket of an active project.
type Bucket struct {
	Project int64
	Name    string
}

// Object is a stored object's current generation.
type Object struct {
	Bucket, Name               string
	Generation, Metageneration int64
	Size                       int64
	MD5                        [16]byte
	CRC32C                     uint32
	// The content fields; "" is unset.
	ContentType, ContentEncoding, ContentDisposition, ContentLanguage, CacheControl string
	// Metadata is the custom metadata. It never holds firebaseStorageDownloadTokens.
	Metadata map[string]string
	// DownloadTokens are the object's download tokens, opened from their seal.
	DownloadTokens   []string
	Created, Updated time.Time
}

// The writable content fields, by the names both surfaces give them: the keys of Patch.Fields.
const (
	FieldContentType        = "contentType"
	FieldContentEncoding    = "contentEncoding"
	FieldContentDisposition = "contentDisposition"
	FieldContentLanguage    = "contentLanguage"
	FieldCacheControl       = "cacheControl"
)

// TokensKey is the custom metadata key GCS shows the download tokens under (ADR-022 §9).
const TokensKey = "firebaseStorageDownloadTokens"

// Patch is a metadata change, or the metadata an upload brings.
type Patch struct {
	// Fields sets content fields by name (Field*): a nil value deletes the field.
	Fields map[string]*string
	// Metadata merges into the custom metadata key by key: a nil value deletes the key.
	Metadata map[string]*string
	// ClearMetadata is "metadata": null: the custom metadata goes before Metadata merges.
	ClearMetadata bool
	// Tokens replaces the download tokens: the GCS surface only, where an admin sets
	// metadata.firebaseStorageDownloadTokens. An empty list removes them all.
	Tokens *[]string
}

// Preconditions are GCS's ifGenerationMatch and the like (ADR-022 §6.2). nil is absent;
// IfGenerationMatch = 0 means the object must not exist.
type Preconditions struct {
	IfGenerationMatch, IfGenerationNotMatch, IfMetagenerationMatch, IfMetagenerationNotMatch *int64
}

// SourceConditions are a rewrite's conditions on its source: GCS's sourceGeneration (0 is the
// current one; any other than the current is ErrNotFound) and ifSourceGenerationMatch and the
// like. Rewrite checks them on the row whose file it copies, so they hold for exactly the bytes
// copied. The zero value is no condition. (Added at S4-A's review, with the lead's agreement.)
type SourceConditions struct {
	Generation int64
	Pre        Preconditions
}

// BucketConditions are GCS's ifMetagenerationMatch and ifMetagenerationNotMatch on a bucket,
// checked by SetCORS on the settings row it locks. The zero value is no condition. (Added at
// S4-A's review, with the lead's agreement.)
type BucketConditions struct {
	IfMetagenerationMatch, IfMetagenerationNotMatch *int64
}

// Checksums are what a client claims its object's checksums are; nil is not claimed.
type Checksums struct {
	CRC32C *uint32
	MD5    *[16]byte
}

// UploadSpec is what an upload's start (or a one-request upload) says about the object.
type UploadSpec struct {
	Name string
	// Attrs is the incoming metadata, after StripClientTokens on SurfaceFirebase.
	Attrs Patch
	// Declared is the size the client declared, -1 when it declared none (Android).
	Declared int64
	Surface  Surface
	Pre      Preconditions
	Want     Checksums
	// IP is the client's address, for the live sessions per IP (IPv6 counted per /64); the zero
	// Addr counts against no address.
	IP netip.Addr
}

// Chunk is one request on an upload session.
type Chunk struct {
	// Offset is where the body starts in the object.
	Offset int64
	Body   io.Reader
	// Length is the body's length, -1 when unknown (chunked).
	Length int64
	// Finalize ends the upload with this chunk.
	Finalize bool
	// Total is the object's size when the request states it, else -1.
	Total int64
	// Want is what this request claims the object's checksums are (Go sends X-Goog-Hash on the
	// last request alone). Finalize merges it with UploadSpec.Want; a claim that contradicts
	// the start's is ErrChecksumMismatch.
	Want Checksums
}

// UploadStatus is an upload session's state.
type UploadStatus uint8

const (
	UploadActive UploadStatus = iota + 1
	UploadFinal
	UploadCancelled
)

func (s UploadStatus) String() string {
	switch s {
	case UploadActive:
		return "active"
	case UploadFinal:
		return "final"
	case UploadCancelled:
		return "cancelled"
	}
	return "invalid"
}

// UploadState is where an upload session stands after a request.
type UploadState struct {
	Status   UploadStatus
	Received int64 // the durable bytes
	// Object is the stored object, when the session is final and succeeded.
	Object *Object
	// Err is the refusal, when the session is final and failed: the same error on every repeat
	// of the finalize and on every query.
	Err error
}

// ListOptions are a list request's. Empty strings are absent.
type ListOptions struct {
	Prefix, Delimiter, PageToken, StartOffset, EndOffset string
	// MaxResults is the page size: 1000 when 0, at most Limits.ListMaxResults.
	MaxResults int
	// Surface: names with an empty segment are left out of /v0 lists (ADR-022 §13).
	Surface Surface
}

// ListPage is one page of a list, prefixes and objects in one byte order.
type ListPage struct {
	Prefixes      []string
	Objects       []Object
	NextPageToken string // "" on the last page
}

// CORSRule is one of a bucket's cors[] rules, with GCS's meaning (ADR-022 §11).
type CORSRule struct {
	Origins, Methods, ResponseHeaders []string
	MaxAgeSeconds                     int
}

// BucketInfo is the bucket resource: what buckets.get and buckets.patch return (B §4.6).
type BucketInfo struct {
	Bucket
	Metageneration   int64     // 1 until the settings first change, then up by one per change
	Created, Updated time.Time // the project's creation; the last settings change (Created until then)
	CORS             []CORSRule
}

// Usage is a project's storage use and quota.
type Usage struct {
	UsedBytes, ReservedBytes, Objects int64
	QuotaBytes                        int64 // 0 is unlimited
}

// Projects is the control plane, as Storage needs it; tenancy implements it.
type Projects interface {
	// ProjectForBucket resolves a bucket name to its active project; an apierr NOT_FOUND when
	// none, any other error when it cannot tell.
	ProjectForBucket(ctx context.Context, bucket string) (tcore.Project, error)
	// GetProject is the ownership gate: a dashboard session's upload finalizes only while its
	// account still owns the project (ADR-022 §15).
	GetProject(ctx context.Context, p *authn.Principal, ref string) (tcore.Project, error)
	// CompiledRelease returns the ruleset of a project's release, here firebase.storage/default
	// (ADR-022 §2, §8).
	CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error)
	// Audit records an administrative action once it took effect.
	Audit(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error
}

// Options configure the service.
type Options struct {
	Limits Limits
	// Keys seal the download tokens, hash the upload ids and sign the page tokens.
	Keys     *crypto.Keys
	Projects Projects
	// Documents are what firestore.get() reads in Storage rules: rules.NoDocuments{} until
	// v0.4. nil is rules.NoDocuments{}.
	Documents rules.Documents
	// Inflight is the process's one cap on evaluation memory, shared with the rules API.
	Inflight *rules.Inflight
	// Users reports a user's live state when an upload finalizes (ADR-022 §7).
	Users idtoken.Users
	// DataDir is the store's data directory (storage.data_dir), which Boot locks and binds to
	// the database (ADR-022 §5.1): an absolute path.
	DataDir string
	// GCGrace is how long a file no row points to waits before it is deleted, covering a read
	// between its row and its file (storage.gc_grace); 0 is one minute.
	GCGrace time.Duration
	// Metrics are the collectors Storage reports to (metrics.go); nil makes collectors nobody
	// registers, so reporting costs nothing and shows nowhere.
	Metrics *Metrics
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service is Cloud Storage apart from any transport. It holds every field the tasks need from
// the contract on, so no task adds one.
type Service struct {
	pool *pgxpool.Pool
	// reads runs the read queries outside a transaction. It is for reads alone: a write goes
	// through inTx or begin, which commit synchronously (durability.go), and
	// TestWritesOnlyInTransactions holds every task to it.
	reads     *queries.Queries
	store     blob.Store
	keys      *crypto.Keys
	projects  Projects
	documents rules.Documents
	inflight  *rules.Inflight
	users     idtoken.Users
	limits    Limits
	dataDir   string
	gcGrace   time.Duration
	now       func() time.Time
	log       *slog.Logger
	metrics   *Metrics

	// starts are the upload starts allowed per client address and in the whole process, which
	// both surfaces take through AllowUploadStart (ADR-022 §17).
	startsPerIP *ratelimit.Limiter
	starts      *ratelimit.Limiter

	// Boot's (boot.go): the data directory's lock, held for the life of the process, the
	// instance id that binds it to this database, and the keys found under p/ and s/ before the
	// listener opened, which Run checks against the rows and deletes (ADR-022 §5.1).
	lock       blob.Lock
	instance   string
	candidates []string

	// sessions serialises the requests on one upload session (uploads.go).
	sessions sessionLocks
	// diskInflight is the bytes every write in flight has reserved against min_free_bytes,
	// checked and added in one atomic step (quota.go).
	diskInflight atomic.Int64

	// gate admits the writes and Run until Close, which waits for them before it releases the
	// lock (run.go; added by S2-D at the review, with the lead's agreement).
	gate writeGate

	// authorizeHook decides for principals other than admins in this package's tests, until
	// S2-E binds the rules (rules.go).
	authorizeHook func(ctx context.Context, b Bucket, p Principal, op ruleOp) error
}

// sessionLocks is a lock per upload session, held by a chunk across its append, Sync and the
// update of received, and taken by finalize, cancel and expiry too (ADR-022 §6.1). An entry
// lives while a request holds or waits for it. A waiter leaves when its request's context ends,
// so requests piled on a session held by a slow one do not outlive their clients.
type sessionLocks struct {
	mu   sync.Mutex
	held map[string]*sessionLock // by the id hash, as a string
}

type sessionLock struct {
	held chan struct{} // one slot: full while a request holds the lock
	refs int
}

// New returns the service on an open pool and a store. It checks the limits and needs Keys,
// Projects, Inflight and an absolute DataDir.
func New(ctx context.Context, pool *pgxpool.Pool, store blob.Store, opts Options, log *slog.Logger) (*Service, error) {
	if err := opts.Limits.Validate(); err != nil {
		return nil, err
	}
	if pool == nil || store == nil || opts.Keys == nil || opts.Projects == nil || opts.Inflight == nil {
		return nil, errors.New("storage: New needs a pool, a store, Keys, Projects and Inflight")
	}
	if !filepath.IsAbs(opts.DataDir) {
		return nil, errors.New("storage: the data directory must be an absolute path")
	}
	if opts.Metrics == nil {
		opts.Metrics = NewMetrics()
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.Documents == nil {
		opts.Documents = rules.NoDocuments{}
	}
	if opts.GCGrace <= 0 {
		opts.GCGrace = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		pool: pool, reads: queries.New(pool), store: store, keys: opts.Keys, projects: opts.Projects,
		documents: opts.Documents, inflight: opts.Inflight, users: opts.Users, limits: opts.Limits,
		dataDir: opts.DataDir, gcGrace: opts.GCGrace, now: opts.Now, log: log, metrics: opts.Metrics,
		startsPerIP: ratelimit.NewLimiter(opts.Limits.UploadStartsPerMinutePerIP, opts.Limits.UploadStartsPerMinutePerIP),
		starts:      ratelimit.NewLimiter(opts.Limits.UploadStartsPerSecond*60, opts.Limits.UploadStartsPerSecond),
		sessions:    sessionLocks{held: map[string]*sessionLock{}},
	}, nil
}

// Metrics returns the collectors Storage reports to; the surfaces report theirs (bytes,
// decompressions cut) there too.
func (s *Service) Metrics() *Metrics { return s.metrics }

// AllowUploadStart takes an upload start from a client address: upload_starts_per_minute_per_ip
// for the address (IPv6 counted per /64), then upload_starts_per_second for the process. Both
// surfaces call it before StartUpload, Insert and a console session; when it refuses, it says
// how long until a start is allowed. The zero address counts on the process alone: the
// surfaces pass it for an admin's start (ADR-022 §17).
func (s *Service) AllowUploadStart(ip netip.Addr) (bool, time.Duration) {
	if ip.IsValid() {
		if ok, wait := s.startsPerIP.Allow(ratelimit.AddrKey(ip)); !ok {
			return false, wait
		}
	}
	return s.starts.Allow("")
}

// Limits returns the configured limits; the surfaces enforce the ones on requests.
func (s *Service) Limits() Limits { return s.limits }

// Now is the server time.
func (s *Service) Now() time.Time { return s.now() }

// Inside the package: the shared types of the tasks' internal calls.

// ruleOp is one operation for the rules to decide (ADR-022 §8). Resource is the existing object
// or nil; Incoming the object a write would store (request.resource), or nil.
type ruleOp struct {
	Method             rules.Method
	Name               string // the object's name, or a list's prefix
	List               bool
	Resource, Incoming *Object
	Patch              *Patch
	Surface            Surface
}

// objectWrite is what commitObject makes visible (commit.go, ADR-022 §5).
type objectWrite struct {
	bucket Bucket
	// object is the new generation; commitObject sets its Generation, and its Metageneration
	// to 1.
	object Object
	// blobKey is the durable file at an object key, written or promoted before the commit.
	blobKey string
	pre     Preconditions
	// reserved is the quota the write holds, turned into used bytes by the commit.
	reserved int64
	// session is the id hash of the upload session it finalizes; nil for Insert and Rewrite.
	session []byte
}
