package acceptance

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// upload_starts_per_minute_per_ip holds the clients' starts, not an admin's (ADR-022 §17,
// ADR-023 §3-81): an Admin SDK import runs from one address, and Node's save() without a
// precondition does not retry, so the per-address rate would fail it. With a limit of 3 a
// minute, the project's service account and the owner's CLI token start ten uploads each on
// /v0, and the service account ten on GCS; a user then gets three and the 503 with Retry-After,
// an anonymous client the same 503, and the admins still start, the address's budget spent.
func TestStorageStartRateSparesAdmins(t *testing.T) {
	e, owner := newUploadProject(t, map[string]string{"ARUMA_STORAGE_UPLOAD_STARTS_PER_MINUTE_PER_IP": "3"})
	users := e.rtdbUsers(owner)
	users.create("chat-app", "alice")
	e.alice = users.signIn("chat-app", "alice").str("idToken")
	_, key := owner.serviceAccountKey("chat-app", "storage-admin")
	sa, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	pat := owner.pat()
	n := 0
	v0 := func(auth string) v0Reply {
		n++
		return e.call("POST", e.list()+fmt.Sprintf("?name=public%%2Fr%d", n), auth, "x", map[string]string{"Content-Type": "text/plain"})
	}
	gcs := func() int {
		n++
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=g%d", e.URL, e.bucket.Name, n),
			strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer "+sa)
		code, _ := send(t, req)
		return code
	}
	admins := func(what string) {
		t.Helper()
		for i := range 10 {
			e.expect(v0("Bearer "+sa), 200, fmt.Sprintf("%s: service account start %d on /v0", what, i))
			e.expect(v0("Bearer "+pat), 200, fmt.Sprintf("%s: CLI token start %d on /v0", what, i))
			if code := gcs(); code != 200 {
				t.Fatalf("%s: service account start %d on GCS: %d", what, i, code)
			}
		}
	}
	admins("before the clients")
	for i := range 3 {
		e.expect(v0("Firebase "+e.alice), 200, fmt.Sprintf("user start %d", i))
	}
	for _, auth := range []string{"Firebase " + e.alice, ""} {
		r := v0(auth)
		if r.status != 503 || r.header.Get("Retry-After") == "" {
			t.Fatalf("a client start past the limit (%q): %d %v %s", auth, r.status, r.header, r.body)
		}
	}
	admins("after the clients")
}
