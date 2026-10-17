package acceptance

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// The flows the firebase CLI runs: projects:list, apps:create (with polling), apps:list,
// apps:sdkconfig through projects/-/.
func TestManagementAPIFirebaseCLIFlows(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.owner()
	owner.project("chat-app")
	cli := e.withBearer(owner.pat())

	list := cli.mustOK(cli.do("GET", "/v1beta1/projects?pageSize=1000", nil))
	projects := list.list("results")
	if len(projects) != 1 || projects[0]["name"] != "projects/chat-app" || projects[0]["state"] != "ACTIVE" {
		t.Fatalf("projects: %s", list.Body)
	}
	if res, _ := projects[0]["resources"].(map[string]any); res["hostingSite"] != "chat-app" {
		t.Fatalf("hostingSite must be set: %s", list.Body)
	}
	if _, ok := list.JSON["nextPageToken"]; ok {
		t.Fatal("nextPageToken on the last page")
	}

	// M-02 by id and by number; names always use the id.
	p := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app", nil))
	byNumber := cli.mustOK(cli.do("GET", "/v1beta1/projects/"+p.str("projectNumber"), nil))
	assertEqual(t, "name by number", byNumber.str("name"), "projects/chat-app")

	// M-06 create returns a done Operation, and polling it returns the app.
	op := cli.mustOK(cli.do("POST", "/v1beta1/projects/chat-app/webApps", map[string]any{"displayName": "Web"}))
	if op.JSON["done"] != true || !strings.HasPrefix(op.str("name"), "operations/") {
		t.Fatalf("create operation: %s", op.Body)
	}
	resp, _ := op.JSON["response"].(map[string]any)
	if resp["@type"] != "type.googleapis.com/google.firebase.service.v1beta1.WebApp" {
		t.Fatalf("operation response: %s", op.Body)
	}
	appID, _ := resp["appId"].(string)
	polled := cli.mustOK(cli.do("GET", "/v1beta1/"+op.str("name"), nil))
	if polled.JSON["done"] != true || polled.str("response", "appId") != appID {
		t.Fatalf("polled operation: %s", polled.Body)
	}

	iosOp := cli.mustOK(cli.do("POST", "/v1beta1/projects/chat-app/iosApps", map[string]any{"displayName": "iOS", "bundleId": "com.ahmad.chat", "appStoreId": "123"}))
	iosID := iosOp.str("response", "appId")
	andOp := cli.mustOK(cli.do("POST", "/v1beta1/projects/chat-app/androidApps", map[string]any{"displayName": "Android", "packageName": "com.ahmad.chat"}))
	andID := andOp.str("response", "appId")

	// M-05 searchApps, with and without a filter; snake_case paging is accepted.
	all := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app:searchApps?page_size=100", nil)).list("apps")
	if len(all) != 3 {
		t.Fatalf("searchApps: %v", all)
	}
	only := cli.mustOK(cli.do("GET", `/v1beta1/projects/chat-app:searchApps?filter=platform%3DANDROID`, nil)).list("apps")
	if len(only) != 1 || only[0]["namespace"] != "com.ahmad.chat" || only[0]["platform"] != "ANDROID" {
		t.Fatalf("filtered searchApps: %v", only)
	}
	expect(t, cli.do("GET", `/v1beta1/projects/chat-app:searchApps?filter=bogus%3D1`, nil), 400, "INVALID_ARGUMENT")

	// M-08, M-11, M-14 through projects/-/, as apps:sdkconfig does.
	web := cli.mustOK(cli.do("GET", "/v1beta1/projects/-/webApps/"+appID+"/config", nil))
	assertEqual(t, "web config appId", web.str("appId"), appID)
	ios := cli.mustOK(cli.do("GET", "/v1beta1/projects/-/iosApps/"+iosID+"/config", nil))
	plist, err := base64.StdEncoding.DecodeString(ios.str("configFileContents"))
	if err != nil || ios.str("configFilename") != "GoogleService-Info.plist" || !bytes.Contains(plist, []byte(iosID)) {
		t.Fatalf("ios config: %s", ios.Body)
	}
	and := cli.mustOK(cli.do("GET", "/v1beta1/projects/-/androidApps/"+andID+"/config", nil))
	if and.str("configFilename") != "google-services.json" {
		t.Fatalf("android config: %s", and.Body)
	}
	// An app of another platform is not found under the wrong collection.
	expect(t, cli.do("GET", "/v1beta1/projects/-/webApps/"+iosID, nil), 404, "NOT_FOUND")

	// M-07 patch with updateMask; M-15 SHA; M-16 remove/undelete; M-18; M-04.
	patched := cli.mustOK(cli.do("PATCH", "/v1beta1/projects/chat-app/androidApps/"+andID+"?updateMask=displayName", map[string]any{"displayName": "Droid"}))
	assertEqual(t, "patched", patched.str("displayName"), "Droid")
	expect(t, cli.do("PATCH", "/v1beta1/projects/chat-app/androidApps/"+andID+"?updateMask=packageName", map[string]any{"packageName": "x.y"}), 400, "INVALID_ARGUMENT")
	sha := cli.mustOK(cli.do("POST", "/v1beta1/projects/chat-app/androidApps/"+andID+"/sha", map[string]any{"shaHash": strings.Repeat("ab", 20), "certType": "SHA_1"}))
	withSha := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app/androidApps/"+andID, nil))
	if hashes, _ := withSha.JSON["sha1Hashes"].([]any); len(hashes) != 1 {
		t.Fatalf("sha1Hashes: %s", withSha.Body)
	}
	cli.mustOK(cli.do("DELETE", "/v1beta1/"+sha.str("name"), nil))

	removed := cli.mustOK(cli.do("POST", "/v1beta1/projects/-/webApps/"+appID+":remove", map[string]any{}))
	if removed.str("response", "state") != "DELETED" || removed.str("response", "expireTime") == "" {
		t.Fatalf("remove: %s", removed.Body)
	}
	cli.mustOK(cli.do("POST", "/v1beta1/projects/-/webApps/"+appID+":undelete", map[string]any{}))
	gone := cli.mustOK(cli.do("POST", "/v1beta1/projects/-/webApps/"+appID+":remove", map[string]any{"immediate": true}))
	assertEqual(t, "immediate removal", gone.str("response", "@type"), "type.googleapis.com/google.protobuf.Empty")
	cli.mustOK(cli.do("POST", "/v1beta1/projects/-/webApps/"+appID+":remove", map[string]any{"allowMissing": true}))
	expect(t, cli.do("POST", "/v1beta1/projects/-/webApps/"+appID+":remove", map[string]any{}), 404, "NOT_FOUND")

	admin := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app/adminSdkConfig", nil))
	assertEqual(t, "adminSdkConfig", admin.str("projectId"), "chat-app")
	avail := cli.mustOK(cli.do("GET", "/v1beta1/availableProjects", nil)).list("projectInfo")
	if len(avail) != 1 || avail[0]["project"] != "projects/chat-app" {
		t.Fatalf("availableProjects: %v", avail)
	}

	// M-03 requires updateMask.
	expect(t, cli.do("PATCH", "/v1beta1/projects/chat-app", map[string]any{"displayName": "X"}), 400, "INVALID_ARGUMENT")
	renamed := cli.mustOK(cli.do("PATCH", "/v1beta1/projects/chat-app?update_mask=display_name", map[string]any{"displayName": "Chat"}))
	assertEqual(t, "renamed", renamed.str("displayName"), "Chat")
}

// AC-21: the Management API and the console serve the same bytes for every config file.
func TestConfigBytesMatchAcrossAPIs(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.owner()
	owner.project("chat-app")
	cli := e.withBearer(owner.pat())
	apps := map[string]string{
		"webApps":     owner.app("chat-app", "WEB", "").str("appId"),
		"iosApps":     owner.app("chat-app", "IOS", "com.ahmad.chat").str("appId"),
		"androidApps": owner.app("chat-app", "ANDROID", "com.ahmad.chat").str("appId"),
	}
	owner.app("chat-app", "ANDROID", "com.ahmad.chat.admin")
	for coll, id := range apps {
		console := owner.mustOK(owner.do("GET", "/console/v1/projects/chat-app/apps/"+id+"/config", nil)).Body
		mgmt := cli.mustOK(cli.do("GET", "/v1beta1/projects/-/"+coll+"/"+id+"/config", nil))
		got := mgmt.Body
		if coll != "webApps" {
			var err error
			if got, err = base64.StdEncoding.DecodeString(mgmt.str("configFileContents")); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(console, got) {
			t.Errorf("%s: console and Management API differ:\n%s\n---\n%s", coll, console, got)
		}
	}
}

// AC-27 and ADR-010 S-10: every Management API route needs a credential, an API key is not one,
// and every error is a google.rpc.Status.
func TestManagementAPIAuthAndErrors(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.owner()
	owner.project("chat-app")
	web := owner.app("chat-app", "WEB", "")
	key := owner.mustOK(owner.do("GET", "/console/v1/projects/chat-app/apiKeys/"+web.str("apiKeyId"), nil)).str("keyString")

	anon := e.client()
	withKey := e.withBearer(key)
	viaQuery := e.client()
	checked := 0
	for _, rt := range e.App.Mgmt.Routes() {
		path := strings.NewReplacer("{project}", "chat-app", "{app}", web.str("appId"), "{name...}", "aruma/x", "{sha}", "x").Replace(rt.Pattern)
		for name, c := range map[string]*Client{"anonymous": anon, "api key": withKey} {
			r := c.do(rt.Method, path, map[string]any{})
			if r.Status != 401 || r.errStatus() != "UNAUTHENTICATED" || r.str("error", "message") == "" {
				t.Errorf("%s %s (%s): %d %s", rt.Method, path, name, r.Status, r.Body)
			}
		}
		if r := viaQuery.do(rt.Method, path+"?key="+key, map[string]any{}); r.Status != 401 {
			t.Errorf("%s %s with ?key=: %d", rt.Method, path, r.Status)
		}
		checked++
	}
	if checked < 25 {
		t.Fatalf("only %d Management API routes", checked)
	}

	cli := e.withBearer(owner.pat())
	for _, path := range []string{"/v1beta1/projects/nope-project", "/v1beta1/unknown", "/v1beta1/operations/garbage"} {
		r := cli.do("GET", path, nil)
		if r.Status != 404 || r.errStatus() != "NOT_FOUND" || r.JSON["error"].(map[string]any)["code"] != float64(404) {
			t.Errorf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	bad := cli.do("POST", "/v1beta1/projects/chat-app/iosApps", map[string]any{"bundleId": "not valid"})
	if bad.Status != 400 || bad.errStatus() != "INVALID_ARGUMENT" {
		t.Fatalf("validation error: %d %s", bad.Status, bad.Body)
	}
	if details, _ := bad.JSON["error"].(map[string]any)["details"].([]any); len(details) == 0 {
		t.Fatalf("no BadRequest details: %s", bad.Body)
	}
}

// Review L-4: removing through the wrong collection with allowMissing leaves the app alone.
func TestRemoveThroughWrongCollectionIsANoOp(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.owner()
	owner.project("chat-app")
	android := owner.app("chat-app", "ANDROID", "com.ahmad.chat").str("appId")
	cli := e.withBearer(owner.pat())
	cli.mustOK(cli.do("POST", "/v1beta1/projects/chat-app/webApps/"+android+":remove", map[string]any{"allowMissing": true}))
	if st := owner.mustOK(owner.do("GET", "/console/v1/projects/chat-app/apps/"+android, nil)).str("state"); st != "ACTIVE" {
		t.Fatalf("android app removed through webApps: %s", st)
	}
}

func TestOperationsRespectOwnership(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.owner()
	owner.project("chat-app")
	cli := e.withBearer(owner.pat())
	op := cli.mustOK(cli.do("POST", "/v1beta1/projects/chat-app/webApps", map[string]any{}))
	stranger := e.withBearer(e.account("stranger@example.com").pat())
	expect(t, stranger.do("GET", "/v1beta1/"+op.str("name"), nil), 404, "NOT_FOUND")
}
