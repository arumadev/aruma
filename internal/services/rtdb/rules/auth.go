package rules

import (
	"fmt"
	"strings"
)

// Auth is who makes a request, as rules see it. The zero value is an unauthenticated client.
type Auth struct {
	admin bool
	// variable is the auth variable: nil when unauthenticated, otherwise a normalized object.
	variable map[string]any
}

// Admin is a service account or the project owner without an auth override: rules do not
// apply to it at all, .validate included, as for Firebase's Admin SDKs.
func Admin() Auth { return Auth{admin: true} }

// Unauthenticated is a client without a token: auth is null.
func Unauthenticated() Auth { return Auth{} }

// User is a client signed in with an ID token: auth.uid is uid, auth.token holds the token's
// claims (sub, email, firebase.sign_in_provider, custom claims...), as verified, and
// auth.provider is the sign-in method the claims name (Provider). As on the emulator 4.11.2,
// auth.user_id repeats the token's user_id claim when it has one.
func User(uid string, claims map[string]any) (Auth, error) {
	token, err := normalizeObject(claims)
	if err != nil {
		return Auth{}, err
	}
	if token == nil {
		token = map[string]any{}
	}
	v := map[string]any{"uid": uid, "token": token, "provider": Provider(claims)}
	if id, ok := token["user_id"].(string); ok {
		v["user_id"] = id
	}
	return Auth{variable: v}, nil
}

// Provider is auth.provider for a token's claims: firebase.sign_in_provider without ".com"
// ("google.com" is "google"; "password", "phone", "custom" stay as they are), and "anonymous"
// for a token that names none, as the emulator 4.11.2 answers (golden auth_user.json in package
// wire).
func Provider(claims map[string]any) string {
	fb, _ := claims["firebase"].(map[string]any)
	p, _ := fb["sign_in_provider"].(string)
	if p == "" {
		return "anonymous"
	}
	return strings.TrimSuffix(p, ".com")
}

// Value returns the auth variable as rules see it: nil for an admin or an unauthenticated
// client. Callers must not change it.
func (a Auth) Value() map[string]any { return a.variable }

// Variable makes auth exactly v: what an Admin SDK sends as auth_variable_override. A nil v is
// an unauthenticated client.
func Variable(v map[string]any) (Auth, error) {
	if v == nil {
		return Auth{}, nil
	}
	obj, err := normalizeObject(v)
	if err != nil {
		return Auth{}, err
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return Auth{variable: obj}, nil
}

// IsAdmin reports an Auth that bypasses rules.
func (a Auth) IsAdmin() bool { return a.admin }

// authValue converts claims as encoding/json decodes them, keeping arrays as arrays: in auth,
// unlike in stored data, an array is indexed but has no length (emulator 4.11.2: arr[0] reads,
// arr.length is null).
func authValue(v any, depth int) (any, error) {
	if depth > maxValueDepth {
		return nil, fmt.Errorf("rules: auth nested deeper than %d levels", maxValueDepth)
	}
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, c := range v {
			n, err := authValue(c, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, c := range v {
			n, err := authValue(c, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	}
	return normalize(v, depth)
}

func normalizeObject(m map[string]any) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	v, err := authValue(m, 0)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}
