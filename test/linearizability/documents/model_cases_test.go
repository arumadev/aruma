package documents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"
)

// op builds a fixture operation; the tests below need no database.
func op(call, ret int64, in input, out output) fixtureOp {
	return fixtureOp{Call: call, Return: ret, In: &in, Out: &out}
}

func set(key int, n int64) write { return write{Key: key, Kind: wSet, N: n} }

func doc(key int, n, ut int64) observation { return observation{Key: key, Exists: true, N: n, UT: ut} }

func committed(ct, seq int64, uts ...int64) output {
	return output{Outcome: outOK, CommitTime: ct, Seq: seq, UpdateTimes: uts, Incr: make([]int64, len(uts))}
}

func read(rt int64, obs ...observation) output {
	return output{Outcome: outOK, Reads: obs, ReadTime: rt}
}

// The model accepts what the store may do and refuses each kind of anomaly the harness looks for.
func TestModel(t *testing.T) {
	setK0 := input{Kind: opCommit, Writes: []write{set(0, 5)}}
	cases := map[string]struct {
		legal bool
		ops   []fixtureOp
	}{
		"read after commit": {true, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opRead}, read(10, doc(0, 5, 10))),
		}},
		"stale read": {false, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opRead}, read(10, observation{Key: 0})),
		}},
		"concurrent read may see either": {true, []fixtureOp{
			op(0, 4, setK0, committed(10, 1, 10)),
			op(1, 2, input{Kind: opRead}, read(9, observation{Key: 0})),
			op(1, 3, input{Kind: opRead}, read(10, doc(0, 5, 10))),
		}},
		"read time below the commit it saw": {false, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opRead}, read(9, doc(0, 5, 10))),
		}},
		"lost update": {false, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 6, input{Kind: opTxn, Writes: []write{set(0, 6)}}, output{Outcome: outOK, Reads: []observation{doc(0, 5, 10)}, CommitTime: 20, Seq: 2, UpdateTimes: []int64{20}, Incr: []int64{0}}),
			op(3, 7, input{Kind: opTxn, Writes: []write{set(0, 6)}}, output{Outcome: outOK, Reads: []observation{doc(0, 5, 10)}, CommitTime: 30, Seq: 3, UpdateTimes: []int64{30}, Incr: []int64{0}}),
		}},
		"commit time goes back": {false, []fixtureOp{
			op(0, 1, setK0, committed(20, 1, 20)),
			op(2, 3, input{Kind: opCommit, Writes: []write{set(1, 7)}}, committed(15, 2, 15)),
		}},
		"commit at or before a read time served": {false, []fixtureOp{
			op(0, 1, input{Kind: opRead}, read(20, observation{Key: 0})),
			op(2, 3, setK0, committed(20, 1, 20)),
		}},
		"a snapshot mixes two commits": {false, []fixtureOp{
			op(0, 1, input{Kind: opCommit, Writes: []write{set(0, 6), set(1, 7)}}, committed(20, 1, 20, 20)),
			op(0, 1, input{Kind: opSnapshot}, read(20, doc(0, 6, 20), observation{Key: 1})),
		}},
		"read at a time between commits": {true, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opCommit, Writes: []write{set(0, 6)}}, committed(20, 2, 20)),
			op(0, 4, input{Kind: opReadAt, At: 15}, read(15, doc(0, 5, 10))),
		}},
		"read at a time sees a later commit": {false, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opCommit, Writes: []write{set(0, 6)}}, committed(20, 2, 20)),
			op(0, 4, input{Kind: opReadAt, At: 15}, read(15, doc(0, 6, 20))),
		}},
		"unknown outcome that happened": {true, []fixtureOp{
			op(0, 1, setK0, output{Outcome: outUnknown}),
			op(2, 3, input{Kind: opRead}, read(33, doc(0, 5, 33))),
			op(4, 5, input{Kind: opRead}, read(34, doc(0, 5, 33))),
		}},
		"unknown outcome that did not": {true, []fixtureOp{
			op(0, 1, setK0, output{Outcome: outUnknown}),
			op(2, 3, input{Kind: opRead}, read(33, observation{Key: 0})),
		}},
		"unknown outcome binds one update_time": {false, []fixtureOp{
			op(0, 1, setK0, output{Outcome: outUnknown}),
			op(2, 3, input{Kind: opRead}, read(33, doc(0, 5, 33))),
			op(4, 5, input{Kind: opRead}, read(35, doc(0, 5, 34))),
		}},
		"refused for the right reason": {true, []fixtureOp{
			op(0, 1, input{Kind: opCommit, Writes: []write{{Key: 0, Kind: wDelete, Pre: precondition{Exists: "exists"}}}}, output{Outcome: outRefused, Code: codeNotFound}),
		}},
		"refused for the wrong reason": {false, []fixtureOp{
			op(0, 1, input{Kind: opCommit, Writes: []write{{Key: 0, Kind: wDelete, Pre: precondition{Exists: "exists"}}}}, output{Outcome: outRefused, Code: codeAlreadyExists}),
		}},
		"stale update_time refused": {true, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opCommit, Writes: []write{{Key: 0, Kind: wSet, N: 6, Pre: precondition{UpdateTime: 9}}}}, output{Outcome: outRefused, Code: codeFailedPrecondition}),
		}},
		"increment result": {true, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opCommit, Writes: []write{{Key: 0, Kind: wIncr, N: 2}}}, output{Outcome: outOK, CommitTime: 20, Seq: 2, UpdateTimes: []int64{20}, Incr: []int64{7}}),
		}},
		"wrong increment result": {false, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opCommit, Writes: []write{{Key: 0, Kind: wIncr, N: 2}}}, output{Outcome: outOK, CommitTime: 20, Seq: 2, UpdateTimes: []int64{20}, Incr: []int64{8}}),
		}},
		"deleting a missing document changes nothing": {true, []fixtureOp{
			op(0, 1, setK0, committed(10, 1, 10)),
			op(2, 3, input{Kind: opCommit, Writes: []write{{Key: 1, Kind: wDelete}}}, output{Outcome: outOK, CommitTime: 10, UpdateTimes: []int64{0}, Incr: []int64{0}}),
		}},
		"a commit that changes nothing takes no seq": {false, []fixtureOp{
			op(2, 3, input{Kind: opCommit, Writes: []write{{Key: 1, Kind: wDelete}}}, output{Outcome: outOK, CommitTime: 10, Seq: 1, UpdateTimes: []int64{0}, Incr: []int64{0}}),
		}},
	}
	m := model.ToModel()
	for name, c := range cases {
		got := porcupine.CheckOperations(m, fromFixture(c.ops))
		if got != c.legal {
			t.Errorf("%s: linearizable %v, want %v", name, got, c.legal)
		}
	}

	// A minimized history stays illegal, and a fixture round-trips through JSON.
	lost := fromFixture(cases["lost update"].ops)
	small := minimize(m, lost, time.Now().Add(time.Minute))
	if porcupine.CheckOperations(m, small) {
		t.Fatal("the minimized history is legal")
	}
	b, err := json.Marshal(fixture{Want: string(porcupine.Illegal), Ops: toFixture(small)})
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil || porcupine.CheckOperations(m, fromFixture(f.Ops)) {
		t.Fatalf("the fixture did not round-trip: %v", err)
	}
}

// TestFixtures checks the histories saved in testdata/: each must get its verdict, so that a
// violation once found stays found by the model.
func TestFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.json"))
	m := model.ToModel()
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var f fixture
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := porcupine.CheckOperationsTimeout(m, fromFixture(f.Ops), 0); string(got) != f.Want {
			t.Errorf("%s: %s, want %s (%s)", name, got, f.Want, f.Note)
		}
	}
}
