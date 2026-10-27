package webchannel_test

import (
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
)

// The handshake is a form POST a page sends with no preflight, its headers in headers=: Bearer
// owner there, authorised by the loopback peer alone, would make any page in the machine's
// browser an admin. A handshake with an Origin is refused it, as REST is.
func TestOwnerRefusedFromBrowser(t *testing.T) {
	x := newHarness(t, options{rules: apitest.Options{DevOwnerToken: true}})
	x.commit(t, "aruma-ref-wc-owner/a", 1)
	c, _ := handshake(t, x.url, map[string]string{"Authorization": "Bearer owner"}, addDoc(1, "aruma-ref-wc-owner/a"))
	c.back("0").wantStatus(t, "UNAUTHENTICATED ")
}
