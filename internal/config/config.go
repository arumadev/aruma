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
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/osama300/aurma-project/internal/platform/log"
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
	// TrustedProxies lists the CIDRs whose X-Forwarded-For header is believed.
	TrustedProxies []string `yaml:"trusted_proxies"`

	Session Session `yaml:"session"`
	Mail    Mail    `yaml:"mail"`
	RTDB    RTDB    `yaml:"rtdb"`

	masterKey      []byte
	publicURL      *url.URL
	trustedProxies []netip.Prefix
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
	// (ADR-020). Until the rules engine lands there is nothing in between, so the setting is
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
		}},
	}
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
	conns := int64(lim.ConnectionsPerProject)
	integer("ARUMA_RTDB_CONNECTIONS_PER_PROJECT", &conns)
	lim.ConnectionsPerProject = int(conns)
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
	if l.StringBytes > l.MessageBytes {
		errs = append(errs, errors.New("rtdb.limits.string_bytes must not exceed message_bytes"))
	}
	if l.UnauthenticatedMessageBytes > l.MessageBytes {
		errs = append(errs, errors.New("rtdb.limits.unauthenticated_message_bytes must not exceed message_bytes"))
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

func (c *Config) loopback() bool {
	host := c.publicURL.Hostname()
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
