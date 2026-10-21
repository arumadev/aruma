package api

import (
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/arumadev/aruma/internal/services/rtdb/core"
)

// REST queries: orderBy names the index as a JSON string ("$key", "$value", "$priority" or a
// child path), and the bounds are JSON values. They read into the same core.QuerySpec as the
// protocol's query object, so a REST read selects what an SDK's get of the same query does.
// The error texts are the emulator 4.11.2's (testdata/emulator/rest.golden.json).

// queryParams are the parameters that make a REST request a query.
var queryParams = []string{"orderBy", "limitToFirst", "limitToLast", "startAt", "startAfter", "endAt", "endBefore", "equalTo"}

// hasQuery reports whether the request carries any query parameter.
func hasQuery(q url.Values) bool {
	for _, k := range queryParams {
		if q.Has(k) {
			return true
		}
	}
	return false
}

// parseQuery reads the query parameters; nil when there are none.
func parseQuery(q url.Values) (*core.QuerySpec, error) {
	if !hasQuery(q) {
		return nil, nil
	}
	if !q.Has("orderBy") {
		return nil, restErr(400, "orderBy must be defined when other query parameters are defined")
	}
	var spec core.QuerySpec
	var order string
	if err := json.Unmarshal([]byte(q.Get("orderBy")), &order); err != nil {
		return nil, restErr(400, "orderBy must be a valid JSON encoded path")
	}
	switch order {
	case "$key":
		spec.Index = ".key"
	case "$value":
		spec.Index = ".value"
	case "$priority":
		spec.Index = ".priority"
	default:
		if p, err := core.ParsePath(order); err != nil || p.IsRoot() {
			return nil, restErr(400, "orderBy must be a valid JSON encoded path")
		}
		spec.Index = order
	}

	for _, name := range []string{"limitToFirst", "limitToLast"} {
		if !q.Has(name) {
			continue
		}
		n, err := strconv.ParseInt(q.Get(name), 10, 32)
		switch {
		case err != nil:
			return nil, restErr(400, name+" must be an integer")
		case n <= 0:
			return nil, restErr(400, name+" must be positive")
		case spec.Limit > 0:
			return nil, restErr(400, "Only one of limitToFirst and limitToLast may be specified")
		}
		spec.Limit, spec.FromRight = n, name == "limitToLast"
	}

	bound := func(name string) (json.RawMessage, error) {
		if !q.Has(name) {
			return nil, nil
		}
		raw := json.RawMessage(q.Get(name))
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			if spec.Index == ".key" {
				return nil, restErr(400, "Constraint key field must be a valid key name")
			}
			return nil, restErr(400, "Constraint index field must be a JSON primitive")
		}
		switch v.(type) {
		case map[string]any, []any:
			return nil, restErr(400, "Constraint index field must be a JSON primitive")
		case string:
		default:
			if spec.Index == ".key" {
				return nil, restErr(400, "Provided key index type is invalid, must be string")
			}
		}
		return raw, nil
	}
	values := map[string]json.RawMessage{}
	for _, name := range []string{"startAt", "startAfter", "endAt", "endBefore", "equalTo"} {
		v, err := bound(name)
		if err != nil {
			return nil, err
		}
		if v != nil {
			values[name] = v
		}
	}
	if values["equalTo"] != nil {
		if len(values) > 1 {
			return nil, restErr(400, "equalTo cannot be specified in addition to startAfter, startAt, endAt, or endBefore")
		}
		spec.StartValue, spec.EndValue = values["equalTo"], values["equalTo"]
		return &spec, nil
	}
	if values["startAt"] != nil && values["startAfter"] != nil {
		return nil, restErr(400, "startAt and startAfter cannot be both specified")
	}
	if values["endAt"] != nil && values["endBefore"] != nil {
		return nil, restErr(400, "endAt and endBefore cannot be both specified")
	}
	// startAfter and endBefore exclude the value itself, as the SDKs send them: the start
	// after every key, or the end before every key, of that value, and exclusive for keys.
	exclusive, maxName, minName := false, core.MaxName, core.MinName
	switch {
	case values["startAt"] != nil:
		spec.StartValue = values["startAt"]
	case values["startAfter"] != nil:
		spec.StartValue, spec.StartName, spec.StartInclusive = values["startAfter"], &maxName, &exclusive
	}
	switch {
	case values["endAt"] != nil:
		spec.EndValue = values["endAt"]
	case values["endBefore"] != nil:
		spec.EndValue, spec.EndName, spec.EndInclusive = values["endBefore"], &minName, &exclusive
	}
	return &spec, nil
}
