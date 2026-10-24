package core

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// The binding against what deployed buckets and the oracles observed (ADR-022 §8): Pyric's
// probes of a real bucket, request by request as Pyric made them, and the emulator's list.

const oracleDir = "../../../../test/oracle/rules"

func readOracle(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(oracleDir, path))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// pyricRR is the part of PY-RR the binding is checked against.
type pyricRR struct {
	ObservedAt           string                       `json:"observedAt"`
	FbSdkVersion         string                       `json:"fbSdkVersion"`
	Bucket               string                       `json:"bucket"`
	ProbeRules           string                       `json:"probeRules"`
	Behavior             map[string]map[string]string `json:"behavior"`
	StoredUpdateBehavior map[string]map[string]string `json:"storedUpdateBehavior"`
}

// Pyric's payloads (storage-request-resource-probes.ts): every seed and create, and every upload
// over a seed.
var (
	pyricPayload     = []byte{0x70, 0x79, 0x72, 0x69, 0x63}
	pyricReplacement = []byte{0x72, 0x75, 0x6c, 0x65, 0x73, 0x21}
)

func checksummed(o Object, body []byte) Object {
	o.Size, o.MD5 = int64(len(body)), md5.Sum(body)
	o.CRC32C = crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli))
	return o
}

// PY-RR, every probe through the binding: the fifteen keys of request.resource on a create, an
// update and an upload over a stored object (verb-create, verb-update, resource-existing,
// keys-size-15 and their siblings), and the stored-update probes over objects a client and
// GCS wrote (ADR-022 §8). The writes are Pyric's (run-storage-request-resource-fields.ts): a
// create sends contentType text/plain, and on the probes not named unset-* the settable fields
// and metadata {probe: value}; an update and an overwrite go to an object seeded through GCS
// (application/octet-stream, no disposition), the update with the settable fields and metadata,
// or the metadata alone on partial-*; the overwrite with contentType text/plain.
func TestPyricRequestResourceFields(t *testing.T) {
	var rr pyricRR
	readOracle(t, "imported/pyric/original/observations/storage-rules/stdlib-realstorage-request-resource-fields.json", &rr)
	if rr.ObservedAt != "2026-09-27T16:19:58.707Z" || rr.FbSdkVersion != "12.13.0" || rr.Bucket != "digame-mas.appspot.com" {
		t.Fatalf("not the observation ADR-022 cites: %s %s %s", rr.ObservedAt, rr.FbSdkVersion, rr.Bucket)
	}
	const prefix = "__pyric_storage_rr/run"
	s := rulesService(t, storageRules(strings.ReplaceAll(rr.ProbeRules, "<prefix>", prefix)))
	b := Bucket{Project: testProject, Name: rr.Bucket}
	ctx := context.Background()

	settable := map[string]*string{FieldContentType: str("text/plain"), FieldContentDisposition: str("inline"),
		FieldContentEncoding: str("identity"), FieldContentLanguage: str("en"), FieldCacheControl: str("no-cache")}
	custom := map[string]*string{"probe": str("value")}
	created := time.Date(2026, 9, 27, 16, 19, 0, 0, time.UTC)
	// seed is an object as Pyric's seeds were stored: through GCS, or a client upload of
	// text/plain alone.
	seed := func(name string, sf Surface) *Object {
		o := checksummed(Object{Bucket: b.Name, Name: name, Generation: 1790000000000000, Metageneration: 1,
			Created: created, Updated: created}, pyricPayload)
		if sf == SurfaceFirebase {
			o.ContentType = "text/plain"
		}
		applyDefaults(&o, sf)
		return &o
	}
	upload := func(name string, fields map[string]*string, md map[string]*string, body []byte, over *Object) ruleOp {
		o := checksummed(Object{Bucket: b.Name, Name: name}, body)
		o = patched(o, &Patch{Fields: fields, Metadata: md})
		return ruleOp{Method: rules.MethodCreate, Name: name, Resource: over, Incoming: &o, Surface: SurfaceFirebase}
	}

	var n, failed int
	check := func(id, want string, op ruleOp) {
		t.Helper()
		n++
		err := authz(s, ctx, b, ruleAlice, op)
		got := "ALLOW"
		if err == ErrPermissionDenied {
			got = "DENY"
		} else if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got != want {
			failed++
			t.Errorf("%s: %s, production %s", id, got, want)
		}
	}
	for _, group := range []string{"create", "update", "overwrite"} {
		probes := rr.Behavior[group]
		if len(probes) == 0 {
			t.Fatalf("no %s probes", group)
		}
		for _, id := range slices.Sorted(mapsKeys(probes)) {
			name := fmt.Sprintf("%s/%s/%s.bin", prefix, group, id)
			var op ruleOp
			switch group {
			case "create":
				if strings.HasPrefix(id, "unset-") {
					op = upload(name, map[string]*string{FieldContentType: str("text/plain")}, nil, pyricPayload, nil)
				} else {
					op = upload(name, settable, custom, pyricPayload, nil)
				}
			case "update":
				p := Patch{Fields: settable, Metadata: custom}
				if strings.HasPrefix(id, "partial-") {
					p = Patch{Metadata: custom}
				}
				op = ruleOp{Method: rules.MethodUpdate, Name: name, Resource: seed(name, SurfaceGCS), Patch: &p, Surface: SurfaceFirebase}
			case "overwrite":
				op = upload(name, map[string]*string{FieldContentType: str("text/plain")}, nil, pyricReplacement, seed(name, SurfaceGCS))
			}
			check(group+"/"+id, probes[id], op)
		}
	}
	// An update whose caller hands an Incoming with the versions it will commit (S2-C's Update
	// does) sees the stored versions all the same: every update probe again, the
	// metageneration-next DENY and metageneration-unchanged ALLOW among them.
	for id, want := range rr.Behavior["update"] {
		name := fmt.Sprintf("%s/update/%s.bin", prefix, id)
		p := Patch{Fields: settable, Metadata: custom}
		if strings.HasPrefix(id, "partial-") {
			p = Patch{Metadata: custom}
		}
		res := seed(name, SurfaceGCS)
		next := patched(*res, &p)
		next.Generation, next.Metageneration = res.Generation+1, res.Metageneration+1
		check("update with a bumped Incoming/"+id, want, ruleOp{Method: rules.MethodUpdate, Name: name, Resource: res,
			Incoming: &next, Patch: &p, Surface: SurfaceFirebase})
	}
	if rr.Behavior["update"]["metageneration-next"] != "DENY" || rr.Behavior["update"]["metageneration-unchanged"] != "ALLOW" {
		t.Error("PY-RR's metageneration probes are not the ones pinned")
	}
	for seedBy, probes := range rr.StoredUpdateBehavior {
		sf := map[string]Surface{"client": SurfaceFirebase, "gcs": SurfaceGCS}[seedBy]
		for id, want := range probes {
			name := fmt.Sprintf("%s/stored-update/%s/%s.txt", prefix, seedBy, id)
			op := ruleOp{Method: rules.MethodUpdate, Name: name, Resource: seed(name, sf), Patch: &Patch{Metadata: custom}, Surface: SurfaceFirebase}
			check("stored-update/"+seedBy+"/"+id, want, op)
		}
	}
	if n < 170 {
		t.Errorf("%d probes checked", n)
	}
	t.Logf("PY-RR: %d of %d probes agree", n-failed, n)
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Pyric's native-object-fields probe (stdlib-realstorage-p2-native-object-fields.json,
// 2026-07-21; its rules from run-storage-stdlib-native-fields.ts, built from the stored
// object's own fields): resource's generation and metageneration are ints, its md5Hash, crc32c
// and etag strings as GCS shows them, its times timestamps; request.resource on a create has no
// version, etag or time; an update and an upload over an object compare hashes with resource.
func TestPyricNativeObjectFields(t *testing.T) {
	var obs struct {
		ObservedAt string         `json:"observedAt"`
		Behavior   map[string]any `json:"behavior"`
		Server     map[string]any `json:"serverFields"`
	}
	readOracle(t, "imported/pyric/original/observations/storage-rules/stdlib-realstorage-p2-native-object-fields.json", &obs)
	created, err := time.Parse(time.RFC3339, fmt.Sprint(obs.Server["timeCreated"]))
	if err != nil {
		t.Fatal(err)
	}
	stored := checksummed(Object{Bucket: "digame-mas.appspot.com", Generation: 1784647629568292, Metageneration: 1,
		ContentType: defaultContentType, Created: created, Updated: created}, pyricPayload)
	if obs.Server["generation"] != "1784647629568292" || obs.Server["metageneration"] != "1" || obs.ObservedAt != "2026-07-21T15:27:18.766Z" {
		t.Fatalf("serverFields %v", obs.Server)
	}
	lit := func(s string) string { return "'" + s + "'" }
	md5b64 := base64.StdEncoding.EncodeToString(stored.MD5[:])
	crc := crc32cBase64(stored.CRC32C)
	exact := strings.Join([]string{
		"resource.generation == 1784647629568292", "resource.metageneration == 1",
		"resource.md5Hash == " + lit(md5b64), "resource.crc32c == " + lit(crc),
		"resource.etag == " + lit(objectETag(stored.Generation, stored.Metageneration)),
		fmt.Sprintf("resource.timeCreated.toMillis() == %d", created.UnixMilli()),
		fmt.Sprintf("resource.updated.toMillis() == %d", created.UnixMilli()),
	}, " && ")
	s := rulesService(t, storageRules(`
    match /native/stored-exact.bin { allow read: if `+exact+`; }
    match /native/identity-mismatch.bin { allow read: if resource.generation == 1784647629568293; }
    match /native/hash-mismatch.bin { allow read: if resource.md5Hash == `+lit(md5b64+"-mismatch")+`; }
    match /native/time-mismatch.bin { allow read: if resource.updated.toMillis() == `+fmt.Sprint(created.UnixMilli()+1)+`; }
    match /native/incoming-exact.bin { allow create: if request.resource.md5Hash == `+lit(md5b64)+` && request.resource.crc32c == `+lit(crc)+`; }
    match /native/incoming-md5-mismatch.bin { allow create: if request.resource.md5Hash == 'mismatch'; }
    match /native/incoming-crc-mismatch.bin { allow create: if request.resource.crc32c == 'mismatch'; }
    match /native/incoming-excluded-version.bin { allow create: if request.resource.generation == 0; }
    match /native/incoming-excluded-etag.bin { allow create: if request.resource.etag == ''; }
    match /native/incoming-excluded-time.bin { allow create: if request.resource.timeCreated.toMillis() > 0; }
    match /native/metadata-update.bin { allow update: if request.resource.md5Hash == resource.md5Hash && request.resource.crc32c == resource.crc32c; }
    match /native/byte-overwrite-control.bin { allow write: if true; }
    match /native/byte-overwrite-md5.bin { allow write: if request.resource.md5Hash != resource.md5Hash; }
    match /native/byte-overwrite-crc.bin { allow write: if request.resource.crc32c != resource.crc32c; }`))
	ctx := context.Background()
	at := func(family string) *Object {
		o := stored
		o.Name = "native/" + family + ".bin"
		return &o
	}
	for family, want := range obs.Behavior {
		w, ok := want.(string)
		if !ok {
			continue // the relations: facts about the stored generations, not decisions
		}
		var op ruleOp
		switch {
		case strings.HasPrefix(family, "incoming-"):
			in := checksummed(Object{Bucket: stored.Bucket, Name: at(family).Name}, pyricPayload)
			op = ruleOp{Method: rules.MethodCreate, Name: in.Name, Incoming: &in, Surface: SurfaceFirebase}
		case family == "metadata-update":
			op = ruleOp{Method: rules.MethodUpdate, Name: at(family).Name, Resource: at(family),
				Patch: &Patch{Metadata: map[string]*string{"probe": str("run")}}, Surface: SurfaceFirebase}
		case strings.HasPrefix(family, "byte-overwrite-"):
			in := checksummed(Object{Bucket: stored.Bucket, Name: at(family).Name}, pyricReplacement)
			op = ruleOp{Method: rules.MethodCreate, Name: in.Name, Resource: at(family), Incoming: &in, Surface: SurfaceFirebase}
		default:
			op = ruleOp{Method: rules.MethodGet, Name: at(family).Name, Resource: at(family), Surface: SurfaceFirebase}
		}
		err := authz(s, ctx, Bucket{Project: testProject, Name: stored.Bucket}, ruleAlice, op)
		if got := map[bool]string{true: "ALLOW", false: "DENY"}[err == nil]; got != w || (err != nil && err != ErrPermissionDenied) {
			t.Errorf("%s: %v, production %s", family, err, w)
		}
	}
}

// GCS's etag of a generation and a metageneration: the form of the GCS etag the oracle's
// resource carries (production/storage.json "CJqUmJ6Ry4ADEAE=", of generation
// 1691431467354650 and metageneration 1), changing with the metageneration.
func TestObjectETag(t *testing.T) {
	if got := objectETag(1691431467354650, 1); got != "CJqUmJ6Ry4ADEAE=" {
		t.Errorf("etag %s", got)
	}
	if objectETag(1700000000000000, 2) == objectETag(1700000000000000, 1) {
		t.Error("the etag does not follow the metageneration")
	}
	if got := crc32cBase64(0xc994_65aa); got != "yZRlqg==" {
		t.Errorf("crc32c %s", got)
	}
}

// oracleFile is a recorded set of Rules Test API cases.
type oracleFile struct {
	Rulesets  map[string]string `json:"rulesets"`
	Resources map[string]struct {
		Name, Bucket, ContentType, MD5Hash, CRC32C, ETag, ContentDisposition, ContentEncoding string
		Size, Generation, Metageneration                                                      int64
		Metadata                                                                              map[string]string
		TimeCreated, Updated                                                                  time.Time
	} `json:"resources"`
	Cases []struct {
		ID          string          `json:"id"`
		Ruleset     string          `json:"ruleset"`
		RulesetRef  string          `json:"rulesetRef"`
		ResourceRef string          `json:"resourceRef"`
		Request     json.RawMessage `json:"request"`
		Expected    struct {
			Outcome string `json:"outcome"`
		} `json:"expected"`
	} `json:"cases"`
}

func (f *oracleFile) ruleset(t *testing.T, id string) string {
	t.Helper()
	for _, c := range f.Cases {
		if c.ID == id {
			if c.Ruleset != "" {
				return c.Ruleset
			}
			return f.Rulesets[c.RulesetRef]
		}
	}
	t.Fatalf("no case %s", id)
	return ""
}

// list is decided on its folder's children, as the emulator's engine decides it
// (aruma/storage/read_list and aruma/storage-paths/list_on_folder: the emulator denies, as here;
// the Test API, which matches the path itself, allows). request.method and request.path follow
// ADR-022 §8: aruma/storage/method_list and request_path, as the Test API answers them.
func TestOracleListAndPath(t *testing.T) {
	var pack, paths oracleFile
	readOracle(t, "production/storage.json", &pack)
	readOracle(t, "production/storage-paths.json", &paths)
	ctx := context.Background()
	b := Bucket{Project: testProject, Name: "demo-oracle.appspot.com"}
	user := Principal{Kind: PrincipalUser, UID: "alice", Token: map[string]any{"sub": "alice"}}
	list := func(prefix string) ruleOp { return ruleOp{Method: rules.MethodList, List: true, Name: prefix} }
	for _, c := range []struct {
		src   string
		op    ruleOp
		allow bool
		why   string
	}{
		{pack.ruleset(t, "aruma/storage/read_list"), list("c_read_list/file.txt/"), false, "read_list: the emulator's answer"},
		{pack.ruleset(t, "aruma/storage/read_list"), list("c_read_list/"), true, "read_list on its folder"},
		{pack.ruleset(t, "aruma/storage/get_list"), list("c_get_list/"), false, "get does not cover list"},
		{pack.ruleset(t, "aruma/storage/method_list"), list("c_method_list/"), true, "request.method is 'list'"},
		{pack.ruleset(t, "aruma/storage/request_path"), ruleOp{Method: rules.MethodGet, Name: "c_request_path/file.txt"}, true, "request.path"},
		{pack.ruleset(t, "aruma/storage/request_path"), ruleOp{Method: rules.MethodGet, Name: "c_request_path/other.txt"}, false, "request.path of another"},
		{paths.ruleset(t, "aruma/storage-paths/list_on_folder"), list("p/a/"), false, "list_on_folder: the emulator's answer"},
		{paths.ruleset(t, "aruma/storage-paths/list_on_folder"), list("p/"), false, "the unknown child has no value"},
		{paths.ruleset(t, "aruma/storage-paths/list_on_prefix"), list("p/"), true, "{rest=**} takes the unknown child"},
		{paths.ruleset(t, "aruma/storage-paths/list_on_prefix"), list("p/a/b/"), true, "and a deeper one"},
		{paths.ruleset(t, "aruma/storage-paths/list_on_prefix"), list(""), false, "not the root"},
	} {
		s := rulesService(t, c.src)
		err := authz(s, ctx, b, user, c.op)
		if c.allow && err != nil || !c.allow && err != ErrPermissionDenied {
			t.Errorf("%s: %v, want allow %v", c.why, err, c.allow)
		}
	}
}

// The resource the binding builds, against the Test API's answers on the same object (pack-1's
// "default" resource): every get of production/storage.json that reads resource or request
// alone. The Test API takes resource as given, so the cases that count its keys, or that read
// keys the oracle's resource left out, measure the request rather than the service: they are
// listed, with the binding's answer.
func TestOracleResourceView(t *testing.T) {
	var pack oracleFile
	readOracle(t, "production/storage.json", &pack)
	r, ok := pack.Resources["default"]
	if !ok {
		t.Fatal("no default resource")
	}
	md5sum, err := base64.StdEncoding.DecodeString(r.MD5Hash)
	if err != nil || len(md5sum) != 16 {
		t.Fatalf("md5Hash %q", r.MD5Hash)
	}
	crcBytes, err := base64.StdEncoding.DecodeString(r.CRC32C)
	if err != nil || len(crcBytes) != 4 {
		t.Fatalf("crc32c %q", r.CRC32C)
	}
	o := Object{Bucket: r.Bucket, Name: r.Name, Size: r.Size, Generation: r.Generation, Metageneration: r.Metageneration,
		ContentType: r.ContentType, ContentDisposition: r.ContentDisposition, ContentEncoding: r.ContentEncoding,
		Metadata: r.Metadata, Created: r.TimeCreated, Updated: r.Updated,
		CRC32C: uint32(crcBytes[0])<<24 | uint32(crcBytes[1])<<16 | uint32(crcBytes[2])<<8 | uint32(crcBytes[3])}
	copy(o.MD5[:], md5sum)
	ctx := context.Background()
	b := Bucket{Project: testProject, Name: r.Bucket}
	type request struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Auth   *struct {
			UID   string         `json:"uid"`
			Token map[string]any `json:"token"`
		} `json:"auth"`
		Time time.Time `json:"time"`
	}
	compiled := map[string]*Service{}
	var agree, total int
	for _, c := range pack.Cases {
		var req request
		if err := json.Unmarshal(c.Request, &req); err != nil {
			t.Fatal(err)
		}
		if req.Method != "get" || c.RulesetRef == "" || c.ResourceRef != "default" || (c.Expected.Outcome != "allow" && c.Expected.Outcome != "deny") {
			continue
		}
		name, ok := strings.CutPrefix(req.Path, "/b/"+r.Bucket+"/o/")
		if !ok {
			continue
		}
		s := compiled[c.RulesetRef]
		if s == nil {
			s = rulesService(t, pack.Rulesets[c.RulesetRef])
			compiled[c.RulesetRef] = s
		}
		s.now = func() time.Time { return req.Time }
		p := anonymous
		if req.Auth != nil {
			p = Principal{Kind: PrincipalUser, UID: req.Auth.UID, Token: req.Auth.Token}
		}
		total++
		err := authz(s, ctx, b, p, ruleOp{Method: rules.MethodGet, Name: name, Resource: &o, Surface: SurfaceFirebase})
		got := map[bool]string{true: "allow", false: "deny"}[err == nil]
		if err != nil && err != ErrPermissionDenied {
			t.Fatalf("%s: %v", c.ID, err)
		}
		if got == c.Expected.Outcome {
			agree++
			continue
		}
		if why, ok := resourceViewDifferences[c.ID]; ok {
			t.Logf("%s: %s where the Test API says %s: %s", c.ID, got, c.Expected.Outcome, why)
			continue
		}
		t.Errorf("%s: %s, the Test API %s", c.ID, got, c.Expected.Outcome)
	}
	if total < 30 {
		t.Errorf("%d cases", total)
	}
	t.Logf("%d of %d gets agree", agree, total)
}

// resourceViewDifferences are the gets whose answer measures the Test API's literal resource,
// not a stored object: the binding gives the keys a deployed bucket has (PY-RR).
var resourceViewDifferences = map[string]string{
	"aruma/storage/fs_get":        "firestore.get() denies until v0.4 (rules.NoDocuments); the Test API mocks the document",
	"aruma/storage/fs_exists":     "firestore.exists() denies until v0.4",
	"aruma/storage/fs_lookups_2":  "firestore.exists() denies until v0.4",
	"aruma/storage/fs_lookups_3":  "past Storage's two lookups, which the Test API does not count",
	"aruma/storage/fs_lookups_11": "past Storage's two lookups, which the Test API does not count",
}
