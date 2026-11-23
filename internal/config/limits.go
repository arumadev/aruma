package config

import (
	"errors"
	"fmt"
	"time"
)

// Server holds the public listener's HTTP timeouts. They bound ordinary requests. The streams
// that live longer lift or renew their own deadlines once established (http.ResponseController):
// the Realtime Database's WebSockets, the Document Database's gRPC and WebChannel streams, and
// Storage's uploads and downloads, which keep moving at storage.limits.min_bytes_per_second; so
// these values never cut a healthy stream. The private metrics listener keeps fixed timeouts.
type Server struct {
	// ReadHeaderTimeout bounds the wait for a request's headers, so that a client that opens a
	// connection and sends nothing does not hold it.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	// ReadTimeout bounds reading a request, body included, unless its handler renews it.
	ReadTimeout time.Duration `yaml:"read_timeout"`
	// WriteTimeout bounds a response, from the end of the request's headers, unless its handler
	// renews it. A Realtime Database long poll is held for 20 seconds inside it.
	WriteTimeout time.Duration `yaml:"write_timeout"`
	// IdleTimeout closes a keep-alive connection that carries no request for this long.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
}

// Auth holds the instance-wide settings of Auth. Everything else about Auth is set per project
// in the console.
type Auth struct {
	// Limits are the rate limits on the client calls, the same for every project.
	Limits AuthLimits `yaml:"limits"`
}

// AuthLimits are Auth's rate limits per client address (resolved through trusted_proxies). Each
// is a token bucket: it holds up to its burst and refills at its rate per minute. The growing
// delay after wrong passwords, per project and email, and the email sending limit are fixed.
type AuthLimits struct {
	// RequestsPerMinute refills the bucket every client call takes from.
	RequestsPerMinute int `yaml:"requests_per_minute"`
	// RequestBurst is the size of that bucket.
	RequestBurst int `yaml:"request_burst"`
	// SignUpsPerMinute refills the bucket account creation takes from.
	SignUpsPerMinute int `yaml:"sign_ups_per_minute"`
	// SignUpBurst is the size of that bucket.
	SignUpBurst int `yaml:"sign_up_burst"`
}

// Bounds of the server timeouts. The write timeout holds a Realtime Database long poll (20
// seconds) with room to answer it.
const (
	minReadHeaderTimeout = time.Second
	minReadTimeout       = 10 * time.Second
	minWriteTimeout      = 30 * time.Second
	minIdleTimeout       = 5 * time.Second
	maxServerTimeout     = time.Hour
)

// validateServer checks the server section.
func (c *Config) validateServer() []error {
	var errs []error
	s := c.Server
	for _, f := range []struct {
		name  string
		v, lo time.Duration
	}{
		{"read_header_timeout", s.ReadHeaderTimeout, minReadHeaderTimeout},
		{"read_timeout", s.ReadTimeout, minReadTimeout},
		{"write_timeout", s.WriteTimeout, minWriteTimeout},
		{"idle_timeout", s.IdleTimeout, minIdleTimeout},
	} {
		if f.v < f.lo || f.v > maxServerTimeout {
			errs = append(errs, fmt.Errorf("server.%s must be between %s and %s", f.name, f.lo, maxServerTimeout))
		}
	}
	if s.ReadHeaderTimeout > s.ReadTimeout {
		errs = append(errs, errors.New("server.read_header_timeout must not exceed server.read_timeout"))
	}
	return errs
}

// maxAuthRate bounds the Auth rate limits far above any real one, so that a typo is caught.
const maxAuthRate = 1_000_000

// validateAuth checks the auth section: every limit is a positive count.
func (c *Config) validateAuth() []error {
	var errs []error
	l := c.Auth.Limits
	for _, f := range []struct {
		name string
		v    int
	}{
		{"requests_per_minute", l.RequestsPerMinute},
		{"request_burst", l.RequestBurst},
		{"sign_ups_per_minute", l.SignUpsPerMinute},
		{"sign_up_burst", l.SignUpBurst},
	} {
		if f.v < 1 || f.v > maxAuthRate {
			errs = append(errs, fmt.Errorf("auth.limits.%s must be between 1 and %d", f.name, maxAuthRate))
		}
	}
	return errs
}
