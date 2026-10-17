package core

import (
	"net/http"
	"strconv"
)

// Error is an Identity Toolkit error: a code the SDKs map (EMAIL_EXISTS, TOKEN_EXPIRED...) and
// an optional detail, rendered as "CODE : detail" in the Google error envelope. Codes and their
// order follow docs/research/phase2-identity-toolkit.md; texts are Aruma's own (U-13).
type Error struct {
	Code   string
	Detail string
	Status int
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return e.Code + " : " + e.Detail
	}
	return e.Code
}

func fail(code string) *Error { return &Error{Code: code, Status: http.StatusBadRequest} }

func failf(code, detail string) *Error {
	return &Error{Code: code, Detail: detail, Status: http.StatusBadRequest}
}

// Codes the client SDKs act on.
var (
	ErrInvalidAPIKey         = &Error{Code: "API key not valid. Please pass a valid API key.", Status: http.StatusBadRequest}
	ErrMissingEmail          = fail("MISSING_EMAIL")
	ErrMissingPassword       = fail("MISSING_PASSWORD")
	ErrInvalidEmail          = fail("INVALID_EMAIL")
	ErrEmailExists           = fail("EMAIL_EXISTS")
	ErrEmailNotFound         = fail("EMAIL_NOT_FOUND")
	ErrInvalidPassword       = fail("INVALID_PASSWORD")
	ErrInvalidLogin          = fail("INVALID_LOGIN_CREDENTIALS")
	ErrOperationNotAllowed   = fail("OPERATION_NOT_ALLOWED")
	ErrPasswordLoginDisabled = fail("PASSWORD_LOGIN_DISABLED")
	ErrAdminOnly             = fail("ADMIN_ONLY_OPERATION")
	ErrUserDisabled          = fail("USER_DISABLED")
	ErrUserNotFound          = fail("USER_NOT_FOUND")
	ErrInvalidIDToken        = fail("INVALID_ID_TOKEN")
	ErrTokenExpired          = fail("TOKEN_EXPIRED")
	ErrCredentialTooOld      = fail("CREDENTIAL_TOO_OLD_LOGIN_AGAIN")
	ErrMissingCustomToken    = fail("MISSING_CUSTOM_TOKEN")
	ErrInvalidCustomToken    = fail("INVALID_CUSTOM_TOKEN")
	ErrCredentialMismatch    = fail("CREDENTIAL_MISMATCH")
	ErrMissingRefreshToken   = fail("MISSING_REFRESH_TOKEN")
	ErrInvalidRefreshToken   = fail("INVALID_REFRESH_TOKEN")
	ErrInvalidGrantType      = fail("INVALID_GRANT_TYPE")
	ErrMissingGrantType      = fail("MISSING_GRANT_TYPE")
	ErrTooManyAttempts       = fail("TOO_MANY_ATTEMPTS_TRY_LATER")
	ErrInvalidReqType        = fail("INVALID_REQ_TYPE")
	ErrUIDExists             = fail("DUPLICATE_LOCAL_ID")
	ErrInvalidClaims         = fail("INVALID_CLAIMS")
	ErrClaimsTooLarge        = fail("CLAIMS_TOO_LARGE")
	ErrInvalidUID            = fail("INVALID_LOCAL_ID")
	ErrInvalidDisplayName    = fail("INVALID_DISPLAY_NAME")
	ErrInvalidPhotoURL       = fail("INVALID_PHOTO_URL")
	ErrPasswordRequirements  = fail("PASSWORD_DOES_NOT_MEET_REQUIREMENTS")
	ErrInvalidPageSelection  = fail("INVALID_PAGE_SELECTION")
	ErrMaximumUserCount      = fail("MAXIMUM_USER_COUNT_EXCEEDED")
)

// weakPassword names the configured minimum, as Firebase's detail does.
func weakPassword(min int) *Error {
	return failf("WEAK_PASSWORD", "Password should be at least "+strconv.Itoa(min)+" characters")
}
