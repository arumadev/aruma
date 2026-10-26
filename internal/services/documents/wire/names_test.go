package wire

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
)

const root = "projects/" + corpusProject + "/databases/(default)/documents"

func TestParseNameRecorded(t *testing.T) {
	// Production's texts for names that do not parse (values-validate.json ref.*,
	// values-order.json r-c-id-*), and the ids it accepts.
	cases := []struct {
		name, want string // want "" is accepted
	}{
		{root + "/c/a", ""},
		{root + "/c/A", ""},
		{root + "/c/a/s/x", ""},
		{root + "/c-b/x", ""},
		{"projects/" + corpusProject + "/databases/aruma-ref-other/documents/c/a", ""},
		{"projects/aruma-ref-other-project/databases/(default)/documents/c/a", ""},
		{root + "/c/__id10__", ""},
		{root + "/c/__id5__", ""},
		{root + "/c/__id-1__", ""},
		{root + "/c/__id-9223372036854775808__", ""},
		{root + "/c/__id9223372036854775807__", ""},
		{root + "/c", `Document parent name "` + root + `/c" lacks "/" at index 54.`},
		{"c/a", `Document parent name "c/a" lacks "projects" at index 0.`},
		{root + "/c//a", `Document parent name "` + root + `/c//a" lacks a resource id at index 55.`},
		{root + "/c/__x__", `Resource id "__x__" is invalid because it is reserved.`},
		{root + "/c/" + strings.Repeat("i", 1501), "The key path element name is longer than 1500 bytes."},
		{"projects/" + corpusProject + "/databases/Bad DB/documents/c/a", `Illegal string "Bad DB" in database.`},
		{root + "/c/__id05__", longID("__id05__")},
		{root + "/c/__id-0__", longID("__id-0__")},
		{root + "/c/__id+5__", longID("__id+5__")},
		{root + "/c/__id__", longID("__id__")},
		{root + "/c/__id9223372036854775808__", longID("__id9223372036854775808__")},
	}
	for _, c := range cases {
		n, err := ParseReference(c.name)
		if got := errText(err); got != c.want {
			t.Errorf("ParseReference(%.80q) = %q, want %q", c.name, got, c.want)
			continue
		}
		if err == nil && n.String() != c.name {
			t.Errorf("ParseReference(%q).String() = %q", c.name, n.String())
		}
		if err != nil && From(err).Code != CodeInvalidArgument {
			t.Errorf("ParseReference(%q): code %v", c.name, From(err).Code)
		}
	}
}

func longID(id string) string {
	return "A long document ID string must be formatted like '__id{LONG}__', where LONG is a 64-bit integer. Found: '" + id + "'."
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return From(err).Message
}

func TestParseNameKinds(t *testing.T) {
	db := Database{corpusProject, DefaultDatabase}
	cases := []struct {
		name                     string
		document, parent, anyOK  bool
		isRoot, isColl, isDocKnd bool
	}{
		{root, false, true, true, true, false, false},
		{root + "/c", false, false, true, false, true, false},
		{root + "/c/d", true, true, true, false, false, true},
		{root + "/c/d/s", false, false, true, false, true, false},
		{root + "/c/d/s/e", true, true, true, false, false, true},
		{root + "/", false, false, false, false, false, false},
		{root + "/c/", false, false, false, false, false, false},
		{root + "x/c/d", false, false, false, false, false, false},
		{"projects/p/databases/(default)", false, false, false, false, false, false},
		{"projects//databases/(default)/documents", false, false, false, false, false, false},
	}
	for _, c := range cases {
		_, errD := ParseDocumentName(c.name)
		_, errP := ParseParentName(c.name)
		n, errA := ParseName(c.name)
		if (errD == nil) != c.document || (errP == nil) != c.parent || (errA == nil) != c.anyOK {
			t.Errorf("%q: document %v parent %v any %v", c.name, errD, errP, errA)
			continue
		}
		if errA == nil {
			if n.IsRoot() != c.isRoot || n.IsCollection() != c.isColl || n.IsDocument() != c.isDocKnd {
				t.Errorf("%q: root %v collection %v document %v", c.name, n.IsRoot(), n.IsCollection(), n.IsDocument())
			}
			if n.Database != db || n.String() != c.name {
				t.Errorf("%q: parsed %+v, formats as %q", c.name, n, n.String())
			}
		}
	}
	// Breaks in the form, each at its index.
	for _, c := range []struct{ name, want string }{
		{"projects", `Document name "projects" lacks "/" at index 8.`},
		{"projects/p", `Document name "projects/p" lacks "/" at index 10.`},
		{"projects/p/dbs/x", `Document name "projects/p/dbs/x" lacks "databases" at index 11.`},
		{"projects/p/databases/(default)", `Document name "projects/p/databases/(default)" lacks "/" at index 30.`},
		{"projects/p/databases/(default)/docs", `Document name "projects/p/databases/(default)/docs" lacks "documents" at index 31.`},
		{"projects/p/databases/(default)/documents/é/d/", `Document name "projects/p/databases/(default)/documents/é/d/" lacks a resource id at index 45.`},
		{"projects/p/databases/(default)/documents/😀//", `Document name "projects/p/databases/(default)/documents/😀//" lacks a resource id at index 44.`},
	} {
		if _, err := ParseDocumentName(c.name); errText(err) != c.want {
			t.Errorf("ParseDocumentName(%q) = %q, want %q", c.name, errText(err), c.want)
		}
	}
}

func TestParseNameLimits(t *testing.T) {
	segs := func(pairs int) string {
		var b strings.Builder
		b.WriteString(root)
		for range pairs {
			b.WriteString("/c/d")
		}
		return b.String()
	}
	if _, err := ParseDocumentName(segs(100)); err != nil {
		t.Errorf("100 pairs: %v", err)
	}
	if _, err := ParseDocumentName(segs(101)); errText(err) != "Key path is too long. Cannot exceed 100 elements." {
		t.Errorf("101 pairs: %v", err)
	}
	// A collection at the 101st level has no room for documents.
	if _, err := ParseName(segs(100) + "/c"); errText(err) != "Key path is too long. Cannot exceed 100 elements." {
		t.Errorf("collection at 101: %v", err)
	}
	// Reserved collection ids, and __id…__ as a collection id.
	for _, id := range []string{"__x__", "__id5__", "_____"} {
		if _, err := ParseParentName(root + "/" + id + "/d"); errText(err) != `Resource id "`+id+`" is invalid because it is reserved.` {
			t.Errorf("collection %q: %v", id, err)
		}
	}
	for _, id := range []string{"___", "____", "__a_", "a__b__"} {
		if _, err := ParseDocumentName(root + "/c/" + id); err != nil {
			t.Errorf("id %q: %v", id, err)
		}
	}
	for _, db := range []string{"abcd", "a-b-c-d", "a123", strings.Repeat("a", 63), DefaultDatabase} {
		if _, err := ParseDatabaseName("projects/p/databases/" + db); err != nil {
			t.Errorf("database %q: %v", db, err)
		}
	}
	for _, db := range []string{"abc", "Abcd", "1abc", "abc-", "ab_c", strings.Repeat("a", 64), "(other)", "a b c"} {
		if _, err := ParseDatabaseName("projects/p/databases/" + db); errText(err) != `Illegal string "`+db+`" in database.` {
			t.Errorf("database %q: %v", db, err)
		}
	}
}

func TestNameNavigation(t *testing.T) {
	n, err := ParseDocumentName(root + "/c/a/s/x")
	if err != nil {
		t.Fatal(err)
	}
	if n.ID() != "x" || n.RelativePath() != "c/a/s/x" {
		t.Errorf("ID %q, relative %q", n.ID(), n.RelativePath())
	}
	coll := n.Parent()
	if !coll.IsCollection() || coll.String() != root+"/c/a/s" {
		t.Errorf("parent %q", coll)
	}
	if p := coll.Parent(); !p.IsDocument() || p.String() != root+"/c/a" {
		t.Errorf("grandparent %q", p)
	}
	if r := n.Parent().Parent().Parent().Parent(); !r.IsRoot() || r.String() != root || !r.Parent().IsRoot() {
		t.Errorf("root %q", r)
	}
	// Child does not write into its parent's path.
	a := coll.Child("y")
	b := coll.Child("z")
	if a.ID() != "y" || b.ID() != "z" || n.ID() != "x" {
		t.Errorf("children %q %q, original %q", a, b, n)
	}
}

// f0Case is one case of f0-errors.json or f0-missing-database.json.
type f0Case struct {
	ID        string `json:"id"`
	Transport string `json:"transport"`
	Request   struct {
		Method         string          `json:"method"`
		Path           string          `json:"path"`
		Body           any             `json:"body"`
		Message        json.RawMessage `json:"message"`
		RPC            string          `json:"rpc"`
		ResourcePrefix string          `json:"resourcePrefix"`
	} `json:"request"`
	Error *struct {
		HTTP    int    `json:"http"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
	Response struct {
		Body        any  `json:"body"`
		BodyIsArray bool `json:"bodyIsArray"`
		HTTP        int  `json:"http"`
	} `json:"response"`
}

// nameMessages are the recorded errors names get before anything else is looked at.
var nameMessages = map[string]bool{
	"The key path element name is longer than 1500 bytes.": true,
	"Key path is too long. Cannot exceed 100 elements.":    true,
	"The document name is longer than 6144 bytes.":         true,
}

// TestBindNamesF0 replays f0-errors.json's name cases (ids of 1500 and 1501 bytes, names over
// 6 KiB measured by the storage-size formula, depth): commits over REST and gRPC, and the GET.
// Bind must refuse each with production's text, and pass the names production accepted.
func TestBindNamesF0(t *testing.T) {
	var corpus struct{ Cases []f0Case }
	readCorpus(t, "f0-errors.json", &corpus)
	auth := Database{corpusProject, ""}
	seen := 0
	for _, c := range corpus.Cases {
		if !strings.Contains(c.ID, "name") && !strings.Contains(c.ID, "depth") && !strings.Contains(c.ID, "id_15") {
			continue
		}
		want := ""
		if c.Error != nil && nameMessages[c.Error.Message] {
			want = c.Error.Message
		}
		var err error
		switch {
		case c.Request.Method == "GET":
			path := strings.ReplaceAll(expand(t, c.Request.Path), "{project}", corpusProject)
			_, err = Bind(&firestorepb.GetDocumentRequest{Name: path}, auth)
		case c.Transport == "rest":
			body := toJSON(t, expandAll(t, c.Request.Body))
			var req firestorepb.CommitRequest
			if err := UnmarshalREST(body, &req); err != nil {
				t.Fatalf("%s: %v", c.ID, err)
			}
			_, err = Bind(&req, auth)
		default:
			var x any
			if err := json.Unmarshal(c.Request.Message, &x); err != nil {
				t.Fatal(err)
			}
			var req firestorepb.CommitRequest
			if err := protojson.Unmarshal(toJSON(t, expandAll(t, x)), &req); err != nil {
				t.Fatalf("%s: %v", c.ID, err)
			}
			_, err = Bind(&req, auth)
		}
		if got := errText(err); got != want {
			t.Errorf("%s: Bind = %q, want %q", c.ID, got, want)
		}
		seen++
	}
	if seen < 15 {
		t.Errorf("only %d name cases replayed", seen)
	}
}

func TestBindIsolation(t *testing.T) {
	const p = "proj-a"
	db := Database{p, DefaultDatabase}
	other := Database{"proj-b", DefaultDatabase}
	otherDB := Database{p, "second"}
	doc := func(d Database, rel string) string { return d.DocumentsName() + "/" + rel }
	ref := func(d Database) *firestorepb.Value {
		return &firestorepb.Value{ValueType: &firestorepb.Value_ReferenceValue{ReferenceValue: doc(d, "c/x")}}
	}
	// Each request is built for a database d; it must bind to db and be refused for the others.
	requests := map[string]func(d Database) proto.Message{
		"GetDocument": func(d Database) proto.Message { return &firestorepb.GetDocumentRequest{Name: doc(d, "c/a")} },
		"ListDocuments": func(d Database) proto.Message {
			return &firestorepb.ListDocumentsRequest{Parent: d.DocumentsName(), CollectionId: "c"}
		},
		"CreateDocument": func(d Database) proto.Message {
			return &firestorepb.CreateDocumentRequest{Parent: doc(d, "c/a"), CollectionId: "s"}
		},
		"UpdateDocument": func(d Database) proto.Message {
			return &firestorepb.UpdateDocumentRequest{Document: &firestorepb.Document{Name: doc(d, "c/a")}}
		},
		"DeleteDocument": func(d Database) proto.Message { return &firestorepb.DeleteDocumentRequest{Name: doc(d, "c/a")} },
		"BatchGet.database": func(d Database) proto.Message {
			return &firestorepb.BatchGetDocumentsRequest{Database: d.Name(), Documents: []string{doc(d, "c/a")}}
		},
		"BatchGet.documents": func(d Database) proto.Message {
			return &firestorepb.BatchGetDocumentsRequest{Database: db.Name(), Documents: []string{doc(db, "c/a"), doc(d, "c/b")}}
		},
		"BeginTransaction": func(d Database) proto.Message { return &firestorepb.BeginTransactionRequest{Database: d.Name()} },
		"Rollback":         func(d Database) proto.Message { return &firestorepb.RollbackRequest{Database: d.Name()} },
		"RunQuery":         func(d Database) proto.Message { return &firestorepb.RunQueryRequest{Parent: d.DocumentsName()} },
		"RunAggregationQuery": func(d Database) proto.Message {
			return &firestorepb.RunAggregationQueryRequest{Parent: doc(d, "c/a")}
		},
		"PartitionQuery":    func(d Database) proto.Message { return &firestorepb.PartitionQueryRequest{Parent: d.DocumentsName()} },
		"ListCollectionIds": func(d Database) proto.Message { return &firestorepb.ListCollectionIdsRequest{Parent: doc(d, "c/a")} },
		"ExecutePipeline":   func(d Database) proto.Message { return &firestorepb.ExecutePipelineRequest{Database: d.Name()} },
		"Commit.database":   func(d Database) proto.Message { return &firestorepb.CommitRequest{Database: d.Name()} },
		"Commit.update": func(d Database) proto.Message {
			return &firestorepb.CommitRequest{Database: db.Name(), Writes: []*firestorepb.Write{
				{Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{Name: doc(db, "c/a")}}},
				{Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{Name: doc(d, "c/b")}}},
			}}
		},
		"Commit.delete": func(d Database) proto.Message {
			return &firestorepb.CommitRequest{Database: db.Name(), Writes: []*firestorepb.Write{
				{Operation: &firestorepb.Write_Delete{Delete: doc(d, "c/b")}}}}
		},
		"Commit.verify": func(d Database) proto.Message {
			return &firestorepb.CommitRequest{Database: db.Name(), Writes: []*firestorepb.Write{
				{Operation: &firestorepb.Write_Verify{Verify: doc(d, "c/b")}}}}
		},
		"Commit.transform": func(d Database) proto.Message {
			return &firestorepb.CommitRequest{Database: db.Name(), Writes: []*firestorepb.Write{
				{Operation: &firestorepb.Write_Transform{Transform: &firestorepb.DocumentTransform{Document: doc(d, "c/b")}}}}}
		},
		"BatchWrite": func(d Database) proto.Message {
			return &firestorepb.BatchWriteRequest{Database: db.Name(), Writes: []*firestorepb.Write{
				{Operation: &firestorepb.Write_Delete{Delete: doc(d, "c/b")}}}}
		},
		"Write": func(d Database) proto.Message {
			return &firestorepb.WriteRequest{Database: d.Name()}
		},
		"Listen.database":   func(d Database) proto.Message { return &firestorepb.ListenRequest{Database: d.Name()} },
		"Admin.GetDatabase": func(d Database) proto.Message { return &adminpb.GetDatabaseRequest{Name: d.Name()} },
		"Admin.ListIndexes": func(d Database) proto.Message {
			return &adminpb.ListIndexesRequest{Parent: d.Name() + "/collectionGroups/-"}
		},
		"Admin.UpdateField": func(d Database) proto.Message {
			return &adminpb.UpdateFieldRequest{Field: &adminpb.Field{Name: d.Name() + "/collectionGroups/c/fields/f"}}
		},
		"Operations.Get": func(d Database) proto.Message {
			return &longrunningpb.GetOperationRequest{Name: d.Name() + "/operations/x"}
		},
	}
	// The requests whose varying name is not the request's own database but another name in it.
	secondary := map[string]bool{"BatchGet.documents": true, "Commit.update": true, "Commit.delete": true,
		"Commit.verify": true, "Commit.transform": true, "BatchWrite": true}
	for name, build := range requests {
		// primary: the name that varies is the request's own database.
		primary := !secondary[name]
		if got, err := Bind(build(db), db); err != nil || got != db {
			t.Errorf("%s: own database: %v, %v", name, got, err)
		}
		if got, err := Bind(build(db), Database{p, ""}); err != nil || got != db {
			t.Errorf("%s: project authority: %v, %v", name, got, err)
		}
		for _, d := range []Database{other, otherDB} {
			// Under project authority.
			got, err := Bind(build(d), Database{p, ""})
			switch {
			case primary && d.Project == p:
				if err != nil || got != d {
					t.Errorf("%s for %v: another database of the project: %v, %v", name, d, got, err)
				}
			case primary:
				if From(err).Code != CodePermissionDenied {
					t.Errorf("%s for %v: Bind = %v, want PERMISSION_DENIED", name, d, err)
				}
			default:
				if errText(err) != mismatch(db, d) {
					t.Errorf("%s for %v: Bind = %v, want %q", name, d, err, mismatch(db, d))
				}
			}
			// With the request's database known.
			if _, err := Bind(build(d), db); errText(err) != mismatch(db, d) {
				t.Errorf("%s for %v, database known: Bind = %v", name, d, err)
			}
		}
	}

	// Reference values are data: a commit storing references to other projects binds.
	commit := &firestorepb.CommitRequest{Database: db.Name(), Writes: []*firestorepb.Write{{
		Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{Name: doc(db, "c/a"),
			Fields: map[string]*firestorepb.Value{"r": ref(other), "s": ref(otherDB)}}},
		UpdateTransforms: []*firestorepb.DocumentTransform_FieldTransform{{FieldPath: "u",
			TransformType: &firestorepb.DocumentTransform_FieldTransform_AppendMissingElements{
				AppendMissingElements: &firestorepb.ArrayValue{Values: []*firestorepb.Value{ref(other)}}}}},
	}}}
	if _, err := Bind(commit, db); err != nil {
		t.Errorf("references to other projects: %v", err)
	}

	// With project authority, the first database fixes the pair and the others must agree.
	mixed := &firestorepb.BatchGetDocumentsRequest{Documents: []string{doc(db, "c/a"), doc(otherDB, "c/a")}}
	if _, err := Bind(mixed, Database{p, ""}); errText(err) != mismatch(db, otherDB) {
		t.Errorf("two databases under project authority: %v", err)
	}

	// A Write stream: the first message fixes the database, a later one may not change it.
	first, err := Bind(&firestorepb.WriteRequest{Database: db.Name()}, Database{p, ""})
	if err != nil || first != db {
		t.Fatalf("first Write message: %v %v", first, err)
	}
	if _, err := Bind(&firestorepb.WriteRequest{StreamToken: []byte{1}}, first); err != nil {
		t.Errorf("later Write message without database: %v", err)
	}
	if _, err := Bind(&firestorepb.WriteRequest{Database: otherDB.Name()}, first); errText(err) != mismatch(db, otherDB) {
		t.Errorf("later Write message on another database: %v", err)
	}

	// Listen: a target naming another project is the target's error, not the stream's.
	listen := &firestorepb.ListenRequest{Database: db.Name(), TargetChange: &firestorepb.ListenRequest_AddTarget{
		AddTarget: &firestorepb.Target{TargetId: 7, TargetType: &firestorepb.Target_Documents{
			Documents: &firestorepb.Target_DocumentsTarget{Documents: []string{doc(db, "c/a"), doc(other, "c/a")}}}}}}
	_, err = Bind(listen, db)
	te, ok := errors.AsType[*TargetError](err)
	if !ok || te.TargetID != 7 || te.Err.Message != mismatch(db, other) {
		t.Errorf("Listen target of another project: %v", err)
	}
	listen.TargetChange = &firestorepb.ListenRequest_AddTarget{AddTarget: &firestorepb.Target{TargetId: 8,
		TargetType: &firestorepb.Target_Query{Query: &firestorepb.Target_QueryTarget{Parent: otherDB.DocumentsName()}}}}
	if _, err := Bind(listen, db); !errors.As(err, &te) || te.TargetID != 8 {
		t.Errorf("Listen query target of another database: %v", err)
	}

	// A malformed name is production's INVALID_ARGUMENT, before any binding.
	if _, err := Bind(&firestorepb.GetDocumentRequest{Name: "c/a"}, db); errText(err) != `Document name "c/a" lacks "projects" at index 0.` {
		t.Errorf("malformed name: %v", err)
	}
	// A request type Bind does not know is refused.
	if _, err := Bind(&firestorepb.Document{}, db); From(err).Code != CodeInternal {
		t.Errorf("unknown request type: %v", err)
	}
	// No project authority binds nothing.
	if _, err := Bind(&firestorepb.RollbackRequest{Database: db.Name()}, Database{}); From(err).Code != CodePermissionDenied {
		t.Errorf("empty authority: %v", err)
	}
}

func mismatch(req, d Database) string {
	return "The request was for database '" + req.Name() + "' but was attempting to access database '" + d.Name() + "'"
}

// TestBindResourceNamesF1 replays f1-resource-names.json: a commit or batchGet on this
// database naming a document of another project or database is INVALID_ARGUMENT with
// production's text, over REST (the path's database first) and gRPC; a
// google-cloud-resource-prefix of another project is ignored.
func TestBindResourceNamesF1(t *testing.T) {
	var corpus struct{ Cases []f0Case }
	readCorpus(t, "f1-resource-names.json", &corpus)
	auth := Database{corpusProject, ""}
	seen := 0
	for _, c := range corpus.Cases {
		var req proto.Message
		switch {
		case c.Transport == "rest" && c.Request.Method == "POST":
			path := strings.ReplaceAll(c.Request.Path, "{project}", corpusProject)
			dbName, method, _ := strings.Cut(path, "/documents:")
			body := []byte(strings.ReplaceAll(string(toJSON(t, c.Request.Body)), "{project}", corpusProject))
			switch method {
			case "commit":
				req = &firestorepb.CommitRequest{}
			case "batchGet":
				req = &firestorepb.BatchGetDocumentsRequest{}
			default:
				t.Fatalf("%s: %s", c.ID, method)
			}
			if err := UnmarshalREST(body, req, "database"); err != nil {
				t.Fatalf("%s: %v", c.ID, err)
			}
			req.ProtoReflect().Set(req.ProtoReflect().Descriptor().Fields().ByName("database"), protoreflectString(dbName))
		case c.Transport == "grpc":
			switch c.Request.RPC {
			case "google.firestore.v1.Firestore/Commit":
				req = &firestorepb.CommitRequest{}
			case "google.firestore.v1.Firestore/GetDocument":
				req = &firestorepb.GetDocumentRequest{}
			default:
				t.Fatalf("%s: %s", c.ID, c.Request.RPC)
			}
			if err := protojson.Unmarshal(c.Request.Message, req); err != nil {
				t.Fatal(err)
			}
		default:
			continue // the API key cases are not names
		}
		seen++
		_, err := Bind(req, auth)
		want := ""
		if c.Error != nil {
			want = c.Error.Message
		}
		if got := errText(err); got != want {
			t.Errorf("%s: Bind = %q, production %q", c.ID, got, want)
		}
		if err != nil && c.Response.HTTP != 0 {
			e := From(err)
			body := e.RESTBody()
			if c.Response.BodyIsArray {
				body = e.RESTStreamBody()
			}
			if e.HTTPStatus() != c.Response.HTTP || !sameJSON(t, body, toJSON(t, c.Response.Body)) {
				t.Errorf("%s: %d %s", c.ID, e.HTTPStatus(), body)
			}
		}
	}
	if seen != 8 {
		t.Errorf("%d cases replayed, want 8", seen)
	}
}

func TestBindAdminProjectNames(t *testing.T) {
	auth := Database{"proj-a", ""}
	for _, c := range []struct {
		req  proto.Message
		want Code
	}{
		{&adminpb.ListDatabasesRequest{Parent: "projects/proj-a"}, CodeOK},
		{&adminpb.ListDatabasesRequest{Parent: "projects/proj-b"}, CodePermissionDenied},
		{&adminpb.CreateDatabaseRequest{Parent: "projects/proj-a", DatabaseId: "second"}, CodeOK},
		{&adminpb.RestoreDatabaseRequest{Parent: "projects/proj-a", Backup: "projects/proj-b/locations/l/backups/b"}, CodePermissionDenied},
		{&adminpb.ListDatabasesRequest{Parent: "proj-a"}, CodeInvalidArgument},
		{&adminpb.GetDatabaseRequest{Name: "projects/proj-a/databases/Bad DB"}, CodeInvalidArgument},
	} {
		_, err := Bind(c.req, auth)
		got := CodeOK
		if err != nil {
			got = From(err).Code
		}
		if got != c.want {
			t.Errorf("%T %v: %v, want %v", c.req, c.req, err, c.want)
		}
	}
}

func TestBindName(t *testing.T) {
	for _, c := range []struct {
		name string
		want Code
	}{
		{"projects/proj-a/databases/(default)", CodeOK},
		{"projects/proj-a/databases/(default)/documents/c/a", CodeOK},
		{"projects/proj-b/databases/(default)", CodePermissionDenied},
		{"projects/proj-a/databases/second", CodeOK},
		{"databases/(default)", CodeInvalidArgument},
	} {
		_, err := BindName(c.name, Database{"proj-a", ""})
		got := CodeOK
		if err != nil {
			got = From(err).Code
		}
		if got != c.want {
			t.Errorf("BindName(%q) = %v, want %v", c.name, err, c.want)
		}
	}
	if got, err := BindName("projects/proj-a/databases/second", Database{"proj-a", ""}); err != nil || got.ID != "second" {
		t.Errorf("project authority: %v %v", got, err)
	}
}

func protoreflectString(s string) protoreflect.Value { return protoreflect.ValueOfString(s) }

// TestNamesF1 replays the f1 names production refused: a relative or collection name in
// batchGet, a collection path in DELETE, a collection as listCollectionIds' parent, and the
// document ids CreateDocument refused.
func TestNamesF1(t *testing.T) {
	cases := map[string]func(c f0Case) error{
		"f1-batch-get.json/rest.relative_name": func(c f0Case) error {
			return bindREST(t, c, &firestorepb.BatchGetDocumentsRequest{})
		},
		"f1-batch-get.json/rest.collection_name": func(c f0Case) error {
			return bindREST(t, c, &firestorepb.BatchGetDocumentsRequest{})
		},
		"f1-delete.json/rest.collection_path": func(c f0Case) error {
			_, err := Bind(&firestorepb.DeleteDocumentRequest{Name: strings.ReplaceAll(c.Request.Path, "{project}", corpusProject)},
				Database{corpusProject, ""})
			return err
		},
		"f1-list-collection-ids.json/rest.collection_parent": func(c f0Case) error {
			parent, _, _ := strings.Cut(strings.ReplaceAll(c.Request.Path, "{project}", corpusProject), ":")
			_, err := Bind(&firestorepb.ListCollectionIdsRequest{Parent: parent}, Database{corpusProject, ""})
			return err
		},
		"f1-create.json/rest.dot_id":      createID,
		"f1-create.json/rest.slash_id":    createID,
		"f1-create.json/rest.reserved_id": createID,
	}
	seen := 0
	for _, file := range []string{"f1-batch-get.json", "f1-delete.json", "f1-list-collection-ids.json", "f1-create.json"} {
		var corpus struct{ Cases []f0Case }
		readCorpus(t, file, &corpus)
		for _, c := range corpus.Cases {
			run, ok := cases[file+"/"+c.ID]
			if !ok {
				continue
			}
			seen++
			if got := errText(run(c)); got != c.Error.Message {
				t.Errorf("%s %s:\n got  %q\n want %q", file, c.ID, got, c.Error.Message)
			}
		}
	}
	if seen != len(cases) {
		t.Errorf("%d of %d cases recorded", seen, len(cases))
	}
}

func bindREST(t *testing.T, c f0Case, req proto.Message) error {
	t.Helper()
	body := []byte(strings.ReplaceAll(string(toJSON(t, c.Request.Body)), "{project}", corpusProject))
	if err := UnmarshalREST(body, req, "database"); err != nil {
		return err
	}
	_, err := Bind(req, Database{corpusProject, ""})
	return err
}

func createID(c f0Case) error {
	_, query, _ := strings.Cut(c.Request.Path, "?")
	q, _ := url.ParseQuery(query)
	return CheckID(q.Get("documentId"), true)
}
