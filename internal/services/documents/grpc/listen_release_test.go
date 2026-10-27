package grpc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Open targets under a new release, and under rules that read request.time (api/rules_listen.go,
// ADR-026 §2): each is judged again at the stream's next recheck; a refused one is REMOVE with
// the cause, an allowed one sends nothing more.

// rvRules allows reading rv under cond.
func rvRules(cond string) string {
	return `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /rv/{d} { allow read: if ` + cond + `; }
  }
}`
}

// openRV opens a stream of the user's with query target 1 on rv and document target 2 on rv/a.
func openRV(t *testing.T, x *harness) (*apitest.ListenClient, string) {
	t.Helper()
	admin, user := x.env.JWT(t), apitest.UserToken(t, "user", nil)
	x.commit(t, admin, update("rv/a", boolField("pub", true)))
	l := apitest.OpenListen(t, x.client, user)
	l.Add(queryTarget(1, &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "rv"}}}))
	wantShapes(t, "query allowed", l.UntilGlobal(), "ADD[1]", "change rv/a [1]", "CURRENT[1]", "global")
	l.Add(docTarget(2, "rv/a"))
	wantShapes(t, "document allowed", l.UntilGlobal(), "ADD[2]", "change rv/a [2]", "CURRENT[2]", "global")
	return l, admin
}

// untilRemoved returns the responses up to REMOVE of target 2, the last one refused.
func untilRemoved(l *apitest.ListenClient) []*pb.ListenResponse {
	return l.Until(func(r *pb.ListenResponse) bool { return apitest.IsTargetChange(r, pb.TargetChange_REMOVE, 2) })
}

// A release that denies what an open query or document target reads refuses it, though its
// rules read no other document; nothing written after reaches the stream (the review's
// listen-release-never-reaches-open-query-targets).
func TestListenRulesNewReleaseDenies(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	x.env.PublishFirestoreRules(t, rvRules("true"))
	l, admin := openRV(t, x)
	x.env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /{document=**} { allow read, write: if false; }
  }
}`)
	wantShapes(t, "after a deny-all release", untilRemoved(l), "REMOVE[1] "+denied, "REMOVE[2] "+denied)
	x.commit(t, admin, update("rv/secret", boolField("pub", false)), update("rv/a", boolField("pub", false)))
	l.Quiet(500 * time.Millisecond)
}

// A new release that still allows the open targets sends nothing; they keep receiving commits.
func TestListenRulesNewReleaseAllows(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	x.env.PublishFirestoreRules(t, rvRules("true"))
	l, admin := openRV(t, x)
	x.env.PublishFirestoreRules(t, rvRules("request.auth != null"))
	l.Quiet(4500 * time.Millisecond) // past two rechecks
	x.commit(t, admin, update("rv/b", boolField("pub", true)))
	wantShapes(t, "still allowed", l.UntilGlobal(), "change rv/b [1]", "global")
}

// A rule that reads request.time alone refuses the open targets once the time it allows has
// passed.
func TestListenRulesRequestTime(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	until := time.Now().Add(3 * time.Second)
	x.env.PublishFirestoreRules(t, rvRules(fmt.Sprintf("request.time < timestamp.value(%d)", until.UnixMilli())))
	l, admin := openRV(t, x)
	time.Sleep(time.Until(until))
	wantShapes(t, "past the time", untilRemoved(l), "REMOVE[1] "+denied, "REMOVE[2] "+denied)
	x.commit(t, admin, update("rv/b", boolField("pub", true)))
	l.Quiet(500 * time.Millisecond)
}
