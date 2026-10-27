package webchannel

import (
	"testing"
)

// A chunk's length counts UTF-16 code units, as JavaScript's String.length does.
func TestChunkLength(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{`[]`, "2\n[]"},
		{`"é"`, "3\n\"é\""},
		{`"😀"`, "4\n\"😀\""},
		{"\xff", "1\n\xff"},
	} {
		if got := string(chunk(nil, []byte(tc.text))); got != tc.want {
			t.Errorf("chunk(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
	if got := string(arrays([]array{{id: 3, payload: noop}, {id: 4, payload: message([]byte(`{}`))}})); got != "[[3,[\"noop\"]],[4,[{}\n]]]" {
		t.Errorf("arrays: %s", got)
	}
}

func TestParseForward(t *testing.T) {
	f, err := parseForward([]byte("headers=Authorization%3ABearer%20t%3Ax%0D%0Ax-goog-api-key%3Ak%0D%0A&count=2&ofs=5&req0___data__=%7B%7D&req1___data__=%5B%5D"), 10)
	if err != nil || f.headers.Get("Authorization") != "Bearer t:x" || f.headers.Get("X-Goog-Api-Key") != "k" ||
		len(f.maps) != 2 || f.maps[1].id != 6 || string(f.maps[1].data) != "[]" {
		t.Errorf("parseForward: %v %+v", err, f)
	}
	for _, body := range []string{"count=x", "count=11&ofs=0", "count=1&ofs=-1&req0___data__=1", "count=1&ofs=0", "a=b"} {
		if _, err := parseForward([]byte(body), 10); err == nil {
			t.Errorf("parseForward(%q) accepted", body)
		}
	}
}
