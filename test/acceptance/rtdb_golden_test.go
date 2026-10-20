package acceptance

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRTDBMatchesEmulator replays the scenarios recorded from the official emulator 4.11.2
// (internal/services/rtdb/wire/testdata/golden) against Aruma, frame by frame, and compares
// what comes back. Every difference must be one listed in knownDifferences with its reason;
// the test logs them, so the report of a run is the comparison itself.
func TestRTDBMatchesEmulator(t *testing.T) {
	// RTDB_GOLDEN_DIR replays other captures, such as fresh ones not yet committed.
	dir := "../../internal/services/rtdb/wire/testdata/golden"
	if d := os.Getenv("RTDB_GOLDEN_DIR"); d != "" {
		dir = d
	}
	files, err := filepath.Glob(dir + "/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("golden files: %v %d", err, len(files))
	}
	e := rtdbEnv(t, false, nil)
	owner := e.owner()
	pat := owner.pat()
	// Each emulator namespace becomes a project of the same id, lengthened to the 6
	// characters a project id needs, whose default database the scenario uses.
	projects := map[string]bool{}
	database := func(ns string) string {
		if len(ns) < 6 {
			ns += "-test"
		}
		if !projects[ns] {
			owner.project(ns)
			projects[ns] = true
		}
		return ns + "-default-rtdb"
	}

	seen := map[string]bool{}
	compared := 0
	for _, f := range files {
		var g struct {
			Scenario    string            `json:"scenario"`
			Connections map[string]string `json:"connections"`
			Setup       []struct {
				Method string          `json:"method"`
				Path   string          `json:"path"`
				Body   json.RawMessage `json:"body"`
			} `json:"setup"`
			Frames []struct {
				Conn string `json:"conn"`
				Dir  string `json:"dir"`
				Text string `json:"text"`
				Len  int    `json:"len"`
			} `json:"frames"`
		}
		b, _ := os.ReadFile(f)
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(g.Scenario, "lp_") {
			// Long polling is HTTP: TestRTDBLongPoll* replay it, and wire's tests hold the
			// envelope to these captures byte for byte.
			continue
		}
		if reason, skip := skippedScenarios[g.Scenario]; skip {
			t.Logf("scenario %s not replayed: %s", g.Scenario, reason)
			continue
		}
		// The emulator's setup went through REST, which comes in R5: the rules through the
		// rules endpoint, the data through an admin connection.
		for _, s := range g.Setup {
			path, query, _ := strings.Cut(s.Path, "?")
			ns := strings.TrimPrefix(query, "ns=")
			db := database(ns)
			switch {
			case s.Method == "PUT" && path == "/.settings/rules.json":
				req, _ := http.NewRequest("PUT", e.URL+"/.settings/rules.json?ns="+db, strings.NewReader(string(s.Body)))
				req.Header.Set("Authorization", "Bearer "+pat)
				resp, err := http.DefaultClient.Do(req)
				if err != nil || resp.StatusCode != 200 {
					t.Fatalf("%s: rules: %v %v", g.Scenario, err, resp)
				}
				resp.Body.Close()
			case s.Method == "PUT" && strings.HasSuffix(path, ".json"):
				a := e.dial(db, dialOpts{})
				a.call("auth", map[string]any{"cred": pat}).mustOK(t)
				a.call("p", map[string]any{"p": strings.TrimSuffix(path, ".json"), "d": s.Body}).mustOK(t)
				a.close()
			case s.Method == "GET":
				// A read the capture made for its notes (an ETag): nothing to set up.
			default:
				t.Fatalf("%s: setup %s %s", g.Scenario, s.Method, s.Path)
			}
		}
		conns := map[string]*wsClient{}
		conn := func(name string) *wsClient {
			if c := conns[name]; c != nil {
				return c
			}
			ns := regexp.MustCompile(`ns=([a-z0-9-]+)`).FindStringSubmatch(g.Connections[name])[1]
			c := e.dial(database(ns), dialOpts{})
			conns[name] = c
			return c
		}
		// Pushes are compared per connection, in order, apart from the replies: when a push
		// arrives relative to another connection's requests is a matter of timing.
		ourPushes, theirPushes := map[string][]string{}, map[string][]string{}
		for _, fr := range g.Frames {
			if fr.Dir == "recv" && isPush(fr.Text) {
				theirPushes[fr.Conn] = append(theirPushes[fr.Conn], fr.Text)
			}
		}
		sentinel := 900000
		// exchange sends one message (or none) and a sentinel request, which marks the end of
		// what the message produced, and returns the replies; pushes go to ourPushes.
		exchange := func(name string, c *wsClient, msg string) []string {
			if msg != "" {
				c.sendRaw(msg)
			}
			sentinel++
			c.sendRaw(`{"t":"d","d":{"r":` + strconv.Itoa(sentinel) + `,"a":"s","b":{"c":{}}}}`)
			var replies []string
			for {
				m := c.next()
				switch {
				case strings.Contains(m, `"r":`+strconv.Itoa(sentinel)+`,`):
					return replies
				case isPush(m):
					ourPushes[name] = append(ourPushes[name], m)
				default:
					replies = append(replies, m)
				}
			}
		}
		for i := 0; i < len(g.Frames); i++ {
			fr := g.Frames[i]
			switch {
			case fr.Dir == "recv" && strings.Contains(fr.Text, `"t":"h"`):
				conn(fr.Conn) // the handshake: TestRTDBHandshake checks its fields
			case fr.Dir == "close" && fr.Text == "":
				if c := conns[fr.Conn]; c != nil {
					exchange(fr.Conn, c, "") // collect what is still on its way
					c.close()
					delete(conns, fr.Conn)
					// The emulator applied onDisconnect writes about 230 ms after a close.
					time.Sleep(300 * time.Millisecond)
				}
			case fr.Dir == "send" && (fr.Len > 0 || isCount(fr.Text)):
				// Frames stored by length only cannot be replayed (TestRTDBLimits sends its own
				// large messages); skip them and what the emulator answered.
				for i+1 < len(g.Frames) && g.Frames[i+1].Dir != "send" {
					if g.Frames[i+1].Dir == "close" {
						break
					}
					i++
				}
			case fr.Dir == "send":
				ours := exchange(fr.Conn, conn(fr.Conn), fr.Text)
				// What the emulator answered this connection before its next request.
				var theirs []string
				for j := i + 1; j < len(g.Frames) && !(g.Frames[j].Conn == fr.Conn && g.Frames[j].Dir == "send"); j++ {
					if r := g.Frames[j]; r.Conn == fr.Conn && r.Dir == "recv" && r.Text != "" && !isPush(r.Text) && !isCount(r.Text) {
						theirs = append(theirs, r.Text)
					}
				}
				compared++
				check(t, seen, g.Scenario+" "+fr.Conn+" "+requestKey(fr.Text), abbreviate(fr.Text), theirs, ours)
			}
		}
		for name, c := range conns {
			exchange(name, c, "")
		}
		for _, name := range slices.Sorted(maps.Keys(g.Connections)) {
			compared++
			check(t, seen, g.Scenario+" "+name+" pushes", "", theirPushes[name], ourPushes[name])
		}
	}
	if compared < 60 {
		t.Errorf("compared only %d messages", compared)
	}
	var stale []string
	for k := range knownDifferences {
		if !seen[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		t.Errorf("listed difference %q no longer occurs: remove it", k)
	}
}

// check compares what Aruma sent with what the emulator sent, allowing a listed difference.
func check(t *testing.T, seen map[string]bool, key, sent string, theirs, ours []string) {
	t.Helper()
	if sameFrames(theirs, ours) {
		return
	}
	reason, ok := knownDifferences[key]
	if !ok {
		t.Errorf("%s: %s\n emulator %v\n aruma    %v", key, sent, abbreviateAll(theirs), abbreviateAll(ours))
		return
	}
	seen[key] = true
	t.Logf("known difference, %s (%s):\n emulator %v\n aruma    %v", key, reason, abbreviateAll(theirs), abbreviateAll(ours))
}

// skippedScenarios are captures this replay does not cover, with where they are covered.
var skippedScenarios = map[string]string{
	"origin":        "HTTP-level; TestRTDBOrigin and TestRTDBHandshakeHostUnderDomain cover it",
	"revoke_unauth": "needs rules with auth and the emulator's unsigned tokens (R4); TestRTDBUnauthRevokesListens covers the c push",
}

// knownDifferences are the places where Aruma answers otherwise than the emulator, keyed by
// scenario, connection and request (action and r, "frame" for a non-request, or "pushes"
// for a connection's pushes).
var knownDifferences = map[string]string{
	// The emulator answers data it cannot parse with a bare error frame and leaves the request
	// unanswered; Aruma replies with the error status, so a client is never left waiting. The
	// SDKs validate the same rules before sending, so they never see either.
	"limits A p r=1":        "forbidden key: reply instead of an error frame",
	"limits A p r=13":       "data key over 768 bytes: reply instead of an error frame",
	"limits A p r=14":       "data key over 768 bytes: reply instead of an error frame",
	"limits A p r=18":       "the emulator also limits the data keys alone, joined, below 768 bytes; Aruma applies the one documented limit, path and keys together, and accepts",
	"limits A p r=25":       "data nested past 32: reply instead of an error frame",
	"limits A p r=29":       "data nested past 32: reply instead of an error frame",
	"server_values A p r=7": "unknown server value: reply instead of an error frame",
	"server_values A p r=8": "non-number increment: reply instead of an error frame",
	// When a write changes a child that stays in a query's window, the emulator pushes the
	// change as written (q/c/s 2.5, or a merge {"c/s":1}); Aruma pushes the child's new value
	// whole (q/c {"s":2.5}). Both leave the client's view of the window the same.
	"query_child_indexed B pushes": windowPushes,
	"query_child_indexed C pushes": windowPushes,
	"query_child_indexed D pushes": windowPushes,
	"query_child_indexed E pushes": windowPushes,
	"query_child_indexed F pushes": windowPushes,
	"query_child_indexed G pushes": windowPushes,
	"query_key C pushes":           windowPushes,
	"query_key E pushes":           windowPushes,
	// A query on a leaf selects nothing: the emulator answers null for a limit and {} for a
	// key range; Aruma answers null for both, which the SDKs read as the same empty node.
	"query_get A g r=18": "empty query result on a leaf: null instead of {}",
}

const windowPushes = "a child changed inside a query window: pushed whole instead of as written"

// requestKey names a client message: "<action> r=<n>", or "frame".
func requestKey(text string) string {
	var m struct {
		D struct {
			R *int64 `json:"r"`
			A string `json:"a"`
		} `json:"d"`
	}
	if json.Unmarshal([]byte(text), &m) != nil || m.D.R == nil {
		return "frame"
	}
	return fmt.Sprintf("%s r=%d", m.D.A, *m.D.R)
}

func isCount(text string) bool {
	_, err := strconv.Atoi(text)
	return err == nil && len(text) <= 6
}

func isPush(text string) bool {
	var m struct {
		T string `json:"t"`
		D struct {
			R *int64 `json:"r"`
			A string `json:"a"`
		} `json:"d"`
	}
	return json.Unmarshal([]byte(text), &m) == nil && m.T == "d" && m.D.R == nil && m.D.A != ""
}

// timestamps are server times in milliseconds, which differ on every run.
var timestamps = regexp.MustCompile(`\b1[7-9]\d{11}\b`)

// errorIDs are the emulator's error counters ("ClientId[7]:ErrorId[3]: ").
var errorIDs = regexp.MustCompile(`ClientId\[\d+\]:ErrorId\[\d+\]: `)

func sameFrames(theirs, ours []string) bool {
	if len(theirs) != len(ours) {
		return false
	}
	for i := range theirs {
		a := errorIDs.ReplaceAllString(timestamps.ReplaceAllString(theirs[i], "0"), "")
		b := timestamps.ReplaceAllString(ours[i], "0")
		if !sameJSON(a, b) {
			return false
		}
	}
	return true
}

func abbreviate(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func abbreviateAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = abbreviate(s)
	}
	return out
}
