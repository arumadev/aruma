package rtdbkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// REST calls the Realtime Database's REST API of one namespace, on the main host with ?ns=.
type REST struct {
	HC   *http.Client
	Base string
	NS   string
}

// URL is the REST URL of path (with or without slashes) with extra query parameters.
func (r REST) URL(path string, query url.Values) string {
	q := url.Values{"ns": {r.NS}}
	for k, v := range query {
		q[k] = v
	}
	return r.Base + "/" + strings.Trim(path, "/") + ".json?" + q.Encode()
}

// Do sends method to path with body (nil for none, else JSON-encoded unless it is already
// json.RawMessage) and returns the status and the response body.
func (r REST) Do(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		raw, ok := body.(json.RawMessage)
		if !ok {
			var err error
			if raw, err = json.Marshal(body); err != nil {
				return 0, nil, err
			}
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL(path, query), rd)
	if err != nil {
		return 0, nil, err
	}
	resp, err := r.HC.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}
