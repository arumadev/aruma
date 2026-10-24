package core

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// TestDatabaseURL pins the two databaseURL forms of ADR-018 that config files carry.
func TestDatabaseURL(t *testing.T) {
	for _, c := range []struct {
		opts Options
		want string
	}{
		{Options{PublicURL: "https://aruma.example.com", RTDBDomain: "rtdb.example.com"}, "https://chat-app-default-rtdb.rtdb.example.com"},
		{Options{PublicURL: "https://aruma.example.com", RTDBDomain: "example-db.com"}, "https://chat-app-default-rtdb.example-db.com"},
		// Without rtdb.domain: the development form on the main host.
		{Options{PublicURL: "http://127.0.0.1:8080"}, "http://127.0.0.1:8080/?ns=chat-app-default-rtdb"},
	} {
		if got := (&Service{opts: c.opts}).DatabaseURL("chat-app"); got != c.want {
			t.Errorf("DatabaseURL with %+v = %q, want %q", c.opts, got, c.want)
		}
	}
}

// A bucket name resolves only in the form toProject derives (ADR-022 §2).
func TestBucketProjectID(t *testing.T) {
	s := &Service{opts: Options{ProjectDomain: "aruma.example.com"}}
	for bucket, want := range map[string]string{
		"chat-app.aruma.example.com":      "chat-app",
		"chat-app.aruma.example.com.":     "",
		"Chat-App.aruma.example.com":      "",
		"chat-app.ARUMA.example.com":      "",
		"chat-app.appspot.com":            "",
		"a.b.aruma.example.com":           "",
		".aruma.example.com":              "",
		"aruma.example.com":               "",
		"chat-app.aruma.example.com.evil": "",
		"x.aruma.example.com":             "",
	} {
		got, ok := s.bucketProjectID(bucket)
		if got != want || ok != (want != "") {
			t.Errorf("bucketProjectID(%q) = %q, %v; want %q", bucket, got, ok, want)
		}
	}
	if _, ok := (&Service{}).bucketProjectID("chat-app."); ok {
		t.Error("a bucket resolves without a project domain")
	}
}

// countingDB counts the queries sent to it and finds nothing.
type countingDB struct{ queries *int }

func (d countingDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	*d.queries++
	return pgconn.CommandTag{}, pgx.ErrNoRows
}

func (d countingDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	*d.queries++
	return nil, pgx.ErrNoRows
}

func (d countingDB) QueryRow(context.Context, string, ...any) pgx.Row {
	*d.queries++
	return noRow{}
}

type noRow struct{}

func (noRow) Scan(...any) error { return pgx.ErrNoRows }

// A ref that can name no project is NOT_FOUND before any query: Postgres refuses a NUL or invalid
// UTF-8 as an error, which answered 500 (a URL's /projects/other-app%00/...). A ref that can
// name one still reaches the query.
func TestResolveProjectRefusesImpossibleRefs(t *testing.T) {
	s := &Service{}
	n := 0
	q := queries.New(countingDB{&n})
	account := &authn.Principal{Kind: authn.KindAccount, AccountID: uuid.New()}
	sa := &authn.Principal{Kind: authn.KindServiceAccount, ProjectNumber: 482915736204}
	for _, ref := range []string{
		"", "other-app\x00", "\x00", "other-app\r\n", "other\napp", "chat-app\t",
		"a" + strings.Repeat("b", 30), strings.Repeat("9", 40), "other-app\xff", "\xc3\x28",
		"Chat-App", "chat-app-", "1chat-app", "-1", "0", "0482915736204", "+482915736204", " 482915736204",
		"9223372036854775808",
	} {
		for _, p := range []*authn.Principal{account, sa} {
			n = 0
			_, err := s.resolveProject(t.Context(), q, p, ref, true)
			if apierr.CodeOf(err) != apierr.NotFound || n != 0 {
				t.Errorf("resolveProject(%q) as kind %d: %v after %d queries, want NOT_FOUND before any", ref, p.Kind, err, n)
			}
		}
	}
	for _, ref := range []string{"chat-app", "a" + strings.Repeat("b", 29), "482915736204", "9223372036854775807"} {
		n = 0
		_, err := s.resolveProject(t.Context(), q, account, ref, true)
		if apierr.CodeOf(err) != apierr.NotFound || n != 1 {
			t.Errorf("resolveProject(%q): %v after %d queries, want the query", ref, err, n)
		}
	}
}
