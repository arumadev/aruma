// Package httpx holds the HTTP plumbing shared by every API: a router for Google-style paths,
// JSON helpers, client address resolution and common middleware.
package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// Router matches Google API style paths, which net/http.ServeMux cannot express:
//
//	/v1beta1/projects/{project}                 a whole segment
//	/v1beta1/projects/{project}:searchApps      a custom method on the last segment
//	/v1beta1/operations/{name...}               the rest of the path
//
// Values are path-unescaped and exposed through Request.PathValue. Routes with a custom method
// take precedence, so "projects/p:searchApps" never reaches the "projects/{project}" route,
// while app ids, which contain colons ("1:123:web:abc"), still match plain wildcards.
type Router struct {
	routes []route
	// NotFound handles requests no route matches. Defaults to http.NotFound.
	NotFound http.Handler
	// MethodNotAllowed handles a matching path with another method. Defaults to 405.
	MethodNotAllowed http.Handler
}

type route struct {
	method  string
	pattern string
	segs    []segment
	verb    string
	rest    bool
	handler http.Handler
}

type segment struct {
	literal string
	param   string // empty for literals
}

// Handle registers h for method and pattern. It panics on a malformed pattern, as ServeMux does.
func (r *Router) Handle(method, pattern string, h http.Handler) {
	rt := route{method: method, pattern: pattern, handler: h}
	parts := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	for i, p := range parts {
		last := i == len(parts)-1
		switch {
		case strings.HasPrefix(p, "{") && strings.HasSuffix(p, "...}"):
			if !last {
				panic("httpx: {name...} must be the last segment: " + pattern)
			}
			rt.segs = append(rt.segs, segment{param: p[1 : len(p)-4]})
			rt.rest = true
		case strings.HasPrefix(p, "{"):
			end := strings.Index(p, "}")
			if end < 0 {
				panic("httpx: unclosed wildcard: " + pattern)
			}
			if tail := p[end+1:]; tail != "" {
				if !last || !strings.HasPrefix(tail, ":") || len(tail) < 2 {
					panic("httpx: only the last segment may carry a :verb: " + pattern)
				}
				rt.verb = tail[1:]
			}
			rt.segs = append(rt.segs, segment{param: p[1:end]})
		default:
			if last {
				if lit, verb, ok := strings.Cut(p, ":"); ok {
					rt.segs = append(rt.segs, segment{literal: lit})
					rt.verb = verb
					continue
				}
			}
			rt.segs = append(rt.segs, segment{literal: p})
		}
	}
	r.routes = append(r.routes, rt)
}

// HandleFunc registers a handler function.
func (r *Router) HandleFunc(method, pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.Handle(method, pattern, http.HandlerFunc(h))
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	segs, ok := splitPath(req.URL.EscapedPath())
	if !ok {
		r.notFound(w, req)
		return
	}
	pathMatched := false
	// Custom-method routes first, then plain ones.
	for _, wantVerb := range []bool{true, false} {
		for i := range r.routes {
			rt := &r.routes[i]
			if (rt.verb != "") != wantVerb {
				continue
			}
			params, ok := rt.match(segs)
			if !ok {
				continue
			}
			if rt.method != req.Method && !(rt.method == http.MethodGet && req.Method == http.MethodHead) {
				pathMatched = true
				continue
			}
			for k, v := range params {
				req.SetPathValue(k, v)
			}
			req.Pattern = rt.method + " " + rt.pattern
			rt.handler.ServeHTTP(w, req)
			return
		}
	}
	if pathMatched {
		if r.MethodNotAllowed != nil {
			r.MethodNotAllowed.ServeHTTP(w, req)
			return
		}
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	r.notFound(w, req)
}

func (r *Router) notFound(w http.ResponseWriter, req *http.Request) {
	if r.NotFound != nil {
		r.NotFound.ServeHTTP(w, req)
		return
	}
	http.NotFound(w, req)
}

func (rt *route) match(segs []string) (map[string]string, bool) {
	if rt.rest {
		if len(segs) < len(rt.segs) {
			return nil, false
		}
	} else if len(segs) != len(rt.segs) {
		return nil, false
	}
	params := map[string]string{}
	for i, s := range rt.segs {
		v := segs[i]
		last := i == len(rt.segs)-1
		if last && rt.rest {
			params[s.param] = strings.Join(segs[i:], "/")
			if params[s.param] == "" {
				return nil, false
			}
			return params, true
		}
		if last && rt.verb != "" {
			base, ok := strings.CutSuffix(v, ":"+rt.verb)
			if !ok {
				return nil, false
			}
			v = base
		}
		if s.param == "" {
			if v != s.literal {
				return nil, false
			}
			continue
		}
		if v == "" {
			return nil, false
		}
		params[s.param] = v
	}
	return params, true
}

// splitPath splits an escaped path into unescaped segments. A trailing slash is ignored.
func splitPath(escaped string) ([]string, bool) {
	p := strings.TrimSuffix(strings.TrimPrefix(escaped, "/"), "/")
	raw := strings.Split(p, "/")
	out := make([]string, len(raw))
	for i, s := range raw {
		u, err := url.PathUnescape(s)
		if err != nil || strings.Contains(u, "/") {
			return nil, false
		}
		out[i] = u
	}
	return out, true
}

// RouteInfo describes a registered route.
type RouteInfo struct {
	Method  string
	Pattern string
}

// Routes lists the registered routes in registration order.
func (r *Router) Routes() []RouteInfo {
	out := make([]RouteInfo, len(r.routes))
	for i, rt := range r.routes {
		out[i] = RouteInfo{Method: rt.method, Pattern: rt.pattern}
	}
	return out
}
