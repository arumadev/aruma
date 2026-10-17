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

	masterKey      []byte
	publicURL      *url.URL
	trustedProxies []netip.Prefix
}

// Session holds console session lifetimes.
type Session struct {
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	AbsoluteTimeout time.Duration `yaml:"absolute_timeout"`
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

	if c.Session.IdleTimeout <= 0 || c.Session.AbsoluteTimeout <= 0 {
		errs = append(errs, errors.New("session timeouts must be positive"))
	} else if c.Session.IdleTimeout > c.Session.AbsoluteTimeout {
		errs = append(errs, errors.New("session.idle_timeout must not exceed session.absolute_timeout"))
	}
	return errors.Join(errs...)
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
	host := c.publicURL.Hostname()
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
