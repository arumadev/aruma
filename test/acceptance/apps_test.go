package acceptance

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var (
	appIDRe  = regexp.MustCompile(`^1:[1-9][0-9]{11}:(web|ios|android):[0-9a-f]{16}$`)
	apiKeyRe = regexp.MustCompile(`^Aru[A-Za-z0-9_-]{36}$`)
)

// AC-12: a package name is unique within a project, but may repeat across projects.
func TestDuplicatePackage(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	c.project("other-app")
	c.app("chat-app", "ANDROID", "com.ahmad.chat")
	expect(t, c.do("POST", "/console/v1/projects/chat-app/apps", map[string]any{"platform": "ANDROID", "packageName": "com.ahmad.chat"}), 409, "ALREADY_EXISTS")
	c.app("other-app", "ANDROID", "com.ahmad.chat")
	c.app("chat-app", "IOS", "com.ahmad.chat")
	expect(t, c.do("POST", "/console/v1/projects/chat-app/apps", map[string]any{"platform": "IOS", "bundleId": "com.ahmad.chat"}), 409, "ALREADY_EXISTS")
}

// AC-13: bundle ids and package names cannot change.
func TestImmutableNamespaces(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	ios := c.app("chat-app", "IOS", "com.ahmad.chat")
	path := "/console/v1/projects/chat-app/apps/" + ios.str("appId")
	expect(t, c.do("PATCH", path, map[string]any{"bundleId": "com.other"}), 400, "INVALID_ARGUMENT")
	r := c.mustOK(c.do("PATCH", path, map[string]any{"displayName": "Chat iOS"}))
	assertEqual(t, "bundle", r.str("bundleId"), "com.ahmad.chat")
	assertEqual(t, "name", r.str("displayName"), "Chat iOS")
}

// AC-14, AC-15: app ids and keys have the formats of ADR-007; one key per platform.
func TestAppIDAndKeyFormats(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	p := c.project("chat-app")
	number := p.str("projectNumber")
	apps := map[string]*Response{
		"web":      c.app("chat-app", "WEB", ""),
		"web2":     c.app("chat-app", "WEB", ""),
		"android":  c.app("chat-app", "ANDROID", "com.ahmad.chat"),
		"android2": c.app("chat-app", "ANDROID", "com.ahmad.chat.admin"),
		"ios":      c.app("chat-app", "IOS", "com.ahmad.chat"),
	}
	for name, a := range apps {
		id := a.str("appId")
		if !appIDRe.MatchString(id) || !strings.HasPrefix(id, "1:"+number+":") {
			t.Errorf("%s app id %q", name, id)
		}
	}
	keys := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/apiKeys", nil)).list("keys")
	if len(keys) != 3 {
		t.Fatalf("want one key per platform (3), got %d", len(keys))
	}
	for _, k := range keys {
		s, _ := k["keyString"].(string)
		if !apiKeyRe.MatchString(s) || len(s) != 39 || strings.HasPrefix(s, "AIza") {
			t.Errorf("key %q", s)
		}
	}
	assertEqual(t, "web apps share a key", apps["web"].str("apiKeyId"), apps["web2"].str("apiKeyId"))
	assertEqual(t, "android apps share a key", apps["android"].str("apiKeyId"), apps["android2"].str("apiKeyId"))
	if apps["web"].str("apiKeyId") == apps["android"].str("apiKeyId") {
		t.Error("platforms share a key")
	}
}

// AC-16: a disabled key is refused; rotation moves config files to a new key.
func TestKeyDisableAndRotate(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	web := c.app("chat-app", "WEB", "")
	keyID := web.str("apiKeyId")
	base := "/console/v1/projects/chat-app/apiKeys/" + keyID
	oldKey := c.mustOK(c.do("GET", base, nil)).str("keyString")
	svc := e.App.Service
	ctx := context.Background()

	if _, err := svc.LookupAPIKey(ctx, oldKey); err != nil {
		t.Fatalf("active key refused: %v", err)
	}
	rotated := c.mustOK(c.do("POST", base+":rotate", nil))
	newKey := rotated.str("keyString")
	if newKey == oldKey || rotated.str("rotatedFrom") != keyID || rotated.str("platform") != "WEB" {
		t.Fatalf("rotation: %s", rotated.Body)
	}
	cfg := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/apps/"+web.str("appId")+"/config", nil))
	if !strings.Contains(string(cfg.Body), newKey) {
		t.Fatal("config does not carry the rotated key")
	}
	if _, err := svc.LookupAPIKey(ctx, oldKey); err != nil {
		t.Fatal("old key stopped working before it was disabled")
	}
	disabled := c.mustOK(c.do("POST", base+":disable", nil))
	assertEqual(t, "state", disabled.str("state"), "DISABLED")
	if _, err := svc.LookupAPIKey(ctx, oldKey); err == nil {
		t.Fatal("disabled key accepted")
	}
	c.mustOK(c.do("POST", base+":enable", nil))
	if _, err := svc.LookupAPIKey(ctx, oldKey); err != nil {
		t.Fatal("re-enabled key refused")
	}
	if _, err := svc.LookupAPIKey(ctx, "AIzaSyB-not-an-aruma-key-at-all-123456"); err == nil {
		t.Fatal("foreign key accepted")
	}
}

func TestKeyRestrictions(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	k := c.mustOK(c.do("POST", "/console/v1/projects/chat-app/apiKeys", map[string]any{
		"displayName":  "Server checks",
		"restrictions": map[string]any{"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"https://chat.example.com/*"}}},
	}))
	if k.str("platform") != "" {
		t.Fatal("manual key bound to a platform")
	}
	path := "/console/v1/projects/chat-app/apiKeys/" + k.str("uid")
	expect(t, c.do("PATCH", path, map[string]any{"restrictions": map[string]any{
		"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{}},
		"iosKeyRestrictions":     map[string]any{"allowedBundleIds": []string{"com.x.y"}},
	}}), 400, "INVALID_ARGUMENT")
	expect(t, c.do("PATCH", path, map[string]any{"restrictions": map[string]any{"bogus": 1}}), 400, "INVALID_ARGUMENT")
	r := c.mustOK(c.do("PATCH", path, map[string]any{"restrictions": map[string]any{"iosKeyRestrictions": map[string]any{"allowedBundleIds": []string{"com.ahmad.chat"}}}}))
	var res struct {
		IOS struct {
			IDs []string `json:"allowedBundleIds"`
		} `json:"iosKeyRestrictions"`
	}
	b, _ := json.Marshal(r.JSON["restrictions"])
	json.Unmarshal(b, &res)
	if len(res.IOS.IDs) != 1 {
		t.Fatalf("restrictions not stored: %s", r.Body)
	}
}

func TestAppRemoveAndUndelete(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	web := c.app("chat-app", "WEB", "")
	path := "/console/v1/projects/chat-app/apps/" + web.str("appId")
	removed := c.mustOK(c.do("POST", path+":remove", map[string]any{}))
	if removed.str("state") != "DELETED" || removed.str("expireTime") == "" {
		t.Fatalf("removed: %s", removed.Body)
	}
	expect(t, c.do("GET", path+"/config", nil), 404, "NOT_FOUND")
	if len(c.mustOK(c.do("GET", "/console/v1/projects/chat-app/apps", nil)).list("apps")) != 0 {
		t.Fatal("deleted app listed")
	}
	c.mustOK(c.do("POST", path+":undelete", nil))
	c.mustOK(c.do("GET", path+"/config", nil))
	c.mustOK(c.do("POST", path+":remove", map[string]any{"immediate": true}))
	expect(t, c.do("GET", path, nil), 404, "NOT_FOUND")
}

func TestShaCertificates(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	android := c.app("chat-app", "ANDROID", "com.ahmad.chat")
	ios := c.app("chat-app", "IOS", "com.ahmad.chat")
	path := "/console/v1/projects/chat-app/apps/" + android.str("appId") + "/sha"
	sha1 := "DA:39:A3:EE:5E:6B:4B:0D:32:55:BF:EF:95:60:18:90:AF:D8:07:09"
	added := c.mustOK(c.do("POST", path, map[string]any{"shaHash": sha1}))
	assertEqual(t, "type", added.str("certType"), "SHA_1")
	assertEqual(t, "normalised", added.str("shaHash"), "da39a3ee5e6b4b0d3255bfef95601890afd80709")
	expect(t, c.do("POST", path, map[string]any{"shaHash": sha1}), 409, "ALREADY_EXISTS")
	expect(t, c.do("POST", path, map[string]any{"shaHash": "abc"}), 400, "INVALID_ARGUMENT")
	c.mustOK(c.do("POST", path, map[string]any{"shaHash": strings.Repeat("ab", 32)}))
	if n := len(c.mustOK(c.do("GET", path, nil)).list("certificates")); n != 2 {
		t.Fatalf("%d certificates", n)
	}
	c.mustOK(c.do("DELETE", path+"/"+added.str("id"), nil))
	expect(t, c.do("DELETE", path+"/"+added.str("id"), nil), 404, "NOT_FOUND")
	expect(t, c.do("GET", "/console/v1/projects/chat-app/apps/"+ios.str("appId")+"/sha", nil), 400, "FAILED_PRECONDITION")
}

// Config files carry every field and the right identifiers for their platform.
func TestConfigDownloads(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	p := c.project("chat-app")
	web := c.app("chat-app", "WEB", "")
	a1 := c.app("chat-app", "ANDROID", "com.ahmad.chat")
	a2 := c.app("chat-app", "ANDROID", "com.ahmad.chat.admin")
	ios := c.app("chat-app", "IOS", "com.ahmad.chat")
	base := "/console/v1/projects/chat-app/apps/"

	wc := c.mustOK(c.do("GET", base+web.str("appId")+"/config", nil))
	if wc.str("appId") != web.str("appId") || wc.str("messagingSenderId") != p.str("projectNumber") || wc.str("storageBucket") != "chat-app.127.0.0.1" {
		t.Fatalf("web config: %s", wc.Body)
	}
	if !strings.Contains(wc.Header.Get("Content-Disposition"), "firebase-config.json") || wc.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("web headers: %v", wc.Header)
	}
	snippet := c.mustOK(c.do("GET", base+web.str("appId")+"/config?format=snippet", nil))
	if !strings.HasPrefix(string(snippet.Body), "const firebaseConfig = {") {
		t.Fatalf("snippet: %s", snippet.Body)
	}
	expect(t, c.do("GET", base+ios.str("appId")+"/config?format=snippet", nil), 400, "INVALID_ARGUMENT")

	gs := c.mustOK(c.do("GET", base+a1.str("appId")+"/config", nil))
	clients, _ := gs.JSON["client"].([]any)
	if len(clients) != 2 || !strings.Contains(string(gs.Body), a2.str("appId")) {
		t.Fatalf("google-services.json lacks a client: %s", gs.Body)
	}
	plist := c.mustOK(c.do("GET", base+ios.str("appId")+"/config", nil))
	if !strings.Contains(string(plist.Body), "<string>"+ios.str("appId")+"</string>") || !strings.Contains(plist.Header.Get("Content-Disposition"), "GoogleService-Info.plist") {
		t.Fatalf("plist: %s", plist.Body)
	}

	flutter := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/flutterOptions?web="+web.str("appId")+"&android="+a1.str("appId")+"&ios="+ios.str("appId"), nil))
	for _, want := range []string{"class DefaultFirebaseOptions", "static const FirebaseOptions android", a1.str("appId"), "iosBundleId: 'com.ahmad.chat'"} {
		if !strings.Contains(string(flutter.Body), want) {
			t.Errorf("firebase_options.dart lacks %q", want)
		}
	}
	expect(t, c.do("GET", "/console/v1/projects/chat-app/flutterOptions?android="+ios.str("appId"), nil), 400, "INVALID_ARGUMENT")
	expect(t, c.do("GET", "/console/v1/projects/chat-app/flutterOptions", nil), 400, "INVALID_ARGUMENT")

	admin := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/adminSdkConfig", nil))
	assertEqual(t, "admin projectId", admin.str("projectId"), "chat-app")
}
