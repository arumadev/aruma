// Package consolemount is the seam through which a service adds its screens' routes to the
// console API (/console/v1/). The console owns authentication, CSRF, routing and the route
// list; a service only says which routes it has and what they do, so its handlers can live
// with the service instead of in the console package.
package consolemount

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/platform/authn"
)

// ProjectRef is the project a console request acts on.
type ProjectRef struct {
	Number int64
	ID     string
}

// Mount is what the console hands each service while it builds its router.
type Mount struct {
	// Handle registers a route that requires credentials accepted by p. It is listed in the
	// console's routes like the console's own.
	Handle func(p authn.Policy, method, pattern string, h http.HandlerFunc)
	// Project resolves the {project} path value for the caller. It fails, with an error ready
	// for Fail, when the project does not exist or the caller does not own it.
	Project func(r *http.Request) (ProjectRef, error)
	// Fail writes err as a google.rpc.Status, logging it when it is an internal error.
	Fail func(w http.ResponseWriter, r *http.Request, err error)
}
