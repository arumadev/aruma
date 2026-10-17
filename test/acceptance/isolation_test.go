package acceptance

import (
	"strings"
	"testing"
)

// isolationEnv has two owners, A and B, each with one project holding one app of every
// platform and a service account key.
type isolationEnv struct {
	*Env
	a, b       *Client
	aCLI, bCLI *Client
	aApps      map[string]string // collection -> app id
	bApps      map[string]string
	bKeyFile   []byte
	aKeyFile   []byte
	bProject   *Response
}

func newIsolation(t *testing.T) *isolationEnv {
	e := newEnv(t, nil)
	ie := &isolationEnv{Env: e, a: e.owner(), b: e.account("b@example.com")}
	ie.a.project("alpha-app")
	ie.bProject = ie.b.project("bravo-app")
	ie.aApps, ie.bApps = map[string]string{}, map[string]string{}
	for _, x := range []struct {
		c       *Client
		project string
		apps    map[string]string
	}{{ie.a, "alpha-app", ie.aApps}, {ie.b, "bravo-app", ie.bApps}} {
		x.apps["webApps"] = x.c.app(x.project, "WEB", "").str("appId")
		x.apps["iosApps"] = x.c.app(x.project, "IOS", "com.shared.app").str("appId")
		x.apps["androidApps"] = x.c.app(x.project, "ANDROID", "com.shared.app").str("appId")
	}
	_, ie.aKeyFile = ie.a.serviceAccountKey("alpha-app", "backend")
	_, ie.bKeyFile = ie.b.serviceAccountKey("bravo-app", "backend")
	ie.aCLI, ie.bCLI = e.withBearer(ie.a.pat()), e.withBearer(ie.b.pat())
	return ie
}

// Owner A can neither read nor change B's project, by id or by number, on either API, and
// gets NOT_FOUND, never PERMISSION_DENIED.
func TestIsolationOwnerCannotReachOtherProject(t *testing.T) {
	ie := newIsolation(t)
	number := ie.bProject.str("projectNumber")
	for _, ref := range []string{"bravo-app", number} {
		// Each request carries a valid body, so it reaches the ownership check.
		console := []struct {
			method, path string
			body         any
		}{
			{"GET", "/console/v1/projects/" + ref, nil},
			{"PATCH", "/console/v1/projects/" + ref, map[string]any{"displayName": "pwned"}},
			{"POST", "/console/v1/projects/" + ref + ":delete", map[string]any{"confirmProjectId": "bravo-app"}},
			{"POST", "/console/v1/projects/" + ref + ":restore", nil},
			{"GET", "/console/v1/projects/" + ref + "/apps", nil},
			{"POST", "/console/v1/projects/" + ref + "/apps", map[string]any{"platform": "WEB"}},
			{"GET", "/console/v1/projects/" + ref + "/apps/" + ie.bApps["webApps"] + "/config", nil},
			{"GET", "/console/v1/projects/" + ref + "/apiKeys", nil},
			{"GET", "/console/v1/projects/" + ref + "/serviceAccounts", nil},
			{"POST", "/console/v1/projects/" + ref + "/serviceAccounts", map[string]any{"accountId": "intruder"}},
			{"GET", "/console/v1/projects/" + ref + "/auditLog", nil},
			{"GET", "/console/v1/projects/" + ref + "/adminSdkConfig", nil},
		}
		for _, c := range console {
			expect(t, ie.a.do(c.method, c.path, c.body), 404, "NOT_FOUND")
		}
		mgmt := []struct{ method, path string }{
			{"GET", "/v1beta1/projects/" + ref},
			{"PATCH", "/v1beta1/projects/" + ref + "?updateMask=displayName"},
			{"GET", "/v1beta1/projects/" + ref + ":searchApps"},
			{"POST", "/v1beta1/projects/" + ref + "/webApps"},
			{"GET", "/v1beta1/projects/" + ref + "/adminSdkConfig"},
		}
		for _, c := range mgmt {
			expect(t, ie.aCLI.do(c.method, c.path, map[string]any{"displayName": "pwned"}), 404, "NOT_FOUND")
		}
	}
	// The "-" wildcard resolves B's apps through their own project, which A does not own.
	for coll, id := range ie.bApps {
		expect(t, ie.aCLI.do("GET", "/v1beta1/projects/-/"+coll+"/"+id+"/config", nil), 404, "NOT_FOUND")
		expect(t, ie.aCLI.do("POST", "/v1beta1/projects/-/"+coll+"/"+id+":remove", map[string]any{}), 404, "NOT_FOUND")
	}
	// B's project is untouched.
	p := ie.b.mustOK(ie.b.do("GET", "/console/v1/projects/bravo-app", nil))
	if p.str("displayName") != "Bravo-app" || p.str("state") != "ACTIVE" {
		t.Fatalf("B's project changed: %s", p.Body)
	}
	if n := len(ie.b.mustOK(ie.b.do("GET", "/console/v1/projects/bravo-app/apps", nil)).list("apps")); n != 3 {
		t.Fatalf("B has %d apps", n)
	}
}

// A service account token of A is refused on B's project.
func TestIsolationServiceAccountToken(t *testing.T) {
	ie := newIsolation(t)
	tokenA, _, err := exchange(t, ie.aKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	sa := ie.withBearer(tokenA)
	sa.mustOK(sa.do("GET", "/v1beta1/projects/alpha-app", nil))
	expect(t, sa.do("GET", "/v1beta1/projects/bravo-app", nil), 404, "NOT_FOUND")
	expect(t, sa.do("GET", "/v1beta1/projects/"+ie.bProject.str("projectNumber")+":searchApps", nil), 404, "NOT_FOUND")
	expect(t, sa.do("GET", "/v1beta1/projects/-/webApps/"+ie.bApps["webApps"]+"/config", nil), 404, "NOT_FOUND")
	if list := sa.mustOK(sa.do("GET", "/v1beta1/projects", nil)).list("results"); len(list) != 1 {
		t.Fatalf("service account lists %d projects", len(list))
	}
}

// A's config files carry no value of B, even with identical package names in both projects.
func TestIsolationConfigFiles(t *testing.T) {
	ie := newIsolation(t)
	bValues := []string{"bravo-app", ie.bProject.str("projectNumber")}
	for _, id := range ie.bApps {
		bValues = append(bValues, id)
	}
	for _, k := range ie.b.mustOK(ie.b.do("GET", "/console/v1/projects/bravo-app/apiKeys", nil)).list("keys") {
		bValues = append(bValues, k["keyString"].(string))
	}
	for coll, id := range ie.aApps {
		body := string(ie.a.mustOK(ie.a.do("GET", "/console/v1/projects/alpha-app/apps/"+id+"/config", nil)).Body)
		for _, v := range bValues {
			if strings.Contains(body, v) {
				t.Errorf("%s config of A contains B's %q", coll, v)
			}
		}
	}
	for _, v := range bValues {
		if strings.Contains(string(ie.aKeyFile), v) {
			t.Errorf("A's key file contains B's %q", v)
		}
	}
}

// Moving an app, key or SHA between projects by editing a request is refused.
func TestIsolationCannotMoveResources(t *testing.T) {
	ie := newIsolation(t)
	// A's app id under A's own project path, but B's app id: not found.
	expect(t, ie.a.do("GET", "/console/v1/projects/alpha-app/apps/"+ie.bApps["webApps"], nil), 404, "NOT_FOUND")
	expect(t, ie.a.do("PATCH", "/console/v1/projects/alpha-app/apps/"+ie.bApps["webApps"], map[string]any{"displayName": "x"}), 404, "NOT_FOUND")
	expect(t, ie.aCLI.do("GET", "/v1beta1/projects/alpha-app/webApps/"+ie.bApps["webApps"], nil), 404, "NOT_FOUND")
	expect(t, ie.a.do("POST", "/console/v1/projects/alpha-app/apps/"+ie.bApps["androidApps"]+"/sha", map[string]any{"shaHash": strings.Repeat("ab", 20)}), 404, "NOT_FOUND")

	bKey := ie.b.mustOK(ie.b.do("GET", "/console/v1/projects/bravo-app/apiKeys", nil)).list("keys")[0]["uid"].(string)
	expect(t, ie.a.do("POST", "/console/v1/projects/alpha-app/apiKeys/"+bKey+":rotate", nil), 404, "NOT_FOUND")
	expect(t, ie.a.do("POST", "/console/v1/projects/alpha-app/apiKeys/"+bKey+":disable", nil), 404, "NOT_FOUND")

	bSA := ie.b.mustOK(ie.b.do("GET", "/console/v1/projects/bravo-app/serviceAccounts", nil)).list("serviceAccounts")[0]["id"].(string)
	expect(t, ie.a.do("POST", "/console/v1/projects/alpha-app/serviceAccounts/"+bSA+"/keys", nil), 404, "NOT_FOUND")
	expect(t, ie.a.do("DELETE", "/console/v1/projects/alpha-app/serviceAccounts/"+bSA, nil), 404, "NOT_FOUND")
}

// Deleting and restoring A leaves B alone; both projects may use the same package name.
func TestIsolationDeleteRestore(t *testing.T) {
	ie := newIsolation(t)
	ie.a.mustOK(ie.a.do("POST", "/console/v1/projects/alpha-app:delete", map[string]any{"confirmProjectId": "alpha-app"}))
	ie.b.mustOK(ie.b.do("GET", "/console/v1/projects/bravo-app/apps/"+ie.bApps["androidApps"]+"/config", nil))
	tokenB, _, err := exchange(t, ie.bKeyFile)
	if err != nil {
		t.Fatalf("B's service account broken by A's deletion: %v", err)
	}
	ie.withBearer(tokenB).mustOK(ie.withBearer(tokenB).do("GET", "/v1beta1/projects/bravo-app", nil))
	if _, _, err := exchange(t, ie.aKeyFile); err == nil {
		t.Fatal("deleted project's service account still exchanges")
	}
	ie.a.mustOK(ie.a.do("POST", "/console/v1/projects/alpha-app:restore", nil))
	if n := len(ie.b.mustOK(ie.b.do("GET", "/console/v1/projects/bravo-app/apps", nil)).list("apps")); n != 3 {
		t.Fatalf("B has %d apps after A's restore", n)
	}
}
