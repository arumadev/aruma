// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"errors"
	"fmt"
	"reflect"

	"firebase.google.com/go/v4/auth"
)

// auth.UserToCreate and auth.UserToUpdate keep what their setters were given in an unexported
// map named params, with the Identity Toolkit field names as keys. Accepting the official
// types keeps the call sites unchanged, so the library reads that map by reflection, read
// only: no unsafe, nothing written. A self-test fails NewClient's callers loudly if a
// firebase-admin-go release renames it.

// params returns a copy of the setters' values of a *auth.UserToCreate or *auth.UserToUpdate.
func params(v any) (map[string]any, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return map[string]any{}, nil
	}
	f := rv.Elem().FieldByName("params")
	if !f.IsValid() || f.Kind() != reflect.Map {
		return nil, errors.New("adminauth: this firebase-admin-go version is not supported (user parameters)")
	}
	out := map[string]any{}
	iter := f.MapRange()
	for iter.Next() {
		val, err := plain(iter.Value())
		if err != nil {
			return nil, fmt.Errorf("adminauth: user parameter %q: %w", iter.Key().String(), err)
		}
		out[iter.Key().String()] = val
	}
	return out, nil
}

// plain copies a value reached through an unexported field, which Interface() refuses, into
// ordinary Go values: strings, bools, numbers, maps and slices of them.
func plain(v reflect.Value) (any, error) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return nil, nil
		}
		return plain(v.Elem())
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("unsupported map key %s", v.Type().Key())
		}
		if v.IsNil() {
			return map[string]any(nil), nil
		}
		m := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			x, err := plain(iter.Value())
			if err != nil {
				return nil, err
			}
			m[iter.Key().String()] = x
		}
		return m, nil
	case reflect.Slice, reflect.Array:
		s := make([]any, v.Len())
		for i := range s {
			x, err := plain(v.Index(i))
			if err != nil {
				return nil, err
			}
			s[i] = x
		}
		return s, nil
	case reflect.Struct:
		// Provider links and multi-factor settings: not supported by Aruma yet, refused by the
		// callers, so only their presence matters.
		return unsupported{v.Type().String()}, nil
	}
	return nil, fmt.Errorf("unsupported value of kind %s", v.Kind())
}

// unsupported marks a parameter Aruma does not take.
type unsupported struct{ what string }

// checkParams verifies once that the official setters land where params reads them.
func checkParams() error {
	p, err := params((&auth.UserToUpdate{}).Email("a@example.com").CustomClaims(map[string]interface{}{"n": 1}))
	if err != nil {
		return err
	}
	claims, _ := p["customClaims"].(map[string]any)
	if p["email"] != "a@example.com" || claims["n"] != int64(1) {
		return errors.New("adminauth: this firebase-admin-go version is not supported (user parameters)")
	}
	return nil
}
