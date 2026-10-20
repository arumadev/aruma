// Package smoke initialises the Go Admin SDK with an Aruma service account key file, with all
// network access blocked: the SDK must accept the file without contacting anything.
package smoke

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"
)

var (
	mu       sync.Mutex
	attempts []string
)

// TestMain blocks every dial through the default transport, which the Google API transports
// clone.
func TestMain(m *testing.M) {
	t := http.DefaultTransport.(*http.Transport)
	t.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		attempts = append(attempts, network+" "+addr)
		mu.Unlock()
		return nil, errors.New("network access blocked by the smoke test")
	}
	t.DialTLSContext = t.DialContext
	code := m.Run()
	if len(attempts) > 0 {
		println("network access attempted:", attempts[0])
		code = 1
	}
	os.Exit(code)
}

func dir() string {
	if d := os.Getenv("ARUMA_SMOKE_DIR"); d != "" {
		return d
	}
	return filepath.Join("..", ".generated")
}

func TestAdminSDKAcceptsKeyFile(t *testing.T) {
	keyFile := filepath.Join(dir(), "service-account.json")

	ctx := context.Background()
	app, err := firebase.NewApp(ctx, nil, option.WithCredentialsFile(keyFile))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	// The Auth client reads the project id and the signing identity from the key file.
	if _, err := app.Auth(ctx); err != nil {
		t.Fatalf("Auth: %v", err)
	}
}

// TestDatabaseURLForms records how the Go SDK reads databaseURL, the evidence behind ADR-018:
// any non-https URL is taken for an emulator "host:port" and refused when it contains "//"; an
// https URL is used verbatim as the base that paths are appended to. The generated configs carry
// https://<ns>.<rtdb.domain>, or without rtdb.domain the development form on public_url, which
// this SDK takes only as localhost:<port>?ns=<ns> (no scheme).
func TestDatabaseURLForms(t *testing.T) {
	ctx := context.Background()
	keyFile := filepath.Join(dir(), "service-account.json")
	for url, ok := range map[string]bool{
		"http://127.0.0.1:8080/?ns=smoke-app-default-rtdb":      false,
		"localhost:8080?ns=smoke-app-default-rtdb":              true,
		"https://aruma.example.com/?ns=smoke-app-default-rtdb":  true,
		"https://smoke-app-default-rtdb.aruma.example.com":      true,
		"https://smoke-app-default-rtdb.rtdb.aruma.example.com": true,
	} {
		app, err := firebase.NewApp(ctx, &firebase.Config{DatabaseURL: url}, option.WithCredentialsFile(keyFile))
		if err != nil {
			t.Fatal(err)
		}
		_, err = app.Database(ctx)
		if (err == nil) != ok {
			t.Errorf("Database(%q): err = %v, want accepted = %v", url, err, ok)
		}
	}
}
