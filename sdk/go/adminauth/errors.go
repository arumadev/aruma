// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
)

// The official helpers (auth.IsIDTokenExpired, auth.IsUserNotFound, errorutils.IsNotFound...)
// type-assert *internal.FirebaseError, a type no other module can name. The library captures
// that type from an error the official client produces offline, and builds its errors as
// values of it, setting the exported fields ErrorCode, String and Ext["authErrorCode"]
// Errors are returned unwrapped: the helpers do not
// look through fmt.Errorf("%w").

// Auth error codes the official helpers compare (firebase-admin-go auth package).
const (
	codeIDTokenInvalid       = "ID_TOKEN_INVALID"
	codeIDTokenExpired       = "ID_TOKEN_EXPIRED"
	codeIDTokenRevoked       = "ID_TOKEN_REVOKED"
	codeSessionInvalid       = "SESSION_COOKIE_INVALID"
	codeSessionExpired       = "SESSION_COOKIE_EXPIRED"
	codeSessionRevoked       = "SESSION_COOKIE_REVOKED"
	codeUserDisabled         = "USER_DISABLED"
	codeCertificateFetch     = "CERTIFICATE_FETCH_FAILED"
	codeUserNotFound         = "USER_NOT_FOUND"
	codeEmailAlreadyExists   = "EMAIL_ALREADY_EXISTS"
	codeUIDAlreadyExists     = "UID_ALREADY_EXISTS"
	codeEmailNotFound        = "EMAIL_NOT_FOUND"
	codePhoneAlreadyExists   = "PHONE_NUMBER_ALREADY_EXISTS"
	codeUnauthorizedContinue = "UNAUTHORIZED_CONTINUE_URI"
)

// Platform error codes (firebase-admin-go internal.ErrorCode values).
const (
	platformInvalidArgument = "INVALID_ARGUMENT"
	platformUnauthenticated = "UNAUTHENTICATED"
	platformPermission      = "PERMISSION_DENIED"
	platformNotFound        = "NOT_FOUND"
	platformAlreadyExists   = "ALREADY_EXISTS"
	platformConflict        = "CONFLICT"
	platformExhausted       = "RESOURCE_EXHAUSTED"
	platformInternal        = "INTERNAL"
	platformUnavailable     = "UNAVAILABLE"
	platformUnknown         = "UNKNOWN"
)

var (
	errTypeOnce sync.Once
	errType     reflect.Type
	errTypeErr  error
)

// firebaseErrorType returns the official error type, captured once.
func firebaseErrorType() (reflect.Type, error) {
	errTypeOnce.Do(func() {
		ctx := context.Background()
		app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: "aruma-self-test"}, option.WithoutAuthentication())
		if err != nil {
			errTypeErr = err
			return
		}
		client, err := app.Auth(ctx)
		if err != nil {
			errTypeErr = err
			return
		}
		// An empty token fails before any network use.
		_, sample := client.VerifyIDToken(ctx, "")
		t := reflect.TypeOf(sample)
		if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
			errTypeErr = fmt.Errorf("adminauth: unexpected firebase error type %v", t)
			return
		}
		for _, f := range []string{"ErrorCode", "String", "Ext"} {
			if _, ok := t.Elem().FieldByName(f); !ok {
				errTypeErr = fmt.Errorf("adminauth: firebase error type %v has no field %s", t, f)
				return
			}
		}
		errType = t
		// The helpers must recognise what the factory builds.
		if !auth.IsIDTokenExpired(newError(platformInvalidArgument, codeIDTokenExpired, "self-test")) {
			errType, errTypeErr = nil, errors.New("adminauth: this firebase-admin-go version is not supported")
		}
	})
	return errType, errTypeErr
}

// newError builds an official error. authCode may be empty for errors the official SDK
// reports by platform code only. NewClient has checked that the type was captured.
func newError(platform, authCode, msg string) error {
	t := errType
	if t == nil {
		return errors.New(msg)
	}
	v := reflect.New(t.Elem())
	e := v.Elem()
	e.FieldByName("ErrorCode").SetString(platform)
	e.FieldByName("String").SetString(msg)
	ext := map[string]interface{}{}
	if authCode != "" {
		ext["authErrorCode"] = authCode
	}
	e.FieldByName("Ext").Set(reflect.ValueOf(ext))
	return v.Interface().(error)
}

// serverCodes are the Identity Toolkit codes the official Go SDK gives an auth code to
// (serverError in firebase-admin-go auth/user_mgt.go); others keep only the platform code.
var serverCodes = map[string]struct{ platform, auth, msg string }{
	"DUPLICATE_EMAIL":     {platformAlreadyExists, codeEmailAlreadyExists, "user with the provided email already exists"},
	"DUPLICATE_LOCAL_ID":  {platformAlreadyExists, codeUIDAlreadyExists, "user with the provided uid already exists"},
	"EMAIL_EXISTS":        {platformAlreadyExists, codeEmailAlreadyExists, "user with the provided email already exists"},
	"EMAIL_NOT_FOUND":     {platformNotFound, codeEmailNotFound, "no user record found for the given email"},
	"PHONE_NUMBER_EXISTS": {platformAlreadyExists, codePhoneAlreadyExists, "user with the provided phone number already exists"},
	"UNAUTHORIZED_DOMAIN": {platformInvalidArgument, codeUnauthorizedContinue, "domain of the continue url is not whitelisted"},
	"USER_NOT_FOUND":      {platformNotFound, codeUserNotFound, "no user record found for the given identifier"},
}

var statusCodes = map[int]string{
	http.StatusBadRequest:          platformInvalidArgument,
	http.StatusUnauthorized:        platformUnauthenticated,
	http.StatusForbidden:           platformPermission,
	http.StatusNotFound:            platformNotFound,
	http.StatusConflict:            platformConflict,
	http.StatusTooManyRequests:     platformExhausted,
	http.StatusInternalServerError: platformInternal,
	http.StatusServiceUnavailable:  platformUnavailable,
}

// responseError turns an Aruma error response into the error the official SDK would return for
// the same Identity Toolkit answer.
func responseError(status int, message string) error {
	code, detail, _ := strings.Cut(message, ":")
	code, detail = strings.TrimSpace(code), strings.TrimSpace(detail)
	if c, ok := serverCodes[code]; ok {
		msg := c.msg
		if detail != "" {
			msg += ": " + detail
		}
		return newError(c.platform, c.auth, msg)
	}
	platform, ok := statusCodes[status]
	if !ok {
		platform = platformUnknown
	}
	if message == "" {
		message = fmt.Sprintf("unexpected http response with status: %d", status)
	}
	return newError(platform, "", message)
}
