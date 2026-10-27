package documents

import "testing"

// The F6 cases: the browser transport. MUST 21 is proven against the server by one WebChannel
// session each of Listen and Write (webchannel.go): handshake, streamed backchannel, forward
// requests. Their bytes are compared with production's recordings by the replay in
// test/differential/documents (webchannel_replay_test.go); here the cases judge a server that
// is running, so they have no recorded half.

const colWeb = "aruma-ref-f6-web{run}"

var f6Cases = []Case{
	// MUST 22: a browser on another origin. The preflight names the headers the web SDK sends and
	// is answered with that origin and credentials (f6-cors.json); so is the request after it.
	{ID: "cors/data-path", Must: []int{22}, Phase: "F6", Steps: []Step{
		Put(colWeb+"/c", map[string]any{"n": 1}),
		{Name: "preflight", Request: Request{Method: "OPTIONS", Path: "{root}/" + colWeb + "/c", NoAuth: true, Header: map[string]string{
			"Origin":                         "https://app.example",
			"Access-Control-Request-Method":  "GET",
			"Access-Control-Request-Headers": "authorization,content-type,google-cloud-resource-prefix,x-firebase-gmpid,x-goog-api-client",
		}}, Expect: []Check{
			HTTP(200),
			Header("Access-Control-Allow-Origin", "https://app.example"),
			Header("Access-Control-Allow-Credentials", "true"),
		}},
		{Name: "get from the origin", Request: Request{Method: "GET", Path: "{root}/" + colWeb + "/c", Header: map[string]string{
			"Origin": "https://app.example",
		}}, Expect: []Check{
			HTTP(200),
			Header("Access-Control-Allow-Origin", "https://app.example"),
			Header("Access-Control-Allow-Credentials", "true"),
			Equal("name", "{root}/"+colWeb+"/c"),
		}},
	}},
	{ID: "webchannel/listen", Must: []int{21}, Phase: "F6", Steps: []Step{
		Put(colWeb+"/a", map[string]any{"n": 1}),
		{Name: "listen session", Request: Channel(ListenChannel,
			// The handshake carries the first add_target; its answer is the ADD, the document, CURRENT
			// and the global snapshot (MUST 2 to 4).
			ChannelSend{Message: map[string]any{"database": "{db}", "addTarget": map[string]any{
				"documents": map[string]any{"documents": []string{"{root}/" + colWeb + "/a"}}, "targetId": 1002}}, Read: 4},
			// A forward request: removing the target is answered REMOVE.
			ChannelSend{Message: map[string]any{"database": "{db}", "removeTarget": 1002}, Read: 1},
		), Expect: []Check{
			Equal("0.targetChange.targetChangeType", "ADD"),
			Equal("0.targetChange.targetIds", []any{1002.0}),
			Equal("1.documentChange.document.name", "{root}/"+colWeb+"/a"),
			Equal("1.documentChange.targetIds", []any{1002.0}),
			Equal("2.targetChange.targetChangeType", "CURRENT"),
			Absent("3.targetChange.targetChangeType"),
			Equal("4.targetChange.targetChangeType", "REMOVE"),
			Timestamps("1.documentChange.document.updateTime", "3.targetChange.readTime"),
		}},
	}},
	{ID: "webchannel/write", Must: []int{21}, Phase: "F6", Steps: []Step{
		{Name: "write session", Request: Channel(WriteChannel,
			// The handshake request names the database and is answered with the stream's id and token
			// (MUST 11); the batch carries that token and is answered with its write results (MUST 12).
			ChannelSend{Message: map[string]any{"database": "{db}"}, Read: 1},
			ChannelSend{Token: true, Read: 1, Message: map[string]any{"writes": []any{map[string]any{
				"update": map[string]any{"name": "{root}/" + colWeb + "/w", "fields": Fields(map[string]any{"n": 7})}}}}},
		), Expect: []Check{
			Absent("0.writeResults"),
			Keys("0", "streamId", "streamToken"),
			Timestamps("1.commitTime", "1.writeResults.0.updateTime"),
		}},
		{Name: "the batch is committed", Request: Request{Method: "GET", Path: "{root}/" + colWeb + "/w"},
			Expect: []Check{HTTP(200), Equal("fields.n.integerValue", "7")}},
	}},
}

// TestF6 runs the F6 cases against Aruma: the WebChannel session needs a running server.
func TestF6(t *testing.T) {
	Run(t, f6Cases)
}
