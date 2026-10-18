// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/iterator"
)

// User management through the Admin API, with the request checks and the record mapping of
// firebase-admin-go (auth/user_mgt.go), so callers see the same values and errors.

const (
	maxLookup      = 100
	maxDelete      = 1000
	maxListPage    = 1000
	maxClaimsBytes = 1000
)

var redacted = base64.StdEncoding.EncodeToString([]byte("REDACTED"))

// reservedClaims may not be custom claims (firebase-admin-go's list).
var reservedClaims = []string{
	"acr", "amr", "at_hash", "aud", "auth_time", "azp", "cnf", "c_hash",
	"exp", "firebase", "iat", "iss", "jti", "nbf", "nonce", "sub",
}

// userJSON is an Identity Toolkit user. Timestamps are quoted integers, as firebase-admin-go
// requires.
type userJSON struct {
	UID               string           `json:"localId"`
	DisplayName       string           `json:"displayName"`
	Email             string           `json:"email"`
	PhoneNumber       string           `json:"phoneNumber"`
	PhotoURL          string           `json:"photoUrl"`
	CreationTimestamp int64            `json:"createdAt,string"`
	LastLogInTime     int64            `json:"lastLoginAt,string"`
	LastRefreshAt     string           `json:"lastRefreshAt"`
	CustomAttributes  string           `json:"customAttributes"`
	Disabled          bool             `json:"disabled"`
	EmailVerified     bool             `json:"emailVerified"`
	ProviderUserInfo  []*auth.UserInfo `json:"providerUserInfo"`
	PasswordHash      string           `json:"passwordHash"`
	PasswordSalt      string           `json:"salt"`
	TenantID          string           `json:"tenantId"`
	ValidSince        int64            `json:"validSince,string"`
}

func (u *userJSON) record() (*auth.ExportedUserRecord, error) {
	var claims map[string]interface{}
	if u.CustomAttributes != "" {
		if err := json.Unmarshal([]byte(u.CustomAttributes), &claims); err != nil {
			return nil, err
		}
		if len(claims) == 0 {
			claims = nil
		}
	}
	var lastRefresh int64
	if u.LastRefreshAt != "" {
		t, err := time.Parse(time.RFC3339, u.LastRefreshAt)
		if err != nil {
			return nil, err
		}
		lastRefresh = t.Unix() * 1000
	}
	hash := u.PasswordHash
	if hash == redacted {
		hash = ""
	}
	return &auth.ExportedUserRecord{
		UserRecord: &auth.UserRecord{
			UserInfo: &auth.UserInfo{
				DisplayName: u.DisplayName, Email: u.Email, PhoneNumber: u.PhoneNumber,
				PhotoURL: u.PhotoURL, ProviderID: "firebase", UID: u.UID,
			},
			CustomClaims:           claims,
			Disabled:               u.Disabled,
			EmailVerified:          u.EmailVerified,
			ProviderUserInfo:       u.ProviderUserInfo,
			TokensValidAfterMillis: u.ValidSince * 1000,
			UserMetadata: &auth.UserMetadata{
				CreationTimestamp: u.CreationTimestamp, LastLogInTimestamp: u.LastLogInTime, LastRefreshTimestamp: lastRefresh,
			},
			TenantID: u.TenantID,
		},
		PasswordHash: hash,
		PasswordSalt: u.PasswordSalt,
	}, nil
}

func checkUID(uid string) error {
	if uid == "" {
		return errors.New("uid must be non-empty")
	}
	if len(uid) > 128 {
		return errors.New("uid string must not be longer than 128 characters")
	}
	return nil
}

func checkEmail(email string) error {
	if email == "" {
		return errors.New("email must be a non-empty string")
	}
	if local, domain, ok := strings.Cut(email, "@"); !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return fmt.Errorf("malformed email string: %q", email)
	}
	return nil
}

type lookupRequest struct {
	LocalID     []string            `json:"localId,omitempty"`
	Email       []string            `json:"email,omitempty"`
	PhoneNumber []string            `json:"phoneNumber,omitempty"`
	Federated   []map[string]string `json:"federatedUserId,omitempty"`
}

func (c *Client) lookup(ctx context.Context, req *lookupRequest) ([]*auth.UserRecord, error) {
	var res struct {
		Users []userJSON `json:"users"`
	}
	if err := c.call(ctx, "POST", "/accounts:lookup", req, &res); err != nil {
		return nil, err
	}
	out := make([]*auth.UserRecord, 0, len(res.Users))
	for i := range res.Users {
		r, err := res.Users[i].record()
		if err != nil {
			return nil, err
		}
		out = append(out, r.UserRecord)
	}
	return out, nil
}

func (c *Client) lookupOne(ctx context.Context, req *lookupRequest, what string) (*auth.UserRecord, error) {
	users, err := c.lookup(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, newError(platformNotFound, codeUserNotFound, "cannot find user from "+what)
	}
	return users[0], nil
}

// GetUser returns the user with the uid.
func (c *Client) GetUser(ctx context.Context, uid string) (*auth.UserRecord, error) {
	if err := checkUID(uid); err != nil {
		return nil, err
	}
	return c.lookupOne(ctx, &lookupRequest{LocalID: []string{uid}}, fmt.Sprintf("uid: %q", uid))
}

// GetUserByEmail returns the user with the email address.
func (c *Client) GetUserByEmail(ctx context.Context, email string) (*auth.UserRecord, error) {
	if err := checkEmail(email); err != nil {
		return nil, err
	}
	return c.lookupOne(ctx, &lookupRequest{Email: []string{email}}, fmt.Sprintf("email: %q", email))
}

// GetUsers looks up to 100 users; identifiers that match nobody are listed in NotFound.
func (c *Client) GetUsers(ctx context.Context, identifiers []auth.UserIdentifier) (*auth.GetUsersResult, error) {
	if len(identifiers) == 0 {
		return &auth.GetUsersResult{Users: []*auth.UserRecord{}, NotFound: []auth.UserIdentifier{}}, nil
	}
	if len(identifiers) > maxLookup {
		return nil, fmt.Errorf("`identifiers` parameter must have <= %d entries", maxLookup)
	}
	req := &lookupRequest{}
	for _, id := range identifiers {
		switch id := id.(type) {
		case auth.UIDIdentifier:
			if err := checkUID(id.UID); err != nil {
				return nil, err
			}
			req.LocalID = append(req.LocalID, id.UID)
		case auth.EmailIdentifier:
			if err := checkEmail(id.Email); err != nil {
				return nil, err
			}
			req.Email = append(req.Email, id.Email)
		case auth.PhoneIdentifier:
			req.PhoneNumber = append(req.PhoneNumber, id.PhoneNumber)
		case auth.ProviderIdentifier:
			req.Federated = append(req.Federated, map[string]string{"providerId": id.ProviderID, "rawId": id.ProviderUID})
		default:
			return nil, fmt.Errorf("unsupported identifier %T", id)
		}
	}
	users, err := c.lookup(ctx, req)
	if err != nil {
		return nil, err
	}
	notFound := []auth.UserIdentifier{}
	for _, id := range identifiers {
		found := false
		for _, u := range users {
			if matches(id, u) {
				found = true
				break
			}
		}
		if !found {
			notFound = append(notFound, id)
		}
	}
	return &auth.GetUsersResult{Users: users, NotFound: notFound}, nil
}

func matches(id auth.UserIdentifier, u *auth.UserRecord) bool {
	switch id := id.(type) {
	case auth.UIDIdentifier:
		return id.UID == u.UID
	case auth.EmailIdentifier:
		return strings.EqualFold(id.Email, u.Email)
	case auth.PhoneIdentifier:
		return id.PhoneNumber == u.PhoneNumber
	case auth.ProviderIdentifier:
		for _, p := range u.ProviderUserInfo {
			if p.ProviderID == id.ProviderID && p.UID == id.ProviderUID {
				return true
			}
		}
	}
	return false
}

// CreateUser creates a user from the official auth.UserToCreate and returns its record.
func (c *Client) CreateUser(ctx context.Context, user *auth.UserToCreate) (*auth.UserRecord, error) {
	p, err := params(user)
	if err != nil {
		return nil, err
	}
	req := map[string]any{}
	for k, v := range p {
		switch k {
		case "localId":
			if err := checkUID(v.(string)); err != nil {
				return nil, err
			}
		case "email":
			if err := checkEmail(v.(string)); err != nil {
				return nil, err
			}
		case "mfaSettings":
			return nil, errors.New("multi-factor settings are not supported by Aruma yet")
		}
		req[k] = v
	}
	var res struct {
		UID string `json:"localId"`
	}
	if err := c.call(ctx, "POST", "/accounts", req, &res); err != nil {
		return nil, err
	}
	return c.GetUser(ctx, res.UID)
}

// UpdateUser applies the official auth.UserToUpdate and returns the updated record. Empty
// DisplayName, PhotoURL and PhoneNumber clear the field, as in firebase-admin-go.
func (c *Client) UpdateUser(ctx context.Context, uid string, user *auth.UserToUpdate) (*auth.UserRecord, error) {
	if err := checkUID(uid); err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errors.New("update parameters must not be nil or empty")
	}
	p, err := params(user)
	if err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, errors.New("update parameters must not be nil or empty")
	}
	req, err := updateRequest(uid, p)
	if err != nil {
		return nil, err
	}
	if err := c.call(ctx, "POST", "/accounts:update", req, nil); err != nil {
		return nil, err
	}
	return c.GetUser(ctx, uid)
}

// updateRequest maps UserToUpdate parameters to the accounts:update fields.
func updateRequest(uid string, p map[string]any) (map[string]any, error) {
	req := map[string]any{"localId": uid}
	var deleteAttribute, deleteProvider []string
	for k, v := range p {
		switch k {
		case "displayName", "photoUrl":
			if v == "" {
				attr := "DISPLAY_NAME"
				if k == "photoUrl" {
					attr = "PHOTO_URL"
				}
				deleteAttribute = append(deleteAttribute, attr)
				continue
			}
		case "phoneNumber":
			if v == "" {
				deleteProvider = append(deleteProvider, "phone")
				continue
			}
		case "email":
			if err := checkEmail(v.(string)); err != nil {
				return nil, err
			}
		case "customClaims":
			claims, _ := v.(map[string]any)
			s, err := marshalClaims(claims)
			if err != nil {
				return nil, err
			}
			req["customAttributes"] = s
			continue
		case "providersToDelete":
			for _, id := range v.([]any) {
				deleteProvider = append(deleteProvider, id.(string))
			}
			continue
		case "mfaSettings":
			return nil, errors.New("multi-factor settings are not supported by Aruma yet")
		}
		if u, ok := v.(unsupported); ok {
			return nil, fmt.Errorf("%s is not supported by Aruma yet", u.what)
		}
		req[k] = v
	}
	if len(deleteAttribute) > 0 {
		req["deleteAttribute"] = deleteAttribute
	}
	if len(deleteProvider) > 0 {
		req["deleteProvider"] = deleteProvider
	}
	return req, nil
}

func marshalClaims(claims map[string]any) (string, error) {
	for _, k := range reservedClaims {
		if _, ok := claims[k]; ok {
			return "", fmt.Errorf("claim %q is reserved and must not be set", k)
		}
	}
	if len(claims) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("custom claims marshaling error: %v", err)
	}
	if len(b) > maxClaimsBytes {
		return "", fmt.Errorf("serialized custom claims must not exceed %d characters", maxClaimsBytes)
	}
	return string(b), nil
}

// SetCustomUserClaims replaces the user's custom claims; nil or empty clears them. They reach
// ID tokens at the next refresh.
func (c *Client) SetCustomUserClaims(ctx context.Context, uid string, customClaims map[string]interface{}) error {
	if err := checkUID(uid); err != nil {
		return err
	}
	s, err := marshalClaims(customClaims)
	if err != nil {
		return err
	}
	return c.call(ctx, "POST", "/accounts:update", map[string]any{"localId": uid, "customAttributes": s}, nil)
}

// RevokeRefreshTokens revokes every session of the user: refreshes fail and the CheckRevoked
// methods reject tokens issued before now.
func (c *Client) RevokeRefreshTokens(ctx context.Context, uid string) error {
	if err := checkUID(uid); err != nil {
		return err
	}
	return c.call(ctx, "POST", "/accounts:update", map[string]any{"localId": uid, "validSince": strconv.FormatInt(c.now().Unix(), 10)}, nil)
}

// DeleteUser deletes the user.
func (c *Client) DeleteUser(ctx context.Context, uid string) error {
	if err := checkUID(uid); err != nil {
		return err
	}
	return c.call(ctx, "POST", "/accounts:delete", map[string]string{"localId": uid}, nil)
}

// DeleteUsers deletes up to 1000 users; unknown uids count as deleted.
func (c *Client) DeleteUsers(ctx context.Context, uids []string) (*auth.DeleteUsersResult, error) {
	if len(uids) == 0 {
		return &auth.DeleteUsersResult{}, nil
	}
	if len(uids) > maxDelete {
		return nil, fmt.Errorf("`uids` parameter must have <= %d entries", maxDelete)
	}
	for _, uid := range uids {
		if err := checkUID(uid); err != nil {
			return nil, err
		}
	}
	var res struct {
		Errors []*auth.DeleteUsersErrorInfo `json:"errors"`
	}
	if err := c.call(ctx, "POST", "/accounts:batchDelete", map[string]any{"localIds": uids, "force": true}, &res); err != nil {
		return nil, err
	}
	return &auth.DeleteUsersResult{SuccessCount: len(uids) - len(res.Errors), FailureCount: len(res.Errors), Errors: res.Errors}, nil
}

// SessionCookie exchanges a valid ID token for a session cookie lasting expiresIn (5 minutes
// to 14 days).
func (c *Client) SessionCookie(ctx context.Context, idToken string, expiresIn time.Duration) (string, error) {
	if idToken == "" {
		return "", errors.New("id token must not be empty")
	}
	if expiresIn < 5*time.Minute || expiresIn > 14*24*time.Hour {
		return "", errors.New("expiry duration must be between 5 minutes and 14 days")
	}
	var res struct {
		SessionCookie string `json:"sessionCookie"`
	}
	err := c.call(ctx, "POST", ":createSessionCookie", map[string]any{"idToken": idToken, "validDuration": int64(expiresIn.Seconds())}, &res)
	return res.SessionCookie, err
}

// UserIterator iterates over the project's users, as auth.UserIterator, whose fields no other
// package can set.
type UserIterator struct {
	client   *Client
	ctx      context.Context
	nextFunc func() error
	pageInfo *iterator.PageInfo
	users    []*auth.ExportedUserRecord
}

// Users returns an iterator over every user, starting after nextPageToken when it is set.
func (c *Client) Users(ctx context.Context, nextPageToken string) *UserIterator {
	it := &UserIterator{client: c, ctx: ctx}
	it.pageInfo, it.nextFunc = iterator.NewPageInfo(
		it.fetch,
		func() int { return len(it.users) },
		func() interface{} { b := it.users; it.users = nil; return b })
	it.pageInfo.MaxSize = maxListPage
	it.pageInfo.Token = nextPageToken
	return it
}

// PageInfo supports pagination (google.golang.org/api/iterator).
func (it *UserIterator) PageInfo() *iterator.PageInfo { return it.pageInfo }

// Next returns the next user, or iterator.Done.
func (it *UserIterator) Next() (*auth.ExportedUserRecord, error) {
	if err := it.nextFunc(); err != nil {
		return nil, err
	}
	u := it.users[0]
	it.users = it.users[1:]
	return u, nil
}

func (it *UserIterator) fetch(pageSize int, pageToken string) (string, error) {
	q := url.Values{"maxResults": {strconv.Itoa(pageSize)}}
	if pageToken != "" {
		q.Set("nextPageToken", pageToken)
	}
	var res struct {
		Users         []userJSON `json:"users"`
		NextPageToken string     `json:"nextPageToken"`
	}
	if err := it.client.call(it.ctx, "GET", "/accounts:batchGet?"+q.Encode(), nil, &res); err != nil {
		return "", err
	}
	for i := range res.Users {
		r, err := res.Users[i].record()
		if err != nil {
			return "", err
		}
		it.users = append(it.users, r)
	}
	it.pageInfo.Token = res.NextPageToken
	return res.NextPageToken, nil
}
