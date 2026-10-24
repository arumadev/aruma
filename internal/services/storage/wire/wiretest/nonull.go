// Package wiretest holds the checks the surfaces' tests run on what they send: AssertNoNull on
// every /v0 reply (ADR-022 §12). It imports wire's standard library alone, and testing.
package wiretest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"testing"
)

// AssertNoNull fails t when body, a JSON reply, holds a null anywhere: Android reads a null as
// the string "null" (A §1.8). An empty body passes (a start reply has none, A §3.10); a body
// that is not JSON fails, since a /v0 reply that says JSON must be.
func AssertNoNull(t testing.TB, body []byte) {
	t.Helper()
	if path, err := FindNull(body); err != nil {
		t.Errorf("not JSON: %v: %s", err, body)
	} else if path != "" {
		t.Errorf("a null at %s: %s", path, body)
	}
}

// FindNull returns where the first null of a JSON body is, as a path such as
// $.items[2].bucket, or "" when there is none. An empty body has none.
func FindNull(body []byte) (string, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	path, err := findNull(dec, "$")
	if err != nil || path != "" {
		return path, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("data after the JSON value")
	}
	return path, nil
}

// findNull walks one value, whose token comes next from dec.
func findNull(dec *json.Decoder, at string) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	switch tok {
	case nil:
		return at, nil
	case json.Delim('{'):
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return "", err
			}
			key, _ := k.(string)
			if p, err := findNull(dec, at+"."+key); err != nil || p != "" {
				return p, err
			}
		}
		_, err = dec.Token() // }
		return "", err
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			if p, err := findNull(dec, at+"["+strconv.Itoa(i)+"]"); err != nil || p != "" {
				return p, err
			}
		}
		_, err = dec.Token() // ]
		return "", err
	}
	return "", nil
}
