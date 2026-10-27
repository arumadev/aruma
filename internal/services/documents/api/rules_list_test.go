package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The rules' list (api/rules_list.go), judged on production's recorded cases: each list case of
// f5-rules-constraints.json and f5-rules-queries.json, its gRPC request sent as the end user
// under the rules it ran under, must end in the recorded status. The generic replay of every
// f5-rules file is test/differential/documents'; this one runs the API alone.

// rulesCorpus is a file of the corpus; Rules is the source its cases ran under, or the name of
// the file that holds it.
type rulesCorpus struct {
	Rules json.RawMessage `json:"rules"`
	Cases []rulesCase     `json:"cases"`
}

func (c rulesCorpus) source(t *testing.T) string {
	t.Helper()
	var named string
	if json.Unmarshal(c.Rules, &named) == nil {
		return loadRulesCorpus(t, strings.TrimPrefix(named, "corpus/")).source(t)
	}
	var r struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(c.Rules, &r); err != nil || r.Source == "" {
		t.Fatalf("rules: %v", err)
	}
	return r.Source
}

type rulesCase struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Actor       string `json:"actor"`
	Transports  struct {
		GRPC struct {
			Outcome string `json:"outcome"`
			Steps   []struct {
				Outcome string `json:"outcome"`
				Request struct {
					RPC     string          `json:"rpc"`
					Message json.RawMessage `json:"message"`
				} `json:"request"`
			} `json:"steps"`
		} `json:"grpc"`
	} `json:"transports"`
}

func loadRulesCorpus(t *testing.T, name string) rulesCorpus {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", name))
	if err != nil {
		t.Fatal(err)
	}
	var c rulesCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRulesListCorpus(t *testing.T) {
	r := newRulesEnv(t, apitest.Options{})
	s, ctx := r.env.Service, t.Context()
	user := apitest.UserToken(t, "user", nil)
	ran := 0
	for _, file := range []string{"f5-rules-constraints.json", "f5-rules-queries.json"} {
		c := loadRulesCorpus(t, file)
		r.env.PublishFirestoreRules(t, c.source(t))
		for _, k := range c.Cases {
			g := k.Transports.GRPC
			if len(g.Steps) != 1 || k.Actor != "user" {
				continue
			}
			step := g.Steps[0]
			msg := []byte(strings.ReplaceAll(string(step.Request.Message), "{project}", apitest.Project))
			unmarshal := func(m proto.Message) {
				if err := protojson.Unmarshal(msg, m); err != nil {
					t.Fatalf("%s: %v", k.ID, err)
				}
			}
			var err error
			switch step.Request.RPC {
			case "google.firestore.v1.Firestore/RunQuery":
				req := &pb.RunQueryRequest{}
				unmarshal(req)
				err = s.RunQuery(ctx, call(user), req, func(*pb.RunQueryResponse) error { return nil })
			case "google.firestore.v1.Firestore/RunAggregationQuery":
				req := &pb.RunAggregationQueryRequest{}
				unmarshal(req)
				err = s.RunAggregationQuery(ctx, call(user), req, func(*pb.RunAggregationQueryResponse) error { return nil })
			case "google.firestore.v1.Firestore/ListDocuments":
				req := &pb.ListDocumentsRequest{}
				unmarshal(req)
				_, err = s.ListDocuments(ctx, call(user), req)
			default:
				continue
			}
			ran++
			if got := codeOf(err).String(); got != step.Outcome {
				t.Errorf("%s %s (%s): %s, %v; production %s", file, k.ID, k.Description, got, err, step.Outcome)
			}
		}
	}
	t.Logf("%d list cases", ran)
	if ran < 100 {
		t.Errorf("%d list cases ran", ran)
	}
}
