//go:build pprof

package server

import (
	"bytes"
	"net/http"
	httppprof "net/http/pprof"
	"runtime/pprof"
	"sync"
)

// With the pprof build tag, the private metrics_listen address also serves Go's profiler, for
// the benchmarks (tools/bench -rtdb-cpuprofile). Release builds never carry it.
func mountPprof(mux *http.ServeMux) {
	mux.HandleFunc("GET /debug/pprof/", httppprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", httppprof.Trace)
	// A CPU profile that a caller starts and stops, so it covers exactly one measurement.
	mux.HandleFunc("POST /debug/pprof/cpu/start", cpuProfile.start)
	mux.HandleFunc("POST /debug/pprof/cpu/stop", cpuProfile.stop)
}

var cpuProfile cpuRecorder

type cpuRecorder struct {
	mu      sync.Mutex
	buf     *bytes.Buffer
	running bool
}

func (c *cpuRecorder) start(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		http.Error(w, "a CPU profile is already running", http.StatusConflict)
		return
	}
	c.buf = &bytes.Buffer{}
	if err := pprof.StartCPUProfile(c.buf); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	c.running = true
	w.WriteHeader(http.StatusNoContent)
}

func (c *cpuRecorder) stop(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		http.Error(w, "no CPU profile is running", http.StatusConflict)
		return
	}
	pprof.StopCPUProfile()
	c.running = false
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(c.buf.Bytes())
	c.buf = nil
}
