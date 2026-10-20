package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
)

// Usage (see ../README.md; run.sh starts it):
//
//	ARUMA_CLI_TOKEN=aruma_pat_… adapter -aruma http://127.0.0.1:PORT -project compat-tests
//
// It prints "ADAPTER_ADDR=host:port" once it listens, and stops on SIGINT or SIGTERM.
func main() {
	upstream := flag.String("aruma", "", "base URL of the throwaway Aruma server")
	project := flag.String("project", "", "Aruma project id (the tests' GCLOUD_PROJECT)")
	listen := flag.String("listen", "127.0.0.1:0", "address to listen on")
	flag.Parse()
	if err := run(*upstream, *project, *listen, os.Getenv("ARUMA_CLI_TOKEN")); err != nil {
		log.Fatal(err)
	}
}

func run(upstream, project, listen, token string) error {
	u, err := url.Parse(upstream)
	if err != nil || u.Host == "" || project == "" || token == "" {
		return errors.New("-aruma, -project and ARUMA_CLI_TOKEN are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := New(u, project, "", token)
	if a.apiKey, err = a.webAPIKey(ctx); err != nil {
		return err
	}
	if a.signer, err = a.newSigner(ctx); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a.Handler()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	fmt.Printf("ADAPTER_ADDR=%s\n", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// webAPIKey reads the project's active web API key from the console API.
func (a *Adapter) webAPIKey(ctx context.Context) (string, error) {
	var res struct {
		Keys []struct {
			KeyString string `json:"keyString"`
			Platform  string `json:"platform"`
			State     string `json:"state"`
		} `json:"keys"`
	}
	if err := a.call(ctx, http.MethodGet, a.consolePath("apiKeys"), nil, &res); err != nil {
		return "", err
	}
	for _, k := range res.Keys {
		if k.Platform == "WEB" && k.State == "ACTIVE" {
			return k.KeyString, nil
		}
	}
	return "", fmt.Errorf("project %s has no active web API key: create a web app first", a.project)
}
