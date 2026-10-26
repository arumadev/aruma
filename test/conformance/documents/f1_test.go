package documents

import (
	"testing"
	"time"
)

// The F1 cases: GetDocument, BatchGetDocuments, ListDocuments, ListCollectionIds, CreateDocument,
// UpdateDocument and DeleteDocument over REST, each step pinned to production's recorded answer
// (test/oracle/documents/corpus/f1-*.json, record-f1.mjs). Collection ids carry {run} so that
// runs against one Aruma server never see each other's documents; cases run in order.

const (
	colGet   = "aruma-ref-f1-get{run}"
	colList  = "aruma-ref-f1-list{run}"
	colLC    = "aruma-ref-f1-lcids{run}"
	colWrite = "aruma-ref-f1-write{run}"
	colAuto  = "aruma-ref-f1-auto{run}"
	colNever = "aruma-ref-f1-never{run}"

	// stale is the fixed precondition time of the recordings, 978307200000001 µs.
	stale = "2001-01-01T00%3A00%3A00.000001Z"
)

var g1 = map[string]any{"a": 1, "b": map[string]any{"c": 2, "d": "x"}, "s": "one"}

// getFixture is the setup of f1-get.json and f1-batch-get.json; gm is a missing document.
func getFixture() []Step {
	return []Step{
		Put(colGet+"/g1", g1),
		Put(colGet+"/g2", map[string]any{"a": 2}),
		Put(colGet+"/g3", map[string]any{"a": 3}),
		Put(colGet+"/gm/sub/k", map[string]any{"v": 1}),
	}
}

// listFixture is the setup of f1-list-documents.json; m is a missing document, d5 has no n.
func listFixture() []Step {
	return []Step{
		Put(colList+"/d1", map[string]any{"n": 3, "s": "c"}),
		Put(colList+"/d2", map[string]any{"n": 1, "s": "a"}),
		Put(colList+"/d3", map[string]any{"n": 2}),
		Put(colList+"/d4", map[string]any{"n": 1, "s": "b"}),
		Put(colList+"/d5", map[string]any{"s": "d"}),
		Put(colList+"/d1/s1/x", map[string]any{"k": 1}),
		Put(colList+"/d1/s2/y", map[string]any{"k": 2}),
		Put(colList+"/m/sub/z", map[string]any{"k": 3}),
	}
}

// lcFixture is the setup of f1-list-collection-ids.json; q is a missing document.
func lcFixture() []Step {
	steps := []Step{Put(colLC+"/p", map[string]any{"a": 1})}
	for _, c := range []string{"c1", "c2", "c3", "c4", "c5"} {
		steps = append(steps, Put(colLC+"/p/"+c+"/x", map[string]any{"a": 1}))
	}
	return append(steps, Put(colLC+"/q/k1/x", map[string]any{"a": 1}))
}

func with(fixture []Step, steps ...Step) []Step { return append(fixture, steps...) }

func get(rec, query string, expect ...Check) Step {
	path := "{root}/" + colGet + "/g1"
	if query != "" {
		path += "?" + query
	}
	return Step{Request: Request{Method: "GET", Path: path}, Recorded: "f1-get.json#" + rec, Expect: expect}
}

func batchGet(rec string, body map[string]any, expect ...Check) Step {
	return Step{Request: Request{Method: "POST", Path: "{root}:batchGet", Body: body}, Recorded: "f1-batch-get.json#" + rec, Expect: expect}
}

func list(rec, query string, expect ...Check) Step {
	path := "{root}/" + colList
	if query != "" {
		path += "?" + query
	}
	return Step{Request: Request{Method: "GET", Path: path}, Recorded: "f1-list-documents.json#" + rec, Expect: expect}
}

func listIDs(rec, parent string, body map[string]any, expect ...Check) Step {
	return Step{Request: Request{Method: "POST", Path: "{root}/" + parent + ":listCollectionIds", Body: body}, Recorded: "f1-list-collection-ids.json#" + rec, Expect: expect}
}

func names(col string, ids ...string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = col + "/" + id
	}
	return out
}

func readTimes() map[string]string {
	now := time.Now().UTC()
	return map[string]string{
		"twoHours": now.Add(-2 * time.Hour).Truncate(time.Minute).Format(time.RFC3339),
		"future":   now.Add(time.Hour).Format("2006-01-02T15:04:05.000000Z"),
		"nanos":    now.Add(-10*time.Second).Truncate(time.Second).Format("2006-01-02T15:04:05") + ".123456789Z",
		"recent":   now.Add(-5 * time.Second).Format("2006-01-02T15:04:05.000000Z"),
	}
}

var f1Cases = []Case{
	// ---- GetDocument ----
	{ID: "get/found", Phase: "F1", Steps: with(getFixture(),
		get("rest.found", "", HTTP(200), Equal("name", "{root}/"+colGet+"/g1"), Equal("fields", Fields(g1)), Timestamps("createTime", "updateTime"), Keys("", "name", "fields", "createTime", "updateTime")),
	)},
	{ID: "get/mask", Phase: "F1", Steps: with(getFixture(),
		get("rest.mask_one", "mask.fieldPaths=a", HTTP(200), Equal("fields", Fields(map[string]any{"a": 1}))),
		get("rest.mask_nested", "mask.fieldPaths=b.c", HTTP(200), Equal("fields", Fields(map[string]any{"b": map[string]any{"c": 2}}))),
		get("rest.mask_two", "mask.fieldPaths=a&mask.fieldPaths=s", HTTP(200), Equal("fields", Fields(map[string]any{"a": 1, "s": "one"}))),
		get("rest.mask_parent_and_child", "mask.fieldPaths=b&mask.fieldPaths=b.c", HTTP(200), Equal("fields", Fields(map[string]any{"b": map[string]any{"c": 2, "d": "x"}}))),
		get("rest.mask_absent_field", "mask.fieldPaths=zz", HTTP(200), Absent("fields"), Keys("", "name", "createTime", "updateTime")),
		get("rest.mask_empty_path", "mask.fieldPaths=", Error(400, "INVALID_ARGUMENT", "Invalid empty property path string.")),
	)},
	{ID: "get/missing", Phase: "F1", Steps: with(getFixture(),
		Step{Request: Request{Method: "GET", Path: "{root}/" + colGet + "/nope"}, Recorded: "f1-get.json#rest.missing", Expect: []Check{NotFoundDoc(colGet + "/nope")}},
		Step{Request: Request{Method: "GET", Path: "{root}/" + colGet + "/gm"}, Recorded: "f1-get.json#rest.missing_with_subcollection", Expect: []Check{NotFoundDoc(colGet + "/gm")}},
	)},
	{ID: "get/read-time-refused", Phase: "F1", Vars: readTimes, Steps: with(getFixture(),
		get("rest.read_time.2h_whole_minute", "readTime={$twoHours:url}", Error(400, "FAILED_PRECONDITION", "The requested 'read_time' is too old.")),
		get("rest.read_time.future", "readTime={$future:url}", Error(400, "INVALID_ARGUMENT", "The requested 'read_time' cannot be in the future.")),
		get("rest.read_time.nanoseconds", "readTime={$nanos:url}", Error(400, "INVALID_ARGUMENT", "timestamp cannot have more than microseconds precision")),
		get("rest.read_time.epoch", "readTime=1970-01-01T00%3A00%3A00Z", Error(400, "INVALID_ARGUMENT", "Only timestamps past epoch are supported.")),
	)},
	{ID: "get/transaction", Phase: "F1", Vars: readTimes, Steps: with(getFixture(),
		Step{Request: Request{Method: "POST", Path: "{root}:beginTransaction", Body: map[string]any{"options": map[string]any{"readOnly": map[string]any{}}}},
			Recorded: "f1-get.json#rest.txn.begin_read_only", Expect: []Check{HTTP(200), Keys("", "transaction"), Capture("txn", "transaction")}},
		get("rest.txn.read_only", "transaction={$txn:url}", HTTP(200), Equal("fields", Fields(g1))),
		get("rest.txn.and_read_time", "transaction={$txn:url}&readTime={$recent:url}",
			// Which of the two fields production names varies between runs (as for current_document).
			ErrorPrefix(400, "INVALID_ARGUMENT", "Invalid value (oneof), oneof field 'consistency_selector' is already set. Cannot set '")),
	)},

	// ---- BatchGetDocuments ----
	{ID: "batch-get/found-and-missing", Must: []int{16}, Phase: "F1", Steps: with(getFixture(),
		batchGet("rest.order_run1", map[string]any{"documents": []string{"{root}/" + colGet + "/g3", "{root}/" + colGet + "/nope1", "{root}/" + colGet + "/g1", "{root}/" + colGet + "/nope2", "{root}/" + colGet + "/g2"}},
			HTTP(200), BatchGetAnswers(names(colGet, "g1", "g2", "g3"), names(colGet, "nope1", "nope2")), Timestamps("0.readTime", "0.found.createTime", "0.found.updateTime")),
	)},
	{ID: "batch-get/duplicates-answered-once", Must: []int{16}, Phase: "F1", Steps: with(getFixture(),
		batchGet("rest.duplicates", map[string]any{"documents": []string{"{root}/" + colGet + "/g1", "{root}/" + colGet + "/g1", "{root}/" + colGet + "/nope", "{root}/" + colGet + "/nope"}},
			HTTP(200), BatchGetAnswers(names(colGet, "g1"), names(colGet, "nope"))),
	)},
	{ID: "batch-get/mask", Must: []int{16}, Phase: "F1", Steps: with(getFixture(),
		batchGet("rest.mask", map[string]any{"documents": []string{"{root}/" + colGet + "/g1", "{root}/" + colGet + "/g2", "{root}/" + colGet + "/nope"}, "mask": map[string]any{"fieldPaths": []string{"a"}}},
			HTTP(200), BatchGetAnswers(names(colGet, "g1", "g2"), names(colGet, "nope"))),
	)},
	{ID: "batch-get/new-transaction", Must: []int{16}, Phase: "F1", Steps: with(getFixture(),
		batchGet("rest.new_transaction.read_only", map[string]any{"documents": []string{"{root}/" + colGet + "/g1", "{root}/" + colGet + "/nope", "{root}/" + colGet + "/g2"}, "newTransaction": map[string]any{"readOnly": map[string]any{}}},
			HTTP(200), TransactionFirst(), BatchGetAnswers(names(colGet, "g1", "g2"), names(colGet, "nope"))),
	)},
	{ID: "batch-get/empty", Must: []int{16}, Phase: "F1", Steps: []Step{
		batchGet("rest.empty_list", map[string]any{"documents": []string{}}, HTTP(200), Equal("", []any{})),
	}},
	{ID: "batch-get/bad-names", Phase: "F1", Steps: []Step{
		batchGet("rest.collection_name", map[string]any{"documents": []string{"{root}/" + colGet}}, LacksSlash("Document name", "{root}/"+colGet)),
		batchGet("rest.relative_name", map[string]any{"documents": []string{colGet + "/g1"}}, Error(400, "INVALID_ARGUMENT", `Document name "`+colGet+`/g1" lacks "projects" at index 0.`)),
	}},
	{ID: "batch-get/name-of-another-project", Phase: "F1", Steps: with(getFixture(),
		Step{Request: Request{Method: "POST", Path: "{root}:batchGet", Body: map[string]any{"documents": []string{"{root}/" + colGet + "/g1", "projects/aruma-ref-no-such-project/databases/(default)/documents/" + colWrite + "/x"}}},
			Recorded: "f1-resource-names.json#rest.batch_get.other_project",
			Expect:   []Check{Error(400, "INVALID_ARGUMENT", "The request was for database '{db}' but was attempting to access database 'projects/aruma-ref-no-such-project/databases/(default)'")}},
	)},

	// ---- ListDocuments ----
	{ID: "list/default", Must: []int{18}, Phase: "F1", Steps: with(listFixture(),
		list("rest.default", "", HTTP(200), DocNames("documents", names(colList, "d1", "d2", "d3", "d4", "d5")...), Absent("nextPageToken")),
		list("rest.page_size_0", "pageSize=0", HTTP(200), DocNames("documents", names(colList, "d1", "d2", "d3", "d4", "d5")...)),
		list("rest.page_size_negative", "pageSize=-1", Error(400, "INVALID_ARGUMENT", "Page size must be nonnegative.")),
	)},
	{ID: "list/paging", Must: []int{18}, Phase: "F1", Steps: with(listFixture(),
		list("rest.paging.page1", "pageSize=2", HTTP(200), DocNames("documents", names(colList, "d1", "d2")...), Capture("t1", "nextPageToken")),
		list("rest.paging.page2", "pageSize=2&pageToken={$t1:url}", HTTP(200), DocNames("documents", names(colList, "d3", "d4")...), Capture("t2", "nextPageToken")),
		list("rest.paging.page3", "pageSize=2&pageToken={$t2:url}", HTTP(200), DocNames("documents", names(colList, "d5")...), Absent("nextPageToken")),
	)},
	{ID: "list/page-token-refused", Must: []int{18}, Phase: "F1", Steps: with(listFixture(),
		list("rest.paging.page1", "pageSize=2", HTTP(200), Capture("t1", "nextPageToken")),
		// Two texts: a token that does not parse, and one from a request with another orderBy.
		list("rest.token.garbage", "pageSize=2&pageToken=abc", Error(400, "INVALID_ARGUMENT", "invalid page token")),
		list("rest.token.other_order_by", "pageSize=2&orderBy=n&pageToken={$t1:url}", Error(400, "INVALID_ARGUMENT", "Invalid page token.")),
	)},
	{ID: "list/order-by", Must: []int{18}, Phase: "F1", Steps: with(listFixture(),
		// A field order leaves out documents without the field (d5), and breaks ties by name in
		// the direction of the last field.
		list("rest.order_by.06", "orderBy=n", HTTP(200), DocNames("documents", names(colList, "d2", "d4", "d3", "d1")...)),
		list("rest.order_by.08", "orderBy=n%20desc", HTTP(200), DocNames("documents", names(colList, "d1", "d3", "d4", "d2")...)),
		list("rest.order_by.09", "orderBy=n%20desc%2C%20__name__%20desc", HTTP(200), DocNames("documents", names(colList, "d1", "d3", "d4", "d2")...)),
		// __name__ descending, and a name order against the field's direction, need a composite index.
		list("rest.order_by.03", "orderBy=__name__%20desc", ErrorPrefix(400, "FAILED_PRECONDITION", "The query requires an index. You can create it here: ")),
		list("rest.order_by.11", "orderBy=n%20desc%2C__name__", ErrorPrefix(400, "FAILED_PRECONDITION", "The query requires an index. You can create it here: ")),
		list("rest.order_by.18", "orderBy=__name__%2Cn", Error(400, "INVALID_ARGUMENT", "order by clause cannot contain more fields after the key")),
		list("rest.order_by.30", "orderBy=n%2Cn", Error(400, "INVALID_ARGUMENT", "order by clause cannot contain duplicate fields n")),
		list("rest.order_by.26", "orderBy=foo%20bar", Error(400, "INVALID_ARGUMENT", `Invalid order by clause "foo bar".`)),
	)},
	{ID: "list/show-missing", Must: []int{18}, Phase: "F1", Steps: with(listFixture(),
		list("rest.show_missing", "showMissing=true", HTTP(200), DocNames("documents", names(colList, "d1", "d2", "d3", "d4", "d5", "m")...), Keys("documents.5", "name")),
		list("rest.show_missing_order_by_name_desc", "showMissing=true&orderBy=__name__%20desc", Error(400, "INVALID_ARGUMENT", "cannot specify an order when show_missing is true")),
	)},
	{ID: "list/mask-and-empty", Must: []int{18}, Phase: "F1", Steps: with(listFixture(),
		list("rest.mask", "mask.fieldPaths=n", HTTP(200), Equal("documents.0.fields", Fields(map[string]any{"n": 3})), Absent("documents.4.fields")),
		Step{Request: Request{Method: "GET", Path: "{root}/" + colNever}, Recorded: "f1-list-documents.json#rest.never_written", Expect: []Check{HTTP(200), Equal("", map[string]any{})}},
	)},

	// ---- ListCollectionIds ----
	{ID: "list-collection-ids/default", Must: []int{18}, Phase: "F1", Steps: with(lcFixture(),
		listIDs("rest.default", colLC+"/p", map[string]any{}, HTTP(200), Strings("collectionIds", "c1", "c2", "c3", "c4", "c5"), Absent("nextPageToken")),
		listIDs("rest.missing_parent", colLC+"/q", map[string]any{}, HTTP(200), Strings("collectionIds", "k1")),
		listIDs("rest.page_size_negative", colLC+"/p", map[string]any{"pageSize": -1}, Error(400, "INVALID_ARGUMENT", "page_size must be greater than or equal to zero.")),
		listIDs("rest.collection_parent", colLC, map[string]any{}, LacksSlash("Document parent name", "{root}/"+colLC)),
	)},
	{ID: "list-collection-ids/paging", Must: []int{18}, Phase: "F1", Steps: with(lcFixture(),
		listIDs("rest.paging.page1", colLC+"/p", map[string]any{"pageSize": 2}, HTTP(200), Strings("collectionIds", "c1", "c2"), Capture("t1", "nextPageToken")),
		listIDs("rest.paging.page2", colLC+"/p", map[string]any{"pageSize": 2, "pageToken": "{$t1}"}, HTTP(200), Strings("collectionIds", "c3", "c4"), Capture("t2", "nextPageToken")),
		listIDs("rest.paging.page3", colLC+"/p", map[string]any{"pageSize": 2, "pageToken": "{$t2}"}, HTTP(200), Strings("collectionIds", "c5"), Absent("nextPageToken")),
		listIDs("rest.token.garbage", colLC+"/p", map[string]any{"pageSize": 2, "pageToken": "abc"}, Error(400, "INVALID_ARGUMENT", "invalid page token")),
	)},

	// ---- CreateDocument ----
	{ID: "create/automatic-id", Phase: "F1", Steps: []Step{
		{Request: Request{Method: "POST", Path: "{root}/" + colAuto, Body: map[string]any{"fields": Fields(map[string]any{"i": 0})}}, Recorded: "f1-create.json#rest.auto_id",
			Expect: []Check{HTTP(200), AutoID("name", colAuto), CreateTimeIsUpdateTime(), Timestamps("createTime", "updateTime")}},
	}},
	{ID: "create/existing-id", Phase: "F1", Steps: []Step{
		Put(colWrite+"/c1", map[string]any{"a": 1}),
		{Request: Request{Method: "POST", Path: "{root}/" + colWrite + "?documentId=c1", Body: map[string]any{"fields": Fields(map[string]any{"a": 2})}}, Recorded: "f1-create.json#rest.existing_id",
			Expect: []Check{Error(409, "ALREADY_EXISTS", "Document already exists: {root}/"+colWrite+"/c1")}},
	}},
	{ID: "create/refused", Phase: "F1", Steps: []Step{
		{Request: Request{Method: "POST", Path: "{root}/" + colWrite + "?documentId=__x__", Body: map[string]any{"fields": Fields(map[string]any{"a": 1})}}, Recorded: "f1-create.json#rest.reserved_id",
			Expect: []Check{Error(400, "INVALID_ARGUMENT", `Resource id "__x__" is invalid because it is reserved.`)}},
		{Request: Request{Method: "POST", Path: "{root}/" + colWrite + "?documentId=c4", Body: map[string]any{"name": "{root}/" + colWrite + "/other", "fields": Fields(map[string]any{"a": 1})}}, Recorded: "f1-create.json#rest.name_in_body",
			Expect: []Check{Error(400, "INVALID_ARGUMENT", "document.name must not be set")}},
	}},

	// ---- UpdateDocument ----
	{ID: "update/upsert", Phase: "F1", Steps: []Step{
		{Request: Request{Method: "PATCH", Path: "{root}/" + colWrite + "/u1", Body: map[string]any{"fields": Fields(map[string]any{"a": 1, "b": 2})}}, Recorded: "f1-update.json#rest.upsert_create",
			Expect: []Check{HTTP(200), CreateTimeIsUpdateTime(), Timestamps("createTime", "updateTime"), Equal("fields", Fields(map[string]any{"a": 1, "b": 2}))}},
	}},
	{ID: "update/mask", Phase: "F1", Steps: []Step{
		Put(colWrite+"/u1", map[string]any{"c": 3}),
		{Request: Request{Method: "PATCH", Path: "{root}/" + colWrite + "/u1?updateMask.fieldPaths=a", Body: map[string]any{"fields": Fields(map[string]any{"a": 10, "z": 9})}}, Recorded: "f1-update.json#rest.mask_one",
			Expect: []Check{HTTP(200), Equal("fields", Fields(map[string]any{"a": 10, "c": 3}))}},
		{Request: Request{Method: "PATCH", Path: "{root}/" + colWrite + "/u1?updateMask.fieldPaths=c", Body: map[string]any{"fields": Fields(map[string]any{"a": 11})}}, Recorded: "f1-update.json#rest.mask_absent_in_body",
			Expect: []Check{HTTP(200), Equal("fields", Fields(map[string]any{"a": 10}))}},
	}},
	{ID: "update/preconditions", Must: []int{15}, Phase: "F1", Steps: []Step{
		Put(colWrite+"/u1", map[string]any{"a": 1}),
		{Request: Request{Method: "PATCH", Path: "{root}/" + colWrite + "/u1?currentDocument.exists=false", Body: map[string]any{"fields": Fields(map[string]any{"a": 14})}}, Recorded: "f1-update.json#rest.exists_false_on_existing",
			Expect: []Check{Error(409, "ALREADY_EXISTS", "Document already exists: {root}/"+colWrite+"/u1")}},
		{Request: Request{Method: "PATCH", Path: "{root}/" + colWrite + "/u-missing?currentDocument.exists=true", Body: map[string]any{"fields": Fields(map[string]any{"a": 1})}}, Recorded: "f1-update.json#rest.exists_true_on_missing",
			Expect: []Check{Error(404, "NOT_FOUND", "No document to update: {root}/"+colWrite+"/u-missing")}},
		{Request: Request{Method: "PATCH", Path: "{root}/" + colWrite + "/u-missing2?currentDocument.updateTime=" + stale, Body: map[string]any{"fields": Fields(map[string]any{"a": 1})}}, Recorded: "f1-update.json#rest.update_time_fixed_on_missing",
			Expect: []Check{Error(400, "FAILED_PRECONDITION", "the stored version (0) does not match the required base version (978307200000001)")}},
	}},

	// ---- DeleteDocument ----
	{ID: "delete/missing-and-preconditions", Must: []int{15}, Phase: "F1", Steps: []Step{
		{Request: Request{Method: "DELETE", Path: "{root}/" + colWrite + "/d-missing"}, Recorded: "f1-delete.json#rest.missing", Expect: []Check{HTTP(200), Equal("", map[string]any{})}},
		{Request: Request{Method: "DELETE", Path: "{root}/" + colWrite + "/d-missing?currentDocument.exists=true"}, Recorded: "f1-delete.json#rest.missing_exists_true",
			Expect: []Check{Error(404, "NOT_FOUND", "No document to update: {root}/"+colWrite+"/d-missing")}},
		{Request: Request{Method: "DELETE", Path: "{root}/" + colWrite + "/d-missing?currentDocument.exists=false"}, Recorded: "f1-delete.json#rest.missing_exists_false", Expect: []Check{HTTP(200), Equal("", map[string]any{})}},
		{Request: Request{Method: "DELETE", Path: "{root}/" + colWrite + "/d-missing?currentDocument.updateTime=" + stale}, Recorded: "f1-delete.json#rest.missing_update_time",
			Expect: []Check{Error(400, "FAILED_PRECONDITION", "the stored version (0) does not match the required base version (978307200000001)")}},
		Put(colWrite+"/d1", map[string]any{"a": 1}),
		{Request: Request{Method: "DELETE", Path: "{root}/" + colWrite + "/d1?currentDocument.exists=false"}, Recorded: "f1-delete.json#rest.exists_false_on_existing",
			Expect: []Check{Error(409, "ALREADY_EXISTS", "Document already exists: {root}/"+colWrite+"/d1")}},
	}},

	// ---- REST wire (MUST 20) and unknown databases (MUST 27) ----
	{ID: "rest/api-key-ignored-with-credentials", Must: []int{20}, Phase: "F1", Steps: with(getFixture(),
		Step{Request: Request{Method: "GET", Path: "{root}/" + colGet + "/g1?key=AIzaSyA-aruma-ref-invalid-key-000000000"}, Recorded: "f1-resource-names.json#rest.get.invalid_api_key_with_auth",
			Expect: []Check{HTTP(200), Equal("name", "{root}/"+colGet+"/g1")}},
	)},
	{ID: "rest/text-plain-body", Must: []int{20}, Phase: "F1", Steps: []Step{
		// Not recorded: the research's requirement (the JS SDK sends text/plain to avoid a preflight).
		{Request: Request{Method: "POST", Path: "{root}:batchGet", ContentType: "text/plain;charset=UTF-8", Body: map[string]any{"documents": []string{"{root}/" + colGet + "/nope"}}},
			Expect: []Check{HTTP(200), BatchGetAnswers(nil, names(colGet, "nope"))}},
	}},
	{ID: "database/unknown", Must: []int{27}, Phase: "F1", Steps: []Step{
		{Request: Request{Method: "GET", Path: "projects/{project}/databases/aruma-ref-nonexistent/documents/aruma-ref-f0{run}/x"}, Recorded: "f0-missing-database.json#rest.get",
			Expect: []Check{Error(404, "NOT_FOUND", "The database aruma-ref-nonexistent does not exist for project {project} Please visit https://console.cloud.google.com/datastore/setup?project={project} to add a Cloud Datastore or Cloud Firestore database. ")}},
	}},
}

// TestF1 runs the F1 cases against production's recorded answers and, with ARUMA_DOCUMENTS_URL,
// against Aruma.
func TestF1(t *testing.T) {
	Run(t, f1Cases)
}
