package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The production goldens are what production Firebase sent a raw client on 2026-10-21
// (testdata/golden/production/README.md): every frame is one this package encodes field for
// field, as for the emulator's, and a cancelled listen is named as production names it.

type productionGolden struct {
	Scenario string        `json:"scenario"`
	Source   string        `json:"source"`
	Project  string        `json:"project"`
	Captured string        `json:"captured"`
	Run      string        `json:"run"`
	Frames   []goldenFrame `json:"frames"`
}

func loadProductionGoldens(t *testing.T) map[string]productionGolden {
	t.Helper()
	files, err := filepath.Glob("testdata/golden/production/*.json")
	if err != nil || len(files) < 10 {
		t.Fatalf("production goldens: %v %d", err, len(files))
	}
	out := map[string]productionGolden{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var g productionGolden
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		// The project, the user and the tokens are redacted; sdk.json has no frames.
		if g.Source != "production" || g.Project != "<redacted>" || g.Captured == "" {
			t.Errorf("%s: provenance %q %q %q", f, g.Source, g.Project, g.Captured)
		}
		out[filepath.Base(f)] = g
	}
	return out
}

func TestProductionGoldenServerFrames(t *testing.T) {
	seen := map[string]int{}
	for name, g := range loadProductionGoldens(t) {
		for i, f := range g.Frames {
			if f.Dir != "recv" || f.Text == "" {
				continue
			}
			if _, ok := frameCount([]byte(f.Text)); ok {
				continue
			}
			kind, ours := reencode(t, f.Text)
			seen[kind]++
			if !sameJSON(t, []byte(f.Text), ours) {
				t.Errorf("%s frame %d (%s):\n production %s\n ours       %s", name, i, kind, f.Text, ours)
			}
		}
	}
	for _, kind := range []string{"hello", "pong", "error", "response", "push d", "push c"} {
		if seen[kind] == 0 {
			t.Errorf("no production %s frame was checked", kind)
		}
	}
}

// After unauth, production cancels each listen the rules no longer allow with a c push that
// names its query with sin and ein filled in, or null for a query without bounds or limit
// (production/cancel.json). EncodeListenRevoked, given each query as the client sent it,
// must give the same pushes, in any order.
func TestProductionCancelledQueries(t *testing.T) {
	g := loadProductionGoldens(t)["cancel.json"]
	var want, got []string
	unauth := -1
	for i, f := range g.Frames {
		if f.Conn != "A" {
			continue
		}
		in, err := Parse([]byte(f.Text))
		if f.Dir == "send" && err == nil && in.Request != nil {
			switch in.Request.Action {
			case ActionListen:
				var b ListenBody
				if err := DecodeBody(in.Request, &b); err != nil {
					t.Fatal(err)
				}
				got = append(got, string(EncodeListenRevoked(b.Path, b.Query)))
			case ActionUnauth:
				unauth = i
			}
		}
		if unauth >= 0 && f.Dir == "recv" && isRevokePush(f.Text) {
			want = append(want, canonical(t, f.Text))
		}
		if unauth >= 0 && f.Dir == "recv" && !isRevokePush(f.Text) {
			break // the reply to unauth comes after the pushes
		}
	}
	if unauth < 0 || len(want) == 0 {
		t.Fatalf("cancel.json: unauth at %d, %d pushes", unauth, len(want))
	}
	// Only the listens made before unauth were cancelled.
	got = got[:len(want)]
	for i := range got {
		got[i] = canonical(t, got[i])
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("c pushes:\n production %q\n ours       %q", want, got)
	}
}

func isRevokePush(text string) bool {
	var m struct {
		D struct {
			A string `json:"a"`
		} `json:"d"`
	}
	return json.Unmarshal([]byte(text), &m) == nil && m.D.A == PushListenRevoked
}

// canonical re-marshals JSON so that key order does not matter.
func canonical(t *testing.T, text string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
