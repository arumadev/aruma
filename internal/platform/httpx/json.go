package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/osama300/aurma-project/internal/platform/apierr"
)

// MaxBodyBytes caps request bodies on every API (ADR-008).
const MaxBodyBytes = 1 << 20

// WriteJSON writes v as a JSON response. API responses are never cached.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON reads a JSON request body into v. An empty body leaves v untouched. strict
// rejects unknown fields; compatible APIs pass false, since Google clients may send fields
// Aruma does not implement.
func DecodeJSON(r *http.Request, v any, strict bool) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			return apierr.New(apierr.InvalidArgument, "Content-Type must be application/json.")
		}
	}
	dec := json.NewDecoder(r.Body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return apierr.PayloadTooLarge()
		}
		return apierr.New(apierr.InvalidArgument, "Invalid JSON payload received. %s", describe(err))
	}
	if dec.More() {
		return apierr.New(apierr.InvalidArgument, "Invalid JSON payload received. Unexpected data after the object.")
	}
	return nil
}

func describe(err error) string {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("Syntax error at offset %d.", syntax.Offset)
	case errors.As(err, &typ):
		return fmt.Sprintf("Invalid value for %q.", typ.Field)
	default:
		return err.Error()
	}
}
