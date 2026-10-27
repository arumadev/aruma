// Package documents is the differential layer of Aruma's Document Database (ADR-028 §7.2): it
// replays recorded production requests against a live Aruma and diffs each answer with the
// recorded one, after normalising what legitimately differs between runs (times, transaction
// ids, page tokens, the project id). A difference is reported as JSON paths, and a differing
// request body can be shrunk to a minimal one (shrink.go).
//
// The replayed requests are the cases a recorder marks `replay: true`: a fixed request whose
// answer depends only on the file's `setup` section (record-f1.mjs). Setup writes go through
// UpdateDocument (PATCH), which F1's server answers; the recorder wrote the same documents by one
// commit. TestReplayCorpusInProcess replays them in every test run against an Aruma in this
// process; against a running server the suite turns on with ARUMA_DOCUMENTS_URL, as
// test/conformance/documents does, and a server that cannot be reached then fails it.
//
// Planned next (plan section 8): shapes.go generates query shapes for the live comparison with
// the emulator, and rawwire.go grows a gRPC sender once the generated protos exist.
package documents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Write is one fixture document of a file's setup: its name (with {project}) and fields.
type Write struct {
	Name   string
	Fields map[string]any
}

// ReplayCase is a recorded request and production's normalised answer.
type ReplayCase struct {
	ID       string
	Method   string
	Path     string // relative to /v1/, with {project}
	Body     any    // nil when the request had none
	HTTP     int
	Response any
}

// ReplayFile is the replayable part of one corpus file.
type ReplayFile struct {
	Name    string
	Project string // the project the recording ran in, which its answers name
	Setup   []Write
	Cases   []ReplayCase
}

type corpusFile struct {
	Header struct {
		Target string `json:"target"`
	} `json:"header"`
	Setup *struct {
		Commits []struct {
			Writes []map[string]json.RawMessage `json:"writes"`
		} `json:"commits"`
	} `json:"setup"`
	Cases []struct {
		ID        string `json:"id"`
		Transport string `json:"transport"`
		Replay    bool   `json:"replay"`
		Request   struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Body   any    `json:"body"`
		} `json:"request"`
		Response struct {
			HTTP int `json:"http"`
			Body any `json:"body"`
		} `json:"response"`
	} `json:"cases"`
}

// LoadReplay reads every corpus file in dir that holds replayable cases.
func LoadReplay(dir string) ([]ReplayFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []ReplayFile
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var f corpusFile
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		rf := ReplayFile{Name: filepath.Base(p)}
		rf.Project, _, _ = strings.Cut(f.Header.Target, " ")
		for _, c := range f.Cases {
			if !c.Replay {
				continue
			}
			if c.Transport != "rest" {
				return nil, fmt.Errorf("%s: %s: only REST cases replay", rf.Name, c.ID)
			}
			rf.Cases = append(rf.Cases, ReplayCase{ID: c.ID, Method: c.Request.Method, Path: c.Request.Path, Body: c.Request.Body, HTTP: c.Response.HTTP, Response: c.Response.Body})
		}
		if len(rf.Cases) == 0 {
			continue
		}
		if f.Setup == nil {
			return nil, fmt.Errorf("%s: replayable cases but no setup section", rf.Name)
		}
		for _, cm := range f.Setup.Commits {
			for _, w := range cm.Writes {
				raw, ok := w["update"]
				if !ok || len(w) != 1 {
					return nil, fmt.Errorf("%s: a setup write that is not a plain update", rf.Name)
				}
				var u struct {
					Name   string         `json:"name"`
					Fields map[string]any `json:"fields"`
				}
				if err := json.Unmarshal(raw, &u); err != nil {
					return nil, fmt.Errorf("%s: %w", rf.Name, err)
				}
				rf.Setup = append(rf.Setup, Write{Name: u.Name, Fields: u.Fields})
			}
		}
		if rf.Project == "" {
			return nil, errors.New(rf.Name + ": no project in header.target")
		}
		out = append(out, rf)
	}
	return out, nil
}
