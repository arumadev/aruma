// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"firebase.google.com/go/v4/auth"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestEmulatorGuard(t *testing.T) {
	if err := emulatorGuard(false, env(nil)); err != nil {
		t.Fatal(err)
	}
	set := map[string]string{"FIREBASE_AUTH_EMULATOR_HOST": "localhost:9099"}
	if err := emulatorGuard(false, env(set)); err == nil {
		t.Fatal("started with the emulator variable set")
	}
	if err := emulatorGuard(true, env(set)); err != nil {
		t.Fatalf("explicitly allowed: %v", err)
	}
	for _, v := range []string{"GO_ENV", "NODE_ENV"} {
		prod := map[string]string{"FIREBASE_AUTH_EMULATOR_HOST": "x", v: "production"}
		if err := emulatorGuard(true, env(prod)); err == nil {
			t.Fatalf("allowed in production (%s)", v)
		}
	}
	t.Setenv("FIREBASE_AUTH_EMULATOR_HOST", "localhost:9099")
	if _, err := NewClient(context.Background(), nil, WithCredentialsJSON([]byte(`{}`))); err == nil || !strings.Contains(err.Error(), "FIREBASE_AUTH_EMULATOR_HOST") {
		t.Fatalf("NewClient: %v", err)
	}
}

func TestResolveURL(t *testing.T) {
	key := serviceAccountKey{TokenURI: "https://aruma.example.com/oauth2/v1/token"}
	for _, c := range []struct {
		opt  string
		env  map[string]string
		want string
	}{
		{"", nil, "https://aruma.example.com"},
		{"", map[string]string{"ARUMA_URL": "https://env.example.com/"}, "https://env.example.com"},
		{"https://opt.example.com/base/", map[string]string{"ARUMA_URL": "https://env.example.com"}, "https://opt.example.com/base"},
		{"http://127.0.0.1:8080", nil, "http://127.0.0.1:8080"},
		{"http://localhost:8080", nil, "http://localhost:8080"},
	} {
		got, err := resolveURL(c.opt, key, env(c.env))
		if err != nil || got != c.want {
			t.Errorf("resolveURL(%q, %v) = %q, %v; want %q", c.opt, c.env, got, err, c.want)
		}
	}
	for _, bad := range []string{"http://aruma.example.com", "https://identitytoolkit.googleapis.com", "https://accounts.google.com", "https://accounts.google.com.", "https://oauth2.googleapis.com./token", "https://a.example.com/?x=1", "::"} {
		if _, err := resolveURL(bad, serviceAccountKey{}, env(nil)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := resolveURL("", serviceAccountKey{TokenURI: "https://oauth2.googleapis.com/token"}, env(nil)); err == nil {
		t.Error("followed a Google token_uri")
	}
}

func TestUpdateRequestMapsOfficialParams(t *testing.T) {
	if err := checkParams(); err != nil {
		t.Fatal(err)
	}
	u := (&auth.UserToUpdate{}).DisplayName("").PhotoURL("https://example.com/p.png").PhoneNumber("").
		Disabled(true).EmailVerified(true).ProvidersToDelete([]string{"google.com"}).
		CustomClaims(map[string]interface{}{"role": "admin", "n": 2})
	p, err := params(u)
	if err != nil {
		t.Fatal(err)
	}
	req, err := updateRequest("uid-1", p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"localId": "uid-1", "photoUrl": "https://example.com/p.png", "disableUser": true, "emailVerified": true,
		"deleteAttribute": []string{"DISPLAY_NAME"}, "customAttributes": `{"n":2,"role":"admin"}`,
	}
	for k, v := range want {
		if !reflect.DeepEqual(req[k], v) {
			t.Errorf("%s = %#v, want %#v", k, req[k], v)
		}
	}
	if dp, _ := req["deleteProvider"].([]string); len(dp) != 2 {
		t.Errorf("deleteProvider = %v", req["deleteProvider"])
	}

	for name, claims := range map[string]map[string]interface{}{
		"reserved": {"sub": "x"},
		"too big":  {"big": strings.Repeat("x", 1000)},
	} {
		if _, err := marshalClaims(claims); err == nil {
			t.Errorf("%s claims accepted", name)
		}
	}
	if s, _ := marshalClaims(nil); s != "{}" {
		t.Errorf("nil claims = %q, want {}", s)
	}
	if _, err := updateRequest("u", map[string]any{"linkProviderUserInfo": unsupported{"auth.UserProvider"}}); err == nil {
		t.Error("provider linking must be refused until Aruma supports it")
	}
}
