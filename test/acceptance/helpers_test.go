package acceptance

import (
	"strings"
	"testing"
)

// project creates a project through the console and returns its JSON.
func (c *Client) project(id string) *Response {
	c.env.t.Helper()
	return c.mustOK(c.do("POST", "/console/v1/projects", map[string]any{"projectId": id, "displayName": strings.ToUpper(id[:1]) + id[1:]}))
}

// app registers an app and returns its JSON.
func (c *Client) app(project, platform, namespace string) *Response {
	c.env.t.Helper()
	body := map[string]any{"platform": platform, "displayName": platform + " app"}
	switch platform {
	case "ANDROID":
		body["packageName"] = namespace
	case "IOS":
		body["bundleId"] = namespace
	}
	return c.mustOK(c.do("POST", "/console/v1/projects/"+project+"/apps", body))
}

// pat creates a personal CLI token for the client's account.
func (c *Client) pat() string {
	c.env.t.Helper()
	return c.mustOK(c.do("POST", "/console/v1/me/tokens", map[string]any{"name": "cli", "currentPassword": testPassword})).str("token")
}

// serviceAccountKey creates a service account and returns its key file.
func (c *Client) serviceAccountKey(project, name string) (saID string, keyFile []byte) {
	c.env.t.Helper()
	sa := c.mustOK(c.do("POST", "/console/v1/projects/"+project+"/serviceAccounts", map[string]any{"accountId": name}))
	key := c.mustOK(c.do("POST", "/console/v1/projects/"+project+"/serviceAccounts/"+sa.str("id")+"/keys", nil))
	return sa.str("id"), key.Body
}

func assertEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}
