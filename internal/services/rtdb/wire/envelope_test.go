package wire

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		msg     string
		control string
		action  string
		id      int64
	}{
		{`{"t":"c","d":{"t":"p","d":{}}}`, "p", "", 0},
		{`{"t":"d","d":{"r":1,"a":"s","b":{"c":{"sdk.js.10-0-0":1}}}}`, "", "s", 1},
		{`{"t":"d","d":{"r":0,"a":"q","b":{"p":"a/b","h":""}}}`, "", "q", 0}, // Android counts from 0
		{`{"t":"d","d":{"r":42,"a":"unauth","b":{}}}`, "", "unauth", 42},
	}
	for _, c := range cases {
		in, err := Parse([]byte(c.msg))
		if err != nil {
			t.Errorf("%s: %v", c.msg, err)
			continue
		}
		switch {
		case c.control != "":
			if in.Control == nil || in.Control.Type != c.control || in.Request != nil {
				t.Errorf("%s: %+v", c.msg, in)
			}
		default:
			if in.Request == nil || in.Request.Action != c.action || in.Request.ID != c.id || in.Control != nil {
				t.Errorf("%s: %+v", c.msg, in)
			}
		}
	}

	for _, bad := range []string{
		`hello`, `{}`, `{"t":"x","d":{}}`, `{"t":"c","d":{}}`, `{"t":"d","d":{"a":"p","b":{}}}`,
		`{"t":"d","d":{"r":1,"b":{}}}`, `{"t":"d","d":{"r":"1","a":"p"}}`, `[1]`,
	} {
		if _, err := Parse([]byte(bad)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", bad, err)
		}
	}
}

func TestDecodeBody(t *testing.T) {
	req := func(a, b string) *Request { return &Request{ID: 1, Action: a, Body: json.RawMessage(b)} }

	var w WriteBody
	if err := DecodeBody(req("p", `{"p":"/a","d":{"x":1},"h":"abc="}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.Path != "/a" || string(w.Data) != `{"x":1}` || w.Hash == nil || *w.Hash != "abc=" {
		t.Errorf("put body: %+v", w)
	}
	w = WriteBody{}
	if err := DecodeBody(req("p", `{"p":"/a","d":null}`), &w); err != nil || string(w.Data) != "null" || w.Hash != nil {
		t.Errorf("delete keeps d=null: %+v %v", w, err)
	}
	if err := DecodeBody(req("p", `{"p":"/a"}`), &WriteBody{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("put without d: %v", err)
	}
	if err := DecodeBody(req("q", ``), &ListenBody{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("missing body: %v", err)
	}

	var l ListenBody
	if err := DecodeBody(req("q", `{"p":"/qq","q":{"i":"s","l":2,"vf":"l"},"t":1,"h":""}`), &l); err != nil {
		t.Fatal(err)
	}
	if l.Path != "/qq" || l.Tag == nil || *l.Tag != 1 || l.Query.OrderBy() != "s" || *l.Query.Limit != 2 {
		t.Errorf("listen body: %+v", l)
	}

	var a AuthBody
	if err := DecodeBody(req("gauth", `{"cred":"tok","authvar":{"uid":"x"}}`), &a); err != nil || a.Credential != "tok" || string(a.AuthVar) != `{"uid":"x"}` {
		t.Errorf("auth body: %+v %v", a, err)
	}
}

func TestEncodeResponse(t *testing.T) {
	cases := []struct {
		r    Reply
		want string
	}{
		// Shapes as the emulator sends them (B-emulator-oracle.md §1.3-1.4).
		{OK(), `{"t":"d","d":{"r":3,"b":{"s":"ok","d":""}}}`},
		{OKWith(map[string]any{}), `{"t":"d","d":{"r":3,"b":{"s":"ok","d":{}}}}`},
		{OKWith(json.RawMessage(`{"a":1}`)), `{"t":"d","d":{"r":3,"b":{"s":"ok","d":{"a":1}}}}`},
		{Fail(StatusDataStale, "Transaction ETag does not match"), `{"t":"d","d":{"r":3,"b":{"s":"datastale","d":"Transaction ETag does not match"}}}`},
		// No HTML escaping.
		{OKWith(json.RawMessage(`"<a&b>"`)), `{"t":"d","d":{"r":3,"b":{"s":"ok","d":"<a&b>"}}}`},
	}
	for _, c := range cases {
		if got := string(EncodeResponse(3, c.r)); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}

func TestControlFrames(t *testing.T) {
	cases := map[string][]byte{
		`{"t":"c","d":{"t":"h","d":{"ts":1792507779527,"v":"5","h":"127.0.0.1:9400","s":"GdAQ4HP6X0SOwGPPHTbRJoKaNZrGYU3u"}}}`: EncodeHello(Hello{Timestamp: 1792507779527, Host: "127.0.0.1:9400", Session: "GdAQ4HP6X0SOwGPPHTbRJoKaNZrGYU3u"}),
		`{"t":"c","d":{"t":"o","d":null}}`:          EncodePong(),
		`{"t":"c","d":{"t":"s","d":"going away"}}`:  EncodeShutdown("going away"),
		`{"t":"c","d":{"t":"r","d":"s-host:1"}}`:    EncodeReset("s-host:1"),
		`{"t":"c","d":{"t":"e","d":"bad message"}}`: EncodeError("bad message"),
	}
	for want, got := range cases {
		if string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	}
}

func TestPushes(t *testing.T) {
	tag := int64(1)
	cases := map[string][]byte{
		`{"t":"d","d":{"a":"d","b":{"p":"x","d":{"a":1}}}}`:                EncodeDataPush("/x", json.RawMessage(`{"a":1}`), nil),
		`{"t":"d","d":{"a":"d","b":{"p":"h","d":null}}}`:                   EncodeDataPush("h", nil, nil),
		`{"t":"d","d":{"a":"d","b":{"p":"","d":5}}}`:                       EncodeDataPush("/", json.RawMessage(`5`), nil),
		`{"t":"d","d":{"a":"m","b":{"p":"f","d":{"x":null,"w":9},"t":1}}}`: EncodeMergePush("/f", json.RawMessage(`{"x":null,"w":9}`), &tag),
		`{"t":"d","d":{"a":"c","b":{"p":"users/u1","q":null}}}`:            EncodeListenRevoked("/users/u1", nil),
		`{"t":"d","d":{"a":"ac","b":{"s":"invalid_token","d":"revoked"}}}`: EncodeAuthRevoked(StatusInvalidToken, "revoked"),
	}
	for want, got := range cases {
		if string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	}
}

func TestQuery(t *testing.T) {
	var q *Query
	if !q.IsDefault() || q.OrderBy() != IndexPriority {
		t.Error("nil query is the default query")
	}
	cases := []struct {
		json    string
		dflt    bool
		invalid bool
	}{
		{`{}`, true, false},
		{`{"i":".priority"}`, true, false},
		{`{"i":".key"}`, false, false},
		{`{"l":2,"vf":"r"}`, false, false},
		{`{"sp":null,"sin":true}`, false, false},
		{`{"sp":3,"sn":"a","ep":"z","en":"b","ein":false}`, false, false},
		{`{"l":0,"vf":"l"}`, false, true},
		{`{"l":1,"vf":"x"}`, false, true},
		{`{"vf":"l"}`, true, true},
	}
	for _, c := range cases {
		var q Query
		if err := json.Unmarshal([]byte(c.json), &q); err != nil {
			t.Fatal(err)
		}
		if q.IsDefault() != c.dflt {
			t.Errorf("%s: IsDefault = %v", c.json, q.IsDefault())
		}
		if err := q.Validate(); (err != nil) != c.invalid {
			t.Errorf("%s: Validate = %v", c.json, err)
		}
		// Round trip keeps exactly what was set, including a null start value.
		var back map[string]any
		var orig map[string]any
		json.Unmarshal(encode(&q), &back)
		json.Unmarshal([]byte(c.json), &orig)
		if !reflect.DeepEqual(back, orig) {
			t.Errorf("%s round-tripped to %s", c.json, encode(&q))
		}
	}
}
