package rest_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
)

// :batchWrite answers a status and a result per write: an applied write's status is empty, a
// failed one's result is.
func TestBatchWriteForm(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	path := "/v1/projects/" + apitest.Project + "/databases/(default)/documents:batchWrite"
	code, _, body := h.do(t, http.MethodPost, path, `{"writes":[
		{"update":{"name":"`+root+`/bw/a","fields":{"a":{"integerValue":"1"}}},"updateTransforms":[{"fieldPath":"ts","setToServerValue":"REQUEST_TIME"}]},
		{"delete":"`+root+`/bw/missing","currentDocument":{"exists":true}}]}`, tok)
	want := regexp.MustCompile(`^\{"writeResults":\[\{"updateTime":"[^"]+","transformResults":\[\{"timestampValue":"[0-9T:-]+(\.\d{3})?Z"\}\]\},\{\}\],` +
		`"status":\[\{\},\{"code":5,"message":"No document to update: ` + regexp.QuoteMeta(root) + `/bw/missing"\}\]\}$`)
	if code != 200 || !want.Match(body) {
		t.Errorf("batchWrite: %d %s", code, body)
	}
	code, _, body = h.do(t, http.MethodPost, path, `{"writes":[{"delete":"`+root+`/bw/a"},{"delete":"`+root+`/bw/a"}]}`, tok)
	if code != 400 {
		t.Errorf("two writes to one document: %d %s", code, body)
	}
}
