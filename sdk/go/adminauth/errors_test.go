// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"fmt"
	"testing"

	"firebase.google.com/go/v4/auth"
	"firebase.google.com/go/v4/errorutils"
)

// The plan's open question: can a separate module return errors the official helpers
// recognise? Yes, with the captured type; and only unwrapped.
func TestOfficialHelpersRecogniseErrors(t *testing.T) {
	if _, err := firebaseErrorType(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		err  error
		is   func(error) bool
		name string
	}{
		{newError(platformInvalidArgument, codeIDTokenExpired, "x"), auth.IsIDTokenExpired, "IsIDTokenExpired"},
		{newError(platformInvalidArgument, codeIDTokenExpired, "x"), auth.IsIDTokenInvalid, "IsIDTokenInvalid (expired)"},
		{newError(platformInvalidArgument, codeIDTokenInvalid, "x"), auth.IsIDTokenInvalid, "IsIDTokenInvalid"},
		{newError(platformInvalidArgument, codeIDTokenRevoked, "x"), auth.IsIDTokenRevoked, "IsIDTokenRevoked"},
		{newError(platformInvalidArgument, codeSessionExpired, "x"), auth.IsSessionCookieExpired, "IsSessionCookieExpired"},
		{newError(platformInvalidArgument, codeSessionInvalid, "x"), auth.IsSessionCookieInvalid, "IsSessionCookieInvalid"},
		{newError(platformInvalidArgument, codeSessionRevoked, "x"), auth.IsSessionCookieRevoked, "IsSessionCookieRevoked"},
		{newError(platformInvalidArgument, codeUserDisabled, "x"), auth.IsUserDisabled, "IsUserDisabled"},
		{newError(platformUnknown, codeCertificateFetch, "x"), auth.IsCertificateFetchFailed, "IsCertificateFetchFailed"},
		{responseError(400, "USER_NOT_FOUND"), auth.IsUserNotFound, "IsUserNotFound"},
		{responseError(400, "USER_NOT_FOUND"), errorutils.IsNotFound, "errorutils.IsNotFound"},
		{responseError(400, "EMAIL_EXISTS"), auth.IsEmailAlreadyExists, "IsEmailAlreadyExists"},
		{responseError(400, "DUPLICATE_LOCAL_ID"), auth.IsUIDAlreadyExists, "IsUIDAlreadyExists"},
		{responseError(400, "DUPLICATE_LOCAL_ID"), errorutils.IsAlreadyExists, "errorutils.IsAlreadyExists"},
		{responseError(400, "INVALID_DURATION"), errorutils.IsInvalidArgument, "errorutils.IsInvalidArgument"},
		{responseError(403, "The caller does not have permission."), errorutils.IsPermissionDenied, "errorutils.IsPermissionDenied"},
		{responseError(401, ""), errorutils.IsUnauthenticated, "errorutils.IsUnauthenticated"},
	}
	for _, c := range cases {
		if !c.is(c.err) {
			t.Errorf("%s does not recognise %v", c.name, c.err)
		}
	}
	if auth.IsIDTokenRevoked(newError(platformInvalidArgument, codeIDTokenExpired, "x")) {
		t.Error("an expired error passes for revoked")
	}
	if auth.IsIDTokenExpired(fmt.Errorf("wrapped: %w", newError(platformInvalidArgument, codeIDTokenExpired, "x"))) {
		t.Error("the official helpers were expected not to unwrap; the library must return errors unwrapped")
	}
	if got := responseError(400, "USER_NOT_FOUND : no such uid").Error(); got != "no user record found for the given identifier: no such uid" {
		t.Errorf("message %q", got)
	}
	if got := responseError(400, "CLAIMS_TOO_LARGE").Error(); got != "CLAIMS_TOO_LARGE" {
		t.Errorf("unmapped codes keep the server message, as firebase-admin-go: %q", got)
	}
}
