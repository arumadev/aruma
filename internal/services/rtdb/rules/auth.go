package rules

import "fmt"

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

// User is a client signed in with an ID token: auth.uid is uid and auth.token holds the
// token's claims (sub, email, firebase.sign_in_provider, custom claims...), as verified.
// auth.provider is refused when rules load, so it is not set.
func User(uid string, claims map[string]any) (Auth, error) {
	token, err := normalizeObject(claims)
	if err != nil {
		return Auth{}, err
	}
	if token == nil {
		token = map[string]any{}
	}
	return Auth{variable: map[string]any{"uid": uid, "token": token}}, nil
}

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
