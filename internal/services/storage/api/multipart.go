package api

import (
	"cmp"
	"io"
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// multipart serves a POST or PUT on /o or /o/{object} with X-Goog-Upload-Protocol: multipart:
// one multipart/related body, its JSON part then the bytes, through wire.NewRelatedReader into
// core.Insert (A §3.9). name is ?name= decoded once, else the path's object (uploadName), and ""
// when neither names it: the JSON part's name may (ADR-023 §3-26).
//
// The router has resolved the bucket, authenticated the request, set the API's CORS headers and
// taken the request's place under upload_requests_per_ip.
//
// The JSON part is read under json_body_bytes and parsed as a start's body is (parseResource);
// the bytes stream to the store as they arrive, never held in memory, and the closing boundary
// may lack its CRLF, as JS sends it (A §3.9, §6.6). ?name= wins over the JSON's name, as in the
// emulator (raw_upload.json), and the content type is the JSON's: the media part's own
// Content-Type is not read, as the emulator does not (every SDK sends contentType in the JSON).
// The reply is 200 and the object's resource; a body that is not one JSON part and one media
// part is 400.
func (a *API) multipart(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	ip := a.clientIP(r)
	if !a.allowStart(w, r, ip) {
		return
	}
	body := a.pace(w, r)
	rel, err := wire.NewRelatedReader(body, r.Header.Get("Content-Type"), a.svc.Limits().JSONBodyBytes)
	if err != nil {
		a.writeBodyError(w, r, body, err)
		return
	}
	res, err := parseResource(rel.JSON)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if name = cmp.Or(name, res.name); name == "" {
		a.writeError(w, r, errNoName)
		return
	}
	media := &countedReader{r: rel.Media}
	o, err := a.svc.Insert(r.Context(), b, p, core.UploadSpec{
		Name: name, Attrs: res.attrs, Declared: -1, Surface: core.SurfaceFirebase, Want: res.want, IP: ip,
	}, media, -1)
	a.received(media.n)
	body.replying()
	if err != nil {
		a.writeBodyError(w, r, body, err)
		return
	}
	writeObject(w, http.StatusOK, o)
}

// countedReader counts the object bytes of a multipart body, its boundaries and JSON aside.
type countedReader struct {
	r io.Reader
	n int64
}

func (c *countedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
