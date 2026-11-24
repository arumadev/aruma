package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// subsetDiff compares two captures, an earlier release's and this one's: everything the earlier
// release served must be served unchanged; a newer release may add fields.
func subsetDiff(path string, old, now any) string {
	switch x := old.(type) {
	case map[string]any:
		y, ok := now.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: %T vs %T", path, old, now)
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok {
				return fmt.Sprintf("%s/%s: lost: %s", path, k, short(xv))
			}
			if d := subsetDiff(path+"/"+k, xv, yv); d != "" {
				return d
			}
		}
		return ""
	case []any:
		y, ok := now.([]any)
		if !ok || len(x) != len(y) {
			return fmt.Sprintf("%s: %s vs %s", path, short(old), short(now))
		}
		for i := range x {
			if d := subsetDiff(fmt.Sprintf("%s[%d]", path, i), x[i], y[i]); d != "" {
				return d
			}
		}
		return ""
	}
	if fmt.Sprint(old) != fmt.Sprint(now) {
		return fmt.Sprintf("%s: %s vs %s", path, short(old), short(now))
	}
	return ""
}

// TestUpgradeFromPreviousRelease runs an earlier release (ARUMA_OLD_BIN), fills it through its
// public APIs, stops it, and brings this binary up on the same database and Storage directory
// the way the docs say: aruma migrate up, then aruma serve. Everything the old release served
// must still be served, byte for byte, and every credential it issued must still work.
func TestUpgradeFromPreviousRelease(t *testing.T) {
	old := oldBinary(t)
	d := newDeployment(t, old, "up")
	d.mustRun("", "migrate", "up")
	oldVersion := strings.TrimSpace(d.mustRun("", "version"))
	d.mustStart()
	fx := populate(t, d)
	want := normalize(t, capture(t, d, fx))
	d.stop()

	d.bin = headBinary(t)
	t.Logf("upgrading %s to %s", oldVersion, strings.TrimSpace(d.mustRun("", "version")))
	out := d.mustRun("", "migrate", "status")
	t.Logf("migrate status before: %s", strings.TrimSpace(out))
	d.mustRun("", "migrate", "up")
	if out := d.mustRun("", "migrate", "status"); !strings.Contains(out, "up to date") || strings.Contains(out, "pending") {
		t.Errorf("migrate status after migrate up: %s", out)
	}
	d.mustRun("", "migrate", "up") // twice changes nothing
	d.mustStart()
	got := normalize(t, capture(t, d, fx))
	if diff := subsetDiff("", want, got); diff != "" {
		t.Errorf("after the upgrade the server no longer serves what %s served: %s", oldVersion, diff)
	}
	verifyBehaviour(t, d, fx, true)
	finishHalfUpload(t, d, fx)
	checkMonotonic(t, newAPI(t, d.base()), fx, fx.Projects[0])
	if t.Failed() {
		t.Logf("server log:\n%s", d.log.String())
	}

	// Going back: when the upgrade migrated the schema, the earlier release refuses to run on it
	// and says why; when it did not, the earlier release still runs.
	d.stop()
	d.bin = old
	err := d.start()
	switch migrated := strings.Contains(out, "pending"); {
	case migrated && (err == nil || !strings.Contains(err.Error(), "newer")):
		t.Errorf("the earlier release on the schema this binary migrated: %v", err)
	case !migrated && err != nil:
		t.Errorf("the earlier release on an unchanged schema: %v", err)
	default:
		t.Logf("the earlier release on the upgraded database (migrated: %v): %v", migrated, firstLine(err))
	}
}

func firstLine(err error) string {
	if err == nil {
		return "starts"
	}
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// TestSchemaRefusals: the server never changes the schema and never runs on one it was not
// written for. A database that was never migrated, one that is behind, and one that a newer
// release has migrated are refused, with the command to run or the version to use.
func TestSchemaRefusals(t *testing.T) {
	bins := map[string]string{"this binary": headBinary(t)}
	if p := getenvOld(); p != "" {
		bins["the earlier release"] = p
	}
	for name, bin := range bins {
		t.Run(name, func(t *testing.T) {
			d := newDeployment(t, bin, "ref")
			ctx := context.Background()

			// Never migrated.
			err := d.start()
			if err == nil || !strings.Contains(err.Error(), "migrate up") {
				t.Fatalf("serve on a database that was never migrated: %v", err)
			}

			d.mustRun("", "migrate", "up")
			conn, cerr := pgx.Connect(ctx, d.dbURL)
			if cerr != nil {
				t.Fatal(cerr)
			}
			defer conn.Close(ctx)

			// Behind: the last documents migration is not applied.
			var v int64
			if err := conn.QueryRow(ctx, `SELECT max(version_id) FROM public.goose_documents`).Scan(&v); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(ctx, `DELETE FROM public.goose_documents WHERE version_id = $1`, v); err != nil {
				t.Fatal(err)
			}
			if err := d.start(); err == nil || !strings.Contains(err.Error(), "migrate up") {
				t.Errorf("serve on a schema that is behind: %v", err)
			}
			if _, err := conn.Exec(ctx, `INSERT INTO public.goose_documents (version_id, is_applied) VALUES ($1, true)`, v); err != nil {
				t.Fatal(err)
			}

			// Ahead: a newer release has migrated the database.
			if _, err := conn.Exec(ctx, `INSERT INTO public.goose_control (version_id, is_applied) VALUES (999, true)`); err != nil {
				t.Fatal(err)
			}
			err = d.start()
			if err == nil || !strings.Contains(err.Error(), "newer") {
				t.Errorf("serve on a schema that is newer than the binary: %v", err)
			}
			if out, err := d.run("", "migrate", "up"); err == nil || !strings.Contains(out, "newer") {
				t.Errorf("migrate up on a schema that is newer than the binary: %v %s", err, out)
			}
			if _, err := conn.Exec(ctx, `DELETE FROM public.goose_control WHERE version_id = 999`); err != nil {
				t.Fatal(err)
			}
			d.mustStart() // the database is as it was: it serves
		})
	}
}
