package api

import (
	"strconv"
	"time"

	"github.com/arumadev/aruma/internal/services/auth/core"
)

// passwordHashPlaceholder stands in for the hash, which never leaves the server. The web SDK
// treats a user with an email but no passwordHash as anonymous (research §3.4), so password
// users must carry a non-empty value. It is base64 of "REDACTED".
const passwordHashPlaceholder = "UkVEQUNURUQ="

type providerJSON struct {
	ProviderID  string `json:"providerId"`
	RawID       string `json:"rawId"`
	FederatedID string `json:"federatedId,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	PhotoURL    string `json:"photoUrl,omitempty"`
}

// userJSON is a user as Identity Toolkit returns it: timestamps as strings of milliseconds,
// validSince as a string of seconds, passwordUpdatedAt as a number.
type userJSON struct {
	LocalID           string         `json:"localId"`
	Email             string         `json:"email,omitempty"`
	EmailVerified     bool           `json:"emailVerified"`
	DisplayName       string         `json:"displayName,omitempty"`
	PhotoURL          string         `json:"photoUrl,omitempty"`
	ProviderUserInfo  []providerJSON `json:"providerUserInfo,omitempty"`
	PasswordHash      string         `json:"passwordHash,omitempty"`
	PasswordUpdatedAt int64          `json:"passwordUpdatedAt,omitempty"`
	ValidSince        string         `json:"validSince"`
	Disabled          bool           `json:"disabled,omitempty"`
	LastLoginAt       string         `json:"lastLoginAt,omitempty"`
	CreatedAt         string         `json:"createdAt"`
	LastRefreshAt     string         `json:"lastRefreshAt,omitempty"`
	CustomAttributes  string         `json:"customAttributes,omitempty"`
}

func millis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func toUserJSON(u core.User) userJSON {
	out := userJSON{
		LocalID: u.UID, Email: u.Email, EmailVerified: u.EmailVerified, DisplayName: u.DisplayName,
		PhotoURL: u.PhotoURL, Disabled: u.Disabled, ValidSince: strconv.FormatInt(u.TokensValidAfter.Unix(), 10),
		CreatedAt: millis(u.CreatedAt),
	}
	if u.LastLoginAt != nil {
		out.LastLoginAt = millis(*u.LastLoginAt)
	}
	if u.LastRefreshAt != nil {
		out.LastRefreshAt = u.LastRefreshAt.UTC().Format(time.RFC3339Nano)
	}
	if u.HasPassword {
		out.PasswordHash = passwordHashPlaceholder
		if u.PasswordUpdated != nil {
			out.PasswordUpdatedAt = u.PasswordUpdated.UnixMilli()
		}
	}
	if len(u.CustomClaims) > 0 {
		out.CustomAttributes = string(u.CustomClaims)
	}
	for _, p := range u.Providers {
		out.ProviderUserInfo = append(out.ProviderUserInfo, providerJSON{
			ProviderID: p.ProviderID, RawID: p.RawID, FederatedID: p.RawID, Email: p.Email,
			DisplayName: p.DisplayName, PhotoURL: p.PhotoURL,
		})
	}
	return out
}
