package ops

import (
	"strings"
	"testing"
)

// TestRestoreWhileServing: the runbook says to stop the server before a restore. One that runs
// meanwhile keeps state in memory (the Realtime Database's tree, listeners) that the restore
// does not change, so restore must refuse while a server holds this database.
func TestRestoreWhileServing(t *testing.T) {
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.mustRun("", "migrate", "up")
	d.mustStart()
	fx := populate(t, d)
	p := fx.Projects[0]
	b := takeBackup(t, d, t.TempDir())
	a := newAPI(t, d.base())
	a.must("PUT", rtdbURL(p, "after/backup", ""), bearer(fx.PAT), "\"later\"")

	out, err := d.run("", "restore", "--in", b.dump, "--yes")
	if err == nil {
		got := a.do("GET", rtdbURL(p, "after/backup", ""), bearer(fx.PAT), nil)
		t.Errorf("restore ran while the server was serving (%s); the server then answers %s for a node the backup does not hold", strings.TrimSpace(out), got)
	} else if !strings.Contains(out, "stop") {
		t.Errorf("the refusal does not say to stop the server: %s", out)
	}
}
