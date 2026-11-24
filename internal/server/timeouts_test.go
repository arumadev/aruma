package server

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/metrics"
)

// The public listener takes its timeouts from the configuration: by default the values it had
// when they were fixed.
func TestPublicServerTimeouts(t *testing.T) {
	for _, c := range []struct {
		env                       map[string]string
		header, read, write, idle time.Duration
	}{
		{nil, 10 * time.Second, 60 * time.Second, 60 * time.Second, 120 * time.Second},
		{map[string]string{
			"ARUMA_SERVER_READ_HEADER_TIMEOUT": "5s", "ARUMA_SERVER_READ_TIMEOUT": "15s",
			"ARUMA_SERVER_WRITE_TIMEOUT": "45s", "ARUMA_SERVER_IDLE_TIMEOUT": "10m",
		}, 5 * time.Second, 15 * time.Second, 45 * time.Second, 10 * time.Minute},
	} {
		vars := map[string]string{
			"ARUMA_DATABASE_URL": "postgres://unused",
			"ARUMA_PUBLIC_URL":   "https://aruma.test",
			"ARUMA_MASTER_KEY":   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}
		for k, v := range c.env {
			vars[k] = v
		}
		cfg, err := config.Load("", func(k string) string { return vars[k] })
		if err != nil {
			t.Fatal(err)
		}
		srv := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New("test"), Handlers{}).publicServer()
		if srv.ReadHeaderTimeout != c.header || srv.ReadTimeout != c.read || srv.WriteTimeout != c.write || srv.IdleTimeout != c.idle {
			t.Errorf("%v: timeouts %s %s %s %s, want %s %s %s %s", c.env, srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout,
				srv.IdleTimeout, c.header, c.read, c.write, c.idle)
		}
	}
}
