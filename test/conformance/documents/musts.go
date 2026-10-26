package documents

// Must is one server requirement of the client research
// (.cache/briefs/research/firestore-clients.md, "server MUST"; ADR-028 §7.2 takes the full list,
// not the plan's summary). Phase is the first phase whose server owes it (plan section 6); once
// phases.json marks that phase closed, TestMustCatalogue fails until a case covers it.
type Must struct {
	N     int
	Phase string
	Text  string
}

// Musts is the catalogue, numbered as in the research.
var Musts = []Must{
	// Listen
	{1, "F4", "Listen: accept add_target/remove_target with client-chosen int32 target ids, many per stream"},
	{2, "F4", "Listen: ack add_target with TargetChange ADD [id] before any data, remove_target with REMOVE [id] without cause; never unsolicited"},
	{3, "F4", "Listen: document_change with full name, update_time and create_time set, target_ids and removed_target_ids; document_delete with read_time; document_remove"},
	{4, "F4", "Listen: CURRENT with resume_token after the initial set, then global NO_CHANGE with read_time; read_time monotonic per client, across reconnects"},
	{5, "F4", "Listen: a single-document target on a missing document sends no document, then CURRENT and a global snapshot"},
	{6, "F4", "Listen: per-target errors as TargetChange REMOVE with cause; the stream stays open"},
	{7, "F4", "Listen: honour resume_token or read_time on add_target: the delta, or RESET and the full set; filter{count} when deletes are unknown"},
	{8, "F4", "Listen: ExistenceFilter count 0 or 1 for a single-document target, the exact count for a query; a bloom filter only if it matches the golden files"},
	{9, "F4", "Listen: limit is a window refilled when a member leaves; implicit __name__ ordering"},
	{10, "F4", "Listen: after RESET, resend every still-matching document before the next global snapshot"},
	// Write stream
	{11, "F4", "Write stream: answer the handshake with stream_id and a non-empty stream_token, no write_results"},
	{12, "F4", "Write stream: each request is one atomic batch, answered in order with stream_token, commit_time and write_results; accept empty writes; 10 batches in flight"},
	{13, "F2", "update with update_mask, delete, verify, update_transforms and current_document on Write, Commit and BatchWrite"},
	{14, "F4", "Write stream: batch failures end the stream with a status; replayed batches are acceptable"},
	// Unary and transactions
	{15, "F2", "Commit (with or without transaction) fails atomically: FAILED_PRECONDITION for a stale update_time, ALREADY_EXISTS for exists:false, NOT_FOUND for exists:true; verify counts"},
	{16, "F1", "BatchGetDocuments: one answer per requested name, found or missing with read_time; transaction, new_transaction (transaction in the first answer) and read_time"},
	{17, "F3", "RunQuery and RunAggregationQuery with transaction and new_transaction; aggregation aliases echoed, exactly one aggregation result"},
	{18, "F1", "BeginTransaction, Rollback, BatchWrite, ListDocuments, ListCollectionIds, PartitionQuery; ABORTED for contention; INVALID_ARGUMENT with \"transaction has expired\""},
	// Transport and wire
	{19, "F1", "gRPC google.firestore.v1.Firestore over h2 with TLS and h2c, messages of at least 17 MiB, 30 s keepalive pings tolerated"},
	{20, "F1", "REST JSON at /v1/…:{batchGet|commit|runQuery|…} and GET list; text/plain JSON bodies and ?key=; arrays for streaming methods; errors as HTTP status plus {error:{code,message,status}}"},
	{21, "F6", "WebChannel at /google.firestore.v1.Firestore/{Listen,Write}/channel"},
	{22, "F6", "Browser CORS: REST preflight with the Firestore headers; Origin echoed with Allow-Credentials"},
	// Auth and headers
	{23, "F1", "project and database from the body or google-cloud-resource-prefix, never only from x-goog-request-params"},
	{24, "F1", "Bearer Firebase ID token and x-firebase-appcheck; no header is unauthenticated; UNAUTHENTICATED for a bad or expired token; long streams closed with UNAUTHENTICATED"},
	{25, "F1", "Admin: self-signed RS256 service-account JWTs; Bearer owner only in an explicit dev mode"},
	{26, "F8", "TLS for production hosts chains to a CA in gRPC's roots.pem"},
	{27, "F1", "NOT_FOUND \"… database … does not exist\" for an unknown database; named databases allowed"},
}
