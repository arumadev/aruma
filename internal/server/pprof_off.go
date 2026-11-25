//go:build !pprof

package server

import "net/http"

// mountPprof is empty without the pprof build tag (pprof.go).
func mountPprof(*http.ServeMux) {}
