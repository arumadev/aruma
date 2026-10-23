package rules

// Auth returns request.auth for a signed-in user: a map of uid and token, the token's claims
// converted to values, whole numbers (exp, iat, auth_time…) as ints and other numbers as
// floats. A request without a user has Null() instead. S1-B replaces this stub.
func Auth(uid string, token map[string]any) Value {
	panic("S1-B: Auth")
}
