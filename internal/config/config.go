// Package config loads the server configuration: an optional YAML file, overridden by
// ARUMA_* environment variables. Instance settings live here, never in the console.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/arumadev/aruma/internal/platform/log"
	"github.com/arumadev/aruma/internal/platform/rules"
)

// Config is the validated server configuration.
type Config struct {
	// DatabaseURL is the Postgres connection string. Required.
	DatabaseURL log.Secret `yaml:"database_url"`
	// PublicURL is the address developers and SDKs reach the server at. Required.
	PublicURL string `yaml:"public_url"`
	// MasterKey is 32 random bytes, base64-encoded, used to derive HMAC and encryption keys. Required.
	MasterKey log.Secret `yaml:"master_key"`

	// Listen is the address of the single public entry point.
	Listen string `yaml:"listen"`
	// MetricsListen serves /metrics on a separate, private address. Empty disables it.
	MetricsListen string `yaml:"metrics_listen"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `yaml:"log_level"`

	// SignupOpen lets anyone create an account from the console. Closed by default.
	SignupOpen bool `yaml:"signup_open"`
	// ProjectDomain is the base domain for authDomain, storage buckets and service account
	// emails. Defaults to the host of PublicURL.
	ProjectDomain string `yaml:"project_domain"`
	// TrustedProxies lists the CIDRs whose X-Forwarded-For header is believed, and, on the
	// Document Database, whose X-Forwarded-Proto: https lets a credential arrive without TLS
	// (ADR-029 §3.7). Empty by default.
	TrustedProxies []string `yaml:"trusted_proxies"`

	Session   Session   `yaml:"session"`
	Mail      Mail      `yaml:"mail"`
	RTDB      RTDB      `yaml:"rtdb"`
	Rules     Rules     `yaml:"rules"`
	Storage   Storage   `yaml:"storage"`
	Documents Documents `yaml:"documents"`

	masterKey      []byte
	publicURL      *url.URL
	trustedProxies []netip.Prefix
	rulesLimits    rules.Limits
}

// Session holds console session lifetimes.
type Session struct {
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	AbsoluteTimeout time.Duration `yaml:"absolute_timeout"`
}

// Mail holds the instance's email settings. SMTP itself is set per project in the console.
type Mail struct {
	// DevMailbox allows the dev mailbox: projects without an SMTP host keep their Auth emails in
	// the console instead of sending them. Defaults to on when public_url is a loopback address.
	DevMailbox *bool `yaml:"dev_mailbox"`
	// SMTPAllowPrivate lets projects use SMTP hosts on loopback, private or link-local
	// addresses (a relay on the same machine or network). Off, the SMTP settings cannot reach
	// the instance's own network. Defaults to on when public_url is a loopback address.
	SMTPAllowPrivate *bool `yaml:"smtp_allow_private"`
}

// RTDB holds the Realtime Database settings (ADR-018, ADR-019, ADR-020).
type RTDB struct {
	// Enabled serves Realtime Database. Off by default until v0.2 ships.
	Enabled bool `yaml:"enabled"`
	// Domain is the parent domain of the database hosts: a database is reached at
	// https://<project_id>-default-rtdb.<domain> (ADR-018). Empty, databases are reached on the
	// main host with ?ns=<project_id>-default-rtdb, which only suits local development.
	Domain string `yaml:"domain"`
	// DevOpenRules lets clients on this machine read and write every database, as Firebase's
	// test-mode rules {".read": true, ".write": true} do. Off, every database keeps the locked
	// rules a new database starts with, and only admin and service credentials reach the data
	// (ADR-020). It is meant for development only, so the setting is
	// accepted only when public_url and listen are loopback addresses, and it applies only to
	// connections whose peer is a loopback address.
	DevOpenRules bool `yaml:"dev_open_rules"`
	// AllowedOrigins, when set, lists the hosts whose web pages may open a connection, besides
	// a database's own host. Empty allows every origin, as Firebase does: the protocol carries
	// its credentials inside the messages, never in cookies, so a foreign page gains nothing a
	// script elsewhere could not do.
	AllowedOrigins []string `yaml:"allowed_origins"`
	// Limits are the server's capacity limits. The limits the SDKs enforce themselves (path
	// depth 32, path length 768 bytes) are fixed: raising them would only break clients.
	Limits RTDBLimits `yaml:"limits"`
}

// RTDBLimits are the configurable Realtime Database limits, with Firebase's published values
// as defaults where a developer can see them (ADR-019).
type RTDBLimits struct {
	// MessageBytes caps one reassembled WebSocket message, and so one write from an SDK.
	MessageBytes int64 `yaml:"message_bytes"`
	// UnauthenticatedMessageBytes caps a message on a connection that has not signed in, so
	// anonymous clients cannot make the server buffer large messages.
	UnauthenticatedMessageBytes int64 `yaml:"unauthenticated_message_bytes"`
	// InflightBytes caps the bytes of partly received messages across all connections; past
	// it, new multi-frame messages are refused.
	InflightBytes int64 `yaml:"inflight_bytes"`
	// RESTWriteBytes caps one REST write body (Firebase allows 256 MB).
	RESTWriteBytes int64 `yaml:"rest_write_bytes"`
	// StringBytes caps one string value.
	StringBytes int64 `yaml:"string_bytes"`
	// WriteBytesPerMinute caps the bytes written to one database per minute.
	WriteBytesPerMinute int64 `yaml:"write_bytes_per_minute"`
	// ConnectionsPerProject caps the connections to one project's databases. A connection
	// counts once it has signed in or done its first permitted operation, so anonymous sockets
	// cannot take a project's slots.
	ConnectionsPerProject int `yaml:"connections_per_project"`
	// ConnectionsPerIP caps the open connections from one client address (resolved through
	// trusted_proxies), counted from the handshake.
	ConnectionsPerIP int `yaml:"connections_per_ip"`
	// OutboxBytes caps what may wait to be sent on one connection; a slower client is
	// disconnected, and resynchronises when it reconnects.
	OutboxBytes int64 `yaml:"outbox_bytes"`
	// ReadRows caps the stored rows one read loads: a get, the first data of a listen, or
	// the value a conditional write compares. A larger read is refused with too_big (400 over
	// REST) before it is loaded, so one read cannot hold a database's writer and its memory.
	// A million rows took about 1.2 s and 50 MB to build on an Apple M1, besides the rows
	// themselves, and gives a reply of some 20 MB with short values, inside the default
	// outbox_bytes.
	ReadRows int64 `yaml:"read_rows"`
}

// Defaults returns a configuration with every optional field set.
func Defaults() Config {
	return Config{
		Listen:        ":8080",
		MetricsListen: "127.0.0.1:9464",
		LogLevel:      "info",
		Session: Session{
			IdleTimeout:     30 * time.Minute,
			AbsoluteTimeout: 8 * time.Hour,
		},
		RTDB: RTDB{Limits: RTDBLimits{
			MessageBytes:                16 << 20,
			UnauthenticatedMessageBytes: 1 << 20,
			InflightBytes:               512 << 20,
			RESTWriteBytes:              16 << 20,
			StringBytes:                 10 << 20,
			WriteBytesPerMinute:         64 << 20,
			ConnectionsPerProject:       10_000,
			ConnectionsPerIP:            500,
			OutboxBytes:                 64 << 20,
			ReadRows:                    1_000_000,
		}},
		Rules: Rules{Limits: RulesLimits(rules.DefaultLimits())},
		Documents: Documents{Limits: DocumentsLimits{
			MessageBytes:        17 << 20,
			StreamFirstMessage:  10 * time.Second,
			StreamWriteStall:    30 * time.Second,
			ServerStreamMax:     5 * time.Minute,
			StreamsPerIP:        500,
			CommitConcurrency:   4,
			TxnWaitTimeout:      20 * time.Second,
			TxnMaxLifetime:      270 * time.Second,
			MaxOpenTxns:         1000,
			MaxOpenUnlockedTxns: 10_000,
			MaxOpenTxnsPerUser:  100,
			MaxEndedTxns:        100_000,
			MaxLocksPerTxn:      10_000,
		}},
		Storage: Storage{
			GCGrace: time.Minute,
			// storage/core.DefaultLimits, which config may not import; a test in internal/app
			// keeps the two equal.
			Limits: StorageLimits{
				ObjectBytes:                5 << 30,
				ProjectBytes:               5 << 30,
				MinFreeBytes:               2 << 30,
				EmptySessionTTL:            10 * time.Minute,
				UploadIdleTTL:              time.Hour,
				UploadSessionTTL:           7 * 24 * time.Hour,
				ConsoleSessionTTL:          time.Hour,
				AnonymousStagedFraction:    0.2,
				MetadataBytes:              8 << 10,
				JSONBodyBytes:              64 << 10,
				HeaderFieldBytes:           1 << 10,
				UploadSessionsPerProject:   1000,
				UploadSessionsPerUser:      50,
				UploadSessionsPerIP:        100,
				UploadStartsPerMinutePerIP: 60,
				UploadStartsPerSecond:      1000,
				UploadRequestsPerIP:        64,
				MinBytesPerSecond:          4 << 10,
				ListMaxResults:             1000,
			},
		},
	}
}

// Documents holds the Document Database settings (ADR-024 to ADR-029).
type Documents struct {
	// Enabled serves the Document Database: gRPC on the public port (h2c or TLS) and REST under
	// /v1/projects/{p}/databases (ADR-027). Off by default until v0.4 ships.
	Enabled bool `yaml:"enabled"`
	// DevOpenRules lets clients on this machine read and write every database without admin
	// credentials, until security rules arrive (ADR-025 §8). Off, only service accounts, the
	// project's owner and the dev owner reach the data. It is meant for development only, so the
	// setting is accepted only when public_url and listen are loopback addresses, and it
	// applies only to requests whose peer is a loopback address.
	DevOpenRules bool `yaml:"dev_open_rules"`
	// DevOwnerToken accepts the emulators' Bearer owner from this machine as an admin of every
	// project, as the Admin SDKs send it in emulator mode and Node's with ssl:false over gRPC.
	// It is meant for development only, so the setting is accepted only when public_url and
	// listen are loopback addresses, and it applies only to connections whose peer is a
	// loopback address (ADR-029 §6). Off, owner is refused as on every other surface.
	DevOwnerToken bool `yaml:"dev_owner_token"`
	// Limits bound what the Document Database's transports accept and hold.
	Limits DocumentsLimits `yaml:"limits"`
}

// DocumentsLimits are the Document Database's limits (ADR-024 §6.3, ADR-026 §3, ADR-027 §3).
type DocumentsLimits struct {
	// MessageBytes caps one gRPC message and one REST body: a 16 MiB document from a mobile
	// client and its overhead (ADR-027 §3.1). Production's own cap on a request, 10 MiB, is
	// core's, whatever the surface.
	MessageBytes int64 `yaml:"message_bytes"`
	// StreamFirstMessage bounds the wait for a stream's first message, read and its credential
	// checked; only then are the server's read and write timeouts lifted (ADR-027 §3.2).
	StreamFirstMessage time.Duration `yaml:"stream_first_message"`
	// StreamWriteStall bounds each message a server stream writes, so a client that stopped
	// reading does not hold its stream (BatchGetDocuments, RunQuery, RunAggregationQuery).
	StreamWriteStall time.Duration `yaml:"stream_write_stall"`
	// ServerStreamMax bounds a server stream that sent no grpc-timeout; it ends with
	// DEADLINE_EXCEEDED.
	ServerStreamMax time.Duration `yaml:"server_stream_max"`
	// StreamsPerIP caps the streams open from one client address (resolved through
	// trusted_proxies).
	StreamsPerIP int `yaml:"streams_per_ip"`
	// CommitConcurrency is how many commits of one database run in Postgres at once (ADR-024
	// §6.3); the others wait in the process, not in the pool.
	CommitConcurrency int `yaml:"commit_concurrency"`
	// TxnWaitTimeout bounds a wait for a transaction's lock: the waiting transaction, or commit,
	// is then ABORTED with production's "Too much contention" text (ADR-024 §6.3).
	TxnWaitTimeout time.Duration `yaml:"txn_wait_timeout"`
	// TxnMaxLifetime ends a transaction this long after it began.
	TxnMaxLifetime time.Duration `yaml:"txn_max_lifetime"`
	// MaxOpenTxns bounds a database's open locking (pessimistic) transactions; over it,
	// BeginTransaction is ABORTED.
	MaxOpenTxns int `yaml:"max_open_txns"`
	// MaxOpenUnlockedTxns bounds a database's open read-only and optimistic transactions, the
	// ones end users open; over it, BeginTransaction is ABORTED.
	MaxOpenUnlockedTxns int `yaml:"max_open_unlocked_txns"`
	// MaxOpenTxnsPerUser bounds one end user's open read-only and optimistic transactions on a
	// database, so that one user cannot take MaxOpenUnlockedTxns from the others.
	MaxOpenTxnsPerUser int `yaml:"max_open_txns_per_user"`
	// MaxEndedTxns bounds the records of ended transactions kept for their ids' answers until
	// 300 s after Begin; past it the oldest go early.
	MaxEndedTxns int `yaml:"max_ended_txns"`
	// MaxLocksPerTxn bounds the documents and listings one transaction locks or, optimistic, reads.
	MaxLocksPerTxn int `yaml:"max_locks_per_txn"`
}

// Storage holds the Cloud Storage for Firebase settings (ADR-022).
type Storage struct {
	// Enabled serves Storage. Off by default until v0.3 ships.
	Enabled bool `yaml:"enabled"`
	// DataDir holds the objects' bytes: an absolute path on a local file system, required when
	// Storage is enabled. One server owns it, under an exclusive lock, and it is bound to one
	// database (ADR-022 §5.1). NFS is not supported.
	DataDir string `yaml:"data_dir"`
	// GCGrace is how long a file no row points to any more waits before it is deleted, covering
	// a read between its row and its file (ADR-022 §5).
	GCGrace time.Duration `yaml:"gc_grace"`
	// Limits bound what Storage accepts and holds (ADR-022 §17).
	Limits StorageLimits `yaml:"limits"`
}

// StorageLimits are Storage's limits (ADR-022 §17). It mirrors storage/core.Limits field for
// field, so each converts to the other and the compiler keeps the two in step; internal/config
// may not import a service, so the checks are made here, the same as core.Limits.Validate makes
// them.
type StorageLimits struct {
	ObjectBytes                int64         `yaml:"object_bytes"`                    // one object, one request body, and what decompression may produce
	ProjectBytes               int64         `yaml:"project_bytes"`                   // a project's quota, staging included; 0 is unlimited
	MinFreeBytes               int64         `yaml:"min_free_bytes"`                  // disk space no write may take, shared with Postgres
	EmptySessionTTL            time.Duration `yaml:"empty_session_ttl"`               // the life of an upload session that received no byte
	UploadIdleTTL              time.Duration `yaml:"upload_idle_ttl"`                 // after it without progress, a session's unused reservation is freed
	UploadSessionTTL           time.Duration `yaml:"upload_session_ttl"`              // an upload session's life
	ConsoleSessionTTL          time.Duration `yaml:"console_session_ttl"`             // the life of an upload session the dashboard issues
	AnonymousStagedFraction    float64       `yaml:"anonymous_staged_fraction"`       // the share of project_bytes uploads without an identity may hold
	MetadataBytes              int           `yaml:"metadata_bytes"`                  // custom metadata, keys and values together
	JSONBodyBytes              int64         `yaml:"json_body_bytes"`                 // a PATCH body, an upload start body, a multipart JSON part
	HeaderFieldBytes           int           `yaml:"header_field_bytes"`              // one of contentType, cacheControl, contentDisposition, contentEncoding, contentLanguage
	UploadSessionsPerProject   int           `yaml:"upload_sessions_per_project"`     // live upload sessions per project
	UploadSessionsPerUser      int           `yaml:"upload_sessions_per_user"`        // live upload sessions per user
	UploadSessionsPerIP        int           `yaml:"upload_sessions_per_ip"`          // live upload sessions per client address
	UploadStartsPerMinutePerIP int           `yaml:"upload_starts_per_minute_per_ip"` // upload starts per client address
	UploadStartsPerSecond      int           `yaml:"upload_starts_per_second"`        // upload starts in the whole server
	UploadRequestsPerIP        int           `yaml:"upload_requests_per_ip"`          // concurrent upload and write requests per client address
	MinBytesPerSecond          int64         `yaml:"min_bytes_per_second"`            // below it over 30 seconds, an upload request is cut
	ListMaxResults             int           `yaml:"list_max_results"`                // the largest list page, at most 1000
}

// The ceilings of object_bytes (GCS's largest object) and json_body_bytes, and what a multipart
// body carries besides them, as storage/core defines them.
const (
	maxStorageObjectBytes   = 5 << 40
	maxStorageJSONBodyBytes = 1 << 20
	storageMultipartBytes   = 64 << 10
)

// BodyCap is the largest body a Storage request may carry: an object, a multipart upload's JSON
// part and its boundaries, as storage/core.Limits.BodyCap computes it. The ceilings keep the sum
// from overflowing.
func (l StorageLimits) BodyCap() int64 {
	return l.ObjectBytes + l.JSONBodyBytes + storageMultipartBytes
}

// Rules holds the settings of the Firestore and Storage security rules engine (ADR-021).
type Rules struct {
	// Limits bound what a ruleset may hold and what one evaluation may do.
	Limits RulesLimits `yaml:"limits"`
}

// RulesLimits are the rules engine's limits (ADR-021 §4), defaulting to rules.DefaultLimits. It
// mirrors rules.Limits field for field, so each converts to the other and the compiler keeps
// the two in step. Firebase's limits are part of compatibility: a value may lower one, and a
// value above Firebase's is refused, since it would allow what Firebase denies. Aruma's own
// limits protect the server from hostile rules and may be set freely.
type RulesLimits struct {
	// Firebase's.
	CallDepth           int `yaml:"call_depth"`            // the longest chain of function calls
	FunctionLets        int `yaml:"function_lets"`         // let bindings in one function
	FunctionArgs        int `yaml:"function_args"`         // parameters of one function
	ExpressionDepth     int `yaml:"expression_depth"`      // the deepest nesting of an expression
	MatchDepth          int `yaml:"match_depth"`           // nested match blocks
	MatchSegments       int `yaml:"match_segments"`        // path segments of nested match blocks
	MatchVariables      int `yaml:"match_variables"`       // path variables of nested match blocks
	SourceBytes         int `yaml:"source_bytes"`          // the size of a rules file
	Expressions         int `yaml:"expressions"`           // expressions evaluated for one request
	LookupsPerOperation int `yaml:"lookups_per_operation"` // documents one Firestore operation reads
	LookupsPerRequest   int `yaml:"lookups_per_request"`   // documents a Firestore batch reads
	StorageLookups      int `yaml:"storage_lookups"`       // Firestore documents one Storage request reads
	RulesetsPerProject  int `yaml:"rulesets_per_project"`  // the rulesets one project keeps

	// Aruma's.
	MemberChain       int   `yaml:"member_chain"`        // a chain of .field and [index]
	Work              int64 `yaml:"work"`                // work units of one evaluation (patterns, collections)
	ValueBytes        int64 `yaml:"value_bytes"`         // one string or bytes value
	ValueElements     int   `yaml:"value_elements"`      // one list, map or set
	AllocBytes        int64 `yaml:"alloc_bytes"`         // the values one evaluation builds
	InflightBytes     int64 `yaml:"inflight_bytes"`      // the values every evaluation in the process builds together
	RegexInstructions int   `yaml:"regex_instructions"`  // the compiled program of one pattern
	RequestSegments   int   `yaml:"request_segments"`    // the segments of a request path
	RegexCacheEntries int   `yaml:"regex_cache_entries"` // patterns built during evaluation a ruleset keeps
	RegexCacheBytes   int64 `yaml:"regex_cache_bytes"`   // the estimated programs of those patterns
	RegexBytes        int64 `yaml:"regex_bytes"`         // the estimated programs of one ruleset's literal patterns
	// CompiledRulesetsBytes caps the compiled rulesets kept in memory, by estimated weight.
	CompiledRulesetsBytes int64 `yaml:"compiled_rulesets_bytes"`
}

// Load reads the YAML file at path (skipped when empty), applies environment overrides from
// getenv, and validates the result.
func Load(path string, getenv func(string) string) (*Config, error) {
	c := Defaults()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
	}
	if err := c.applyEnv(getenv); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyEnv(getenv func(string) string) error {
	str := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	secret := func(name string, dst *log.Secret) {
		if v := getenv(name); v != "" {
			*dst = log.Secret(v)
		}
	}
	var errs []error
	boolean := func(name string, dst *bool) {
		if v := getenv(name); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			*dst = b
		}
	}
	integer := func(name string, dst *int64) {
		if v := getenv(name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			*dst = n
		}
	}
	duration := func(name string, dst *time.Duration) {
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			*dst = d
		}
	}

	secret("ARUMA_DATABASE_URL", &c.DatabaseURL)
	str("ARUMA_PUBLIC_URL", &c.PublicURL)
	secret("ARUMA_MASTER_KEY", &c.MasterKey)
	str("ARUMA_LISTEN", &c.Listen)
	if v, ok := lookup(getenv, "ARUMA_METRICS_LISTEN"); ok {
		c.MetricsListen = v // may be set to empty to disable
	}
	str("ARUMA_LOG_LEVEL", &c.LogLevel)
	boolean("ARUMA_SIGNUP_OPEN", &c.SignupOpen)
	str("ARUMA_PROJECT_DOMAIN", &c.ProjectDomain)
	if v := getenv("ARUMA_TRUSTED_PROXIES"); v != "" {
		c.TrustedProxies = splitList(v)
	}
	duration("ARUMA_SESSION_IDLE_TIMEOUT", &c.Session.IdleTimeout)
	duration("ARUMA_SESSION_ABSOLUTE_TIMEOUT", &c.Session.AbsoluteTimeout)
	if v := getenv("ARUMA_MAIL_DEV_MAILBOX"); v != "" {
		var b bool
		boolean("ARUMA_MAIL_DEV_MAILBOX", &b)
		c.Mail.DevMailbox = &b
	}
	if v := getenv("ARUMA_MAIL_SMTP_ALLOW_PRIVATE"); v != "" {
		var b bool
		boolean("ARUMA_MAIL_SMTP_ALLOW_PRIVATE", &b)
		c.Mail.SMTPAllowPrivate = &b
	}
	boolean("ARUMA_RTDB_ENABLED", &c.RTDB.Enabled)
	str("ARUMA_RTDB_DOMAIN", &c.RTDB.Domain)
	boolean("ARUMA_RTDB_DEV_OPEN_RULES", &c.RTDB.DevOpenRules)
	if v := getenv("ARUMA_RTDB_ALLOWED_ORIGINS"); v != "" {
		c.RTDB.AllowedOrigins = splitList(v)
	}
	lim := &c.RTDB.Limits
	integer("ARUMA_RTDB_MESSAGE_BYTES", &lim.MessageBytes)
	integer("ARUMA_RTDB_UNAUTHENTICATED_MESSAGE_BYTES", &lim.UnauthenticatedMessageBytes)
	integer("ARUMA_RTDB_INFLIGHT_BYTES", &lim.InflightBytes)
	perIP := int64(lim.ConnectionsPerIP)
	integer("ARUMA_RTDB_CONNECTIONS_PER_IP", &perIP)
	lim.ConnectionsPerIP = int(perIP)
	integer("ARUMA_RTDB_REST_WRITE_BYTES", &lim.RESTWriteBytes)
	integer("ARUMA_RTDB_STRING_BYTES", &lim.StringBytes)
	integer("ARUMA_RTDB_WRITE_BYTES_PER_MINUTE", &lim.WriteBytesPerMinute)
	integer("ARUMA_RTDB_OUTBOX_BYTES", &lim.OutboxBytes)
	integer("ARUMA_RTDB_READ_ROWS", &lim.ReadRows)
	conns := int64(lim.ConnectionsPerProject)
	integer("ARUMA_RTDB_CONNECTIONS_PER_PROJECT", &conns)
	lim.ConnectionsPerProject = int(conns)

	count := func(name string, dst *int) {
		n := int64(*dst)
		integer(name, &n)
		*dst = int(n)
	}
	rl := &c.Rules.Limits
	count("ARUMA_RULES_CALL_DEPTH", &rl.CallDepth)
	count("ARUMA_RULES_FUNCTION_LETS", &rl.FunctionLets)
	count("ARUMA_RULES_FUNCTION_ARGS", &rl.FunctionArgs)
	count("ARUMA_RULES_EXPRESSION_DEPTH", &rl.ExpressionDepth)
	count("ARUMA_RULES_MATCH_DEPTH", &rl.MatchDepth)
	count("ARUMA_RULES_MATCH_SEGMENTS", &rl.MatchSegments)
	count("ARUMA_RULES_MATCH_VARIABLES", &rl.MatchVariables)
	count("ARUMA_RULES_SOURCE_BYTES", &rl.SourceBytes)
	count("ARUMA_RULES_EXPRESSIONS", &rl.Expressions)
	count("ARUMA_RULES_LOOKUPS_PER_OPERATION", &rl.LookupsPerOperation)
	count("ARUMA_RULES_LOOKUPS_PER_REQUEST", &rl.LookupsPerRequest)
	count("ARUMA_RULES_STORAGE_LOOKUPS", &rl.StorageLookups)
	count("ARUMA_RULES_RULESETS_PER_PROJECT", &rl.RulesetsPerProject)
	count("ARUMA_RULES_MEMBER_CHAIN", &rl.MemberChain)
	integer("ARUMA_RULES_WORK", &rl.Work)
	integer("ARUMA_RULES_VALUE_BYTES", &rl.ValueBytes)
	count("ARUMA_RULES_VALUE_ELEMENTS", &rl.ValueElements)
	integer("ARUMA_RULES_ALLOC_BYTES", &rl.AllocBytes)
	integer("ARUMA_RULES_INFLIGHT_BYTES", &rl.InflightBytes)
	count("ARUMA_RULES_REGEX_INSTRUCTIONS", &rl.RegexInstructions)
	count("ARUMA_RULES_REQUEST_SEGMENTS", &rl.RequestSegments)
	count("ARUMA_RULES_REGEX_CACHE_ENTRIES", &rl.RegexCacheEntries)
	integer("ARUMA_RULES_REGEX_CACHE_BYTES", &rl.RegexCacheBytes)
	integer("ARUMA_RULES_REGEX_BYTES", &rl.RegexBytes)
	integer("ARUMA_RULES_COMPILED_RULESETS_BYTES", &rl.CompiledRulesetsBytes)

	fraction := func(name string, dst *float64) {
		if v := getenv(name); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			*dst = f
		}
	}
	boolean("ARUMA_DOCUMENTS_ENABLED", &c.Documents.Enabled)
	boolean("ARUMA_DOCUMENTS_DEV_OPEN_RULES", &c.Documents.DevOpenRules)
	boolean("ARUMA_DOCUMENTS_DEV_OWNER_TOKEN", &c.Documents.DevOwnerToken)
	dl := &c.Documents.Limits
	integer("ARUMA_DOCUMENTS_MESSAGE_BYTES", &dl.MessageBytes)
	duration("ARUMA_DOCUMENTS_STREAM_FIRST_MESSAGE", &dl.StreamFirstMessage)
	duration("ARUMA_DOCUMENTS_STREAM_WRITE_STALL", &dl.StreamWriteStall)
	duration("ARUMA_DOCUMENTS_SERVER_STREAM_MAX", &dl.ServerStreamMax)
	count("ARUMA_DOCUMENTS_STREAMS_PER_IP", &dl.StreamsPerIP)
	count("ARUMA_DOCUMENTS_COMMIT_CONCURRENCY", &dl.CommitConcurrency)
	duration("ARUMA_DOCUMENTS_TXN_WAIT_TIMEOUT", &dl.TxnWaitTimeout)
	duration("ARUMA_DOCUMENTS_TXN_MAX_LIFETIME", &dl.TxnMaxLifetime)
	count("ARUMA_DOCUMENTS_MAX_OPEN_TXNS", &dl.MaxOpenTxns)
	count("ARUMA_DOCUMENTS_MAX_OPEN_UNLOCKED_TXNS", &dl.MaxOpenUnlockedTxns)
	count("ARUMA_DOCUMENTS_MAX_OPEN_TXNS_PER_USER", &dl.MaxOpenTxnsPerUser)
	count("ARUMA_DOCUMENTS_MAX_ENDED_TXNS", &dl.MaxEndedTxns)
	count("ARUMA_DOCUMENTS_MAX_LOCKS_PER_TXN", &dl.MaxLocksPerTxn)
	boolean("ARUMA_STORAGE_ENABLED", &c.Storage.Enabled)
	str("ARUMA_STORAGE_DATA_DIR", &c.Storage.DataDir)
	duration("ARUMA_STORAGE_GC_GRACE", &c.Storage.GCGrace)
	sl := &c.Storage.Limits
	integer("ARUMA_STORAGE_OBJECT_BYTES", &sl.ObjectBytes)
	integer("ARUMA_STORAGE_PROJECT_BYTES", &sl.ProjectBytes)
	integer("ARUMA_STORAGE_MIN_FREE_BYTES", &sl.MinFreeBytes)
	duration("ARUMA_STORAGE_EMPTY_SESSION_TTL", &sl.EmptySessionTTL)
	duration("ARUMA_STORAGE_UPLOAD_IDLE_TTL", &sl.UploadIdleTTL)
	duration("ARUMA_STORAGE_UPLOAD_SESSION_TTL", &sl.UploadSessionTTL)
	duration("ARUMA_STORAGE_CONSOLE_SESSION_TTL", &sl.ConsoleSessionTTL)
	fraction("ARUMA_STORAGE_ANONYMOUS_STAGED_FRACTION", &sl.AnonymousStagedFraction)
	count("ARUMA_STORAGE_METADATA_BYTES", &sl.MetadataBytes)
	integer("ARUMA_STORAGE_JSON_BODY_BYTES", &sl.JSONBodyBytes)
	count("ARUMA_STORAGE_HEADER_FIELD_BYTES", &sl.HeaderFieldBytes)
	count("ARUMA_STORAGE_UPLOAD_SESSIONS_PER_PROJECT", &sl.UploadSessionsPerProject)
	count("ARUMA_STORAGE_UPLOAD_SESSIONS_PER_USER", &sl.UploadSessionsPerUser)
	count("ARUMA_STORAGE_UPLOAD_SESSIONS_PER_IP", &sl.UploadSessionsPerIP)
	count("ARUMA_STORAGE_UPLOAD_STARTS_PER_MINUTE_PER_IP", &sl.UploadStartsPerMinutePerIP)
	count("ARUMA_STORAGE_UPLOAD_STARTS_PER_SECOND", &sl.UploadStartsPerSecond)
	count("ARUMA_STORAGE_UPLOAD_REQUESTS_PER_IP", &sl.UploadRequestsPerIP)
	integer("ARUMA_STORAGE_MIN_BYTES_PER_SECOND", &sl.MinBytesPerSecond)
	count("ARUMA_STORAGE_LIST_MAX_RESULTS", &sl.ListMaxResults)
	return errors.Join(errs...)
}

// lookup distinguishes an empty variable from an unset one. getenv cannot, so "-" is accepted
// as an explicit empty value.
func lookup(getenv func(string) string, name string) (string, bool) {
	v := getenv(name)
	if v == "" {
		return "", false
	}
	if v == "-" {
		return "", true
	}
	return v, true
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *Config) validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database_url (ARUMA_DATABASE_URL) is required"))
	}

	if c.PublicURL == "" {
		errs = append(errs, errors.New("public_url (ARUMA_PUBLIC_URL) is required"))
	} else if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("public_url %q must be an absolute http(s) URL", c.PublicURL))
	} else if u.Path != "" && u.Path != "/" {
		errs = append(errs, fmt.Errorf("public_url %q must not have a path", c.PublicURL))
	} else {
		u.Path = ""
		c.publicURL = u
		if c.ProjectDomain == "" {
			c.ProjectDomain = u.Hostname()
		}
	}

	if c.MasterKey == "" {
		errs = append(errs, errors.New("master_key (ARUMA_MASTER_KEY) is required: 32 random bytes, base64"))
	} else if k, err := base64.StdEncoding.DecodeString(c.MasterKey.Reveal()); err != nil || len(k) != 32 {
		errs = append(errs, errors.New("master_key must be exactly 32 bytes, base64-encoded"))
	} else {
		c.masterKey = k
	}

	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen %q: %w", c.Listen, err))
	}
	if c.MetricsListen != "" {
		if _, _, err := net.SplitHostPort(c.MetricsListen); err != nil {
			errs = append(errs, fmt.Errorf("metrics_listen %q: %w", c.MetricsListen, err))
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log_level %q must be debug, info, warn or error", c.LogLevel))
	}

	c.trustedProxies = nil
	for _, p := range c.TrustedProxies {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			if addr, aerr := netip.ParseAddr(p); aerr == nil {
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			} else {
				errs = append(errs, fmt.Errorf("trusted_proxies: %q is not a CIDR or address", p))
				continue
			}
		}
		c.trustedProxies = append(c.trustedProxies, prefix.Masked())
	}

	errs = append(errs, c.validateRTDB()...)
	errs = append(errs, c.validateRules()...)
	errs = append(errs, c.validateStorage()...)
	errs = append(errs, c.validateDocuments()...)

	if c.Session.IdleTimeout <= 0 || c.Session.AbsoluteTimeout <= 0 {
		errs = append(errs, errors.New("session timeouts must be positive"))
	} else if c.Session.IdleTimeout > c.Session.AbsoluteTimeout {
		errs = append(errs, errors.New("session.idle_timeout must not exceed session.absolute_timeout"))
	}
	return errors.Join(errs...)
}

// validateRTDB checks the rtdb section. It runs after public_url is parsed.
func (c *Config) validateRTDB() []error {
	var errs []error
	r := &c.RTDB
	r.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.Domain)), ".")
	if r.Domain != "" && !validDomain(r.Domain) {
		errs = append(errs, fmt.Errorf("rtdb.domain %q must be a DNS name such as rtdb.example.com, without scheme or port", r.Domain))
	}
	if r.DevOpenRules && (c.publicURL == nil || !c.loopback() || !loopbackListen(c.Listen)) {
		errs = append(errs, errors.New("rtdb.dev_open_rules lets anyone read and write every database; it is accepted only when public_url and listen are loopback addresses"))
	}
	if r.Domain != "" && c.publicURL != nil {
		// A host under rtdb.domain is routed to the Realtime Database before anything else
		// (ADR-018), so Aruma's own hosts must stay outside it.
		for name, host := range map[string]string{"public_url": c.publicURL.Hostname(), "project_domain": c.ProjectDomain} {
			if h := strings.TrimSuffix(strings.ToLower(host), "."); h == r.Domain || strings.HasSuffix(h, "."+r.Domain) {
				errs = append(errs, fmt.Errorf("%s %q lies under rtdb.domain %q, whose hosts all belong to the Realtime Database", name, host, r.Domain))
			}
		}
	}
	for i, o := range r.AllowedOrigins {
		r.AllowedOrigins[i] = strings.ToLower(strings.TrimSpace(o))
		if strings.Contains(o, "/") || r.AllowedOrigins[i] == "" {
			errs = append(errs, fmt.Errorf("rtdb.allowed_origins: %q must be a host name such as app.example.com", o))
		}
	}
	l := r.Limits
	if l.MessageBytes < 1<<20 || l.UnauthenticatedMessageBytes < 1<<10 || l.InflightBytes < 1<<20 ||
		l.RESTWriteBytes < 1<<20 || l.StringBytes < 1<<10 || l.WriteBytesPerMinute < 1<<20 ||
		l.ConnectionsPerProject < 1 || l.ConnectionsPerIP < 1 || l.OutboxBytes < 1<<20 {
		errs = append(errs, errors.New("rtdb.limits: byte limits must be at least 1 MiB (string_bytes and unauthenticated_message_bytes 1 KiB) and connection limits at least 1"))
	}
	if l.ReadRows < 1000 {
		errs = append(errs, errors.New("rtdb.limits.read_rows must be at least 1000"))
	}
	if l.StringBytes > l.MessageBytes {
		errs = append(errs, errors.New("rtdb.limits.string_bytes must not exceed message_bytes"))
	}
	if l.UnauthenticatedMessageBytes > l.MessageBytes {
		errs = append(errs, errors.New("rtdb.limits.unauthenticated_message_bytes must not exceed message_bytes"))
	}
	return errs
}

// validateDocuments checks the documents section: the two development settings need a loopback
// deployment, and the limits must be positive. The limits are checked whether or not the
// Document Database is on, since they cost nothing to get right.
func (c *Config) validateDocuments() []error {
	var errs []error
	d := c.Documents
	if d.DevOwnerToken && !c.LoopbackDeployment() {
		errs = append(errs, errors.New("documents.dev_owner_token makes any process on this machine an admin of every project; it is accepted only when public_url and listen are loopback addresses"))
	}
	if d.DevOpenRules && !c.LoopbackDeployment() {
		errs = append(errs, errors.New("documents.dev_open_rules lets anyone read and write every database; it is accepted only when public_url and listen are loopback addresses"))
	}
	l := d.Limits
	if l.MessageBytes < 1<<20 {
		errs = append(errs, errors.New("documents.limits.message_bytes must be at least 1 MiB"))
	}
	for _, f := range []struct {
		name string
		v    int64
	}{
		{"stream_first_message", int64(l.StreamFirstMessage)},
		{"stream_write_stall", int64(l.StreamWriteStall)},
		{"server_stream_max", int64(l.ServerStreamMax)},
		{"streams_per_ip", int64(l.StreamsPerIP)},
		{"commit_concurrency", int64(l.CommitConcurrency)},
		{"txn_wait_timeout", int64(l.TxnWaitTimeout)},
		{"txn_max_lifetime", int64(l.TxnMaxLifetime)},
		{"max_open_txns", int64(l.MaxOpenTxns)},
		{"max_open_unlocked_txns", int64(l.MaxOpenUnlockedTxns)},
		{"max_open_txns_per_user", int64(l.MaxOpenTxnsPerUser)},
		{"max_ended_txns", int64(l.MaxEndedTxns)},
		{"max_locks_per_txn", int64(l.MaxLocksPerTxn)},
	} {
		if f.v < 1 {
			errs = append(errs, fmt.Errorf("documents.limits.%s must be positive", f.name))
		}
	}
	return errs
}

// validateRules checks the rules section: rules.Limits.Validate refuses a limit below 1 and a
// Firebase limit above Firebase's value.
func (c *Config) validateRules() []error {
	lim := rules.Limits(c.Rules.Limits)
	err := lim.Validate()
	if err == nil {
		c.rulesLimits = lim
		return nil
	}
	var errs []error
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			errs = append(errs, fmt.Errorf("rules.limits.%w", e))
		}
		return errs
	}
	return []error{fmt.Errorf("rules.limits: %w", err)}
}

// validateStorage checks the storage section when Storage is enabled (ADR-022 §5.1, §17):
// data_dir, and the limits with their cross checks, as storage/core.Limits.Validate checks them.
// Disabled, nothing in the section matters, so nothing in it is refused.
func (c *Config) validateStorage() []error {
	var errs []error
	st := &c.Storage
	if !st.Enabled {
		return nil
	}
	switch {
	case st.DataDir == "":
		errs = append(errs, errors.New("storage.data_dir (ARUMA_STORAGE_DATA_DIR) is required when Storage is enabled: an absolute path on a local file system"))
	case !filepath.IsAbs(st.DataDir):
		errs = append(errs, fmt.Errorf("storage.data_dir %q must be an absolute path", st.DataDir))
	}
	if st.GCGrace <= 0 {
		errs = append(errs, errors.New("storage.gc_grace must be positive"))
	}
	l := st.Limits
	for _, f := range []struct {
		name string
		v    int64
	}{
		{"object_bytes", l.ObjectBytes},
		{"min_free_bytes", l.MinFreeBytes},
		{"empty_session_ttl", int64(l.EmptySessionTTL)},
		{"upload_idle_ttl", int64(l.UploadIdleTTL)},
		{"upload_session_ttl", int64(l.UploadSessionTTL)},
		{"console_session_ttl", int64(l.ConsoleSessionTTL)},
		{"metadata_bytes", int64(l.MetadataBytes)},
		{"json_body_bytes", l.JSONBodyBytes},
		{"header_field_bytes", int64(l.HeaderFieldBytes)},
		{"upload_sessions_per_project", int64(l.UploadSessionsPerProject)},
		{"upload_sessions_per_user", int64(l.UploadSessionsPerUser)},
		{"upload_sessions_per_ip", int64(l.UploadSessionsPerIP)},
		{"upload_starts_per_minute_per_ip", int64(l.UploadStartsPerMinutePerIP)},
		{"upload_starts_per_second", int64(l.UploadStartsPerSecond)},
		{"upload_requests_per_ip", int64(l.UploadRequestsPerIP)},
		{"min_bytes_per_second", l.MinBytesPerSecond},
		{"list_max_results", int64(l.ListMaxResults)},
	} {
		if f.v < 1 {
			errs = append(errs, fmt.Errorf("storage.limits.%s must be positive", f.name))
		}
	}
	if l.ObjectBytes > maxStorageObjectBytes {
		errs = append(errs, errors.New("storage.limits.object_bytes must not exceed 5 TiB, the largest object GCS stores"))
	}
	if l.JSONBodyBytes > maxStorageJSONBodyBytes {
		errs = append(errs, errors.New("storage.limits.json_body_bytes must not exceed 1 MiB"))
	}
	if l.ProjectBytes < 0 {
		errs = append(errs, errors.New("storage.limits.project_bytes must not be negative (0 is unlimited)"))
	}
	if l.ProjectBytes > 0 && l.ObjectBytes > l.ProjectBytes {
		errs = append(errs, errors.New("storage.limits.object_bytes must not exceed project_bytes"))
	}
	if !(l.EmptySessionTTL < l.UploadIdleTTL && l.UploadIdleTTL < l.UploadSessionTTL) {
		errs = append(errs, errors.New("storage.limits: empty_session_ttl, upload_idle_ttl and upload_session_ttl must increase in that order"))
	}
	if l.ConsoleSessionTTL > l.UploadSessionTTL {
		errs = append(errs, errors.New("storage.limits.console_session_ttl must not exceed upload_session_ttl"))
	}
	if !(l.AnonymousStagedFraction >= 0 && l.AnonymousStagedFraction <= 1) {
		errs = append(errs, errors.New("storage.limits.anonymous_staged_fraction must lie between 0 and 1"))
	}
	if l.ListMaxResults > 1000 {
		errs = append(errs, errors.New("storage.limits.list_max_results must not exceed 1000"))
	}
	return errs
}

// loopbackListen reports whether the listen address accepts connections from this machine
// only.
func loopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// validDomain reports whether d is a lower-case DNS name of at least two labels.
func validDomain(d string) bool {
	labels := strings.Split(d, ".")
	if len(labels) < 2 || len(d) > 253 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// MasterKeyBytes returns the decoded master key.
func (c *Config) MasterKeyBytes() []byte { return c.masterKey }

// PublicBaseURL returns the parsed public URL, without a trailing slash.
func (c *Config) PublicBaseURL() *url.URL { u := *c.publicURL; return &u }

// TrustedProxyPrefixes returns the parsed trusted proxy ranges.
func (c *Config) TrustedProxyPrefixes() []netip.Prefix { return c.trustedProxies }

// RulesEngineLimits returns the validated rules.limits as the rules engine takes them.
func (c *Config) RulesEngineLimits() rules.Limits { return c.rulesLimits }

// SecureCookies reports whether cookies must carry the Secure attribute: always over https,
// and on loopback hosts, which browsers treat as secure contexts.
func (c *Config) SecureCookies() bool {
	if c.publicURL.Scheme == "https" {
		return true
	}
	return c.loopback()
}

// DevMailbox reports whether the dev mailbox is allowed: as configured, else on loopback only.
func (c *Config) DevMailbox() bool {
	if c.Mail.DevMailbox != nil {
		return *c.Mail.DevMailbox
	}
	return c.loopback()
}

// SMTPAllowPrivate reports whether projects may send through SMTP hosts on loopback, private
// or link-local addresses: as configured, else on loopback only, where the network is the
// developer's own.
func (c *Config) SMTPAllowPrivate() bool {
	if c.Mail.SMTPAllowPrivate != nil {
		return *c.Mail.SMTPAllowPrivate
	}
	return c.loopback()
}

// LoopbackDeployment reports whether public_url and listen are both loopback addresses, so
// nothing reaches the server from another machine: the development server of ADR-029 §3.7 and
// §6. False until public_url has been validated.
func (c *Config) LoopbackDeployment() bool {
	return c.publicURL != nil && c.loopback() && loopbackListen(c.Listen)
}

func (c *Config) loopback() bool {
	host := c.publicURL.Hostname()
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
