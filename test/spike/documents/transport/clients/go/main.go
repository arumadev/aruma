// Command spike-go runs the official Go client, cloud.google.com/go/firestore, against the
// transport spike server (../../server): a unary call (Commit through DocumentRef.Set), the
// index error's status details, a Listen stream held until a later message arrives (Snapshots),
// and a Write handshake through the generated client (apiv1). It is its own module, so Google's
// firestorepb never shares a binary with Aruma's.
//
//	go run . -addr 127.0.0.1:9100 -auth emulator
//	go run . -addr 127.0.0.1:9102 -ca ../../.generated/ca.pem -auth sa -sa ../../.generated/sa/sa-google.json
//
// The network guard: every dial through http.DefaultTransport (and its clones, which the auth
// libraries use) and every gRPC dial goes through guardDial, which allows loopback alone and
// records the rest; HTTPS_PROXY pointing at the server's refusing proxy is the second layer
// (run.sh sets it). The summary is one JSON line on stdout; the exit status is 1 on any failure.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	vkit "cloud.google.com/go/firestore/apiv1"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const project = "spike-project"

var (
	guardMu sync.Mutex
	blocked []string
)

// guardDial connects to loopback addresses alone; anything else is recorded and refused, by
// name, before any lookup.
func guardDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		guardMu.Lock()
		blocked = append(blocked, addr)
		guardMu.Unlock()
		fmt.Fprintf(os.Stderr, "[guard] blocked %s\n", addr)
		return nil, fmt.Errorf("blocked by the spike's network guard: %s", addr)
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
}

type summary struct {
	Client     string   `json:"client"`
	Label      string   `json:"label"`
	Unary      string   `json:"unary"`
	IndexError string   `json:"index_error"`
	Stream     string   `json:"stream"`
	Write      string   `json:"write"`
	Blocked    []string `json:"blocked"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9100", "the spike server")
	caFile := flag.String("ca", "", "the server's CA (TLS when set, h2c otherwise)")
	auth := flag.String("auth", "emulator", "emulator (FIRESTORE_EMULATOR_HOST), sa (a key file), exchange (the key file's token_uri), none")
	saFile := flag.String("sa", "", "the fake service account's key file, for -auth sa")
	listenFor := flag.Duration("listen-for", 90*time.Second, "how long to wait for Listen's later message")
	hold := flag.Duration("hold", 60*time.Second, "how long the stream must have been open when the later message arrives")
	label := flag.String("label", "", "a name for this run in the summary")
	flag.Parse()

	t := http.DefaultTransport.(*http.Transport)
	t.DialContext = guardDial
	var transportCreds credentials.TransportCredentials = insecure.NewCredentials()
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			fail(err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		t.TLSClientConfig = &tls.Config{RootCAs: pool}
		transportCreds = credentials.NewClientTLSFromCert(pool, "")
	}

	ctx := context.Background()
	opts := []option.ClientOption{
		option.WithEndpoint(*addr),
		option.WithGRPCDialOption(grpc.WithContextDialer(func(ctx context.Context, a string) (net.Conn, error) { return guardDial(ctx, "tcp", a) })),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(transportCreds)),
	}
	switch *auth {
	case "emulator":
		os.Setenv("FIRESTORE_EMULATOR_HOST", *addr)
		opts = opts[:0]
	case "sa":
		opts = append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, *saFile))
	case "exchange":
		// The OAuth path, chosen by the caller: the key file's token_uri gets a JWT bearer
		// grant, and the access token it returns goes to the service.
		key, err := os.ReadFile(*saFile)
		if err != nil {
			fail(err)
		}
		cfg, err := google.JWTConfigFromJSON(key, vkit.DefaultAuthScopes()...)
		if err != nil {
			fail(err)
		}
		opts = append(opts, option.WithTokenSource(cfg.TokenSource(ctx)))
	case "none":
		opts = append(opts, option.WithoutAuthentication())
	}
	s := summary{Client: "go", Label: *label}
	client, err := firestore.NewClient(ctx, project, opts...)
	if err != nil {
		fail(err)
	}
	defer client.Close()

	// Unary: Commit.
	s.Unary = result(func() error {
		_, err := client.Doc("spike/unary").Set(ctx, map[string]any{"n": 1})
		return err
	})

	// The index error: FAILED_PRECONDITION with the link in the message and a google.rpc.Help.
	s.IndexError = result(func() error {
		_, err := client.Doc("spike/index-error").Set(ctx, map[string]any{"n": 1})
		st := status.Convert(err)
		if st.Code() != codes.FailedPrecondition {
			return fmt.Errorf("got %v", err)
		}
		for _, d := range st.Details() {
			if h, ok := d.(*errdetails.Help); ok && len(h.GetLinks()) > 0 {
				return nil
			}
		}
		return fmt.Errorf("FAILED_PRECONDITION without a Help detail: %v", st.Details())
	})

	// Write handshake through the generated client, which the Admin API does not expose.
	s.Write = result(func() error { return writeHandshake(ctx, *auth, *addr, opts) })

	// Listen: the first snapshot, then the later document on the same stream.
	s.Stream = result(func() error {
		lctx, cancel := context.WithTimeout(ctx, *listenFor)
		defer cancel()
		it := client.Doc("spike/doc").Snapshots(lctx)
		defer it.Stop()
		begin := time.Now()
		first, err := it.Next()
		if err != nil && status.Code(err) != codes.NotFound {
			return fmt.Errorf("first snapshot: %w", err)
		}
		if first != nil && first.Exists() {
			return errors.New("first snapshot: the document exists")
		}
		later, err := it.Next()
		if err != nil {
			return fmt.Errorf("later snapshot after %v: %w", time.Since(begin).Round(time.Second), err)
		}
		if !later.Exists() || later.Data()["later"] != true {
			return fmt.Errorf("later snapshot: %v", later.Data())
		}
		fmt.Fprintf(os.Stderr, "later snapshot after %v on the same stream\n", time.Since(begin).Round(time.Second))
		if time.Since(begin) < *hold {
			return fmt.Errorf("the later snapshot came after %v, before the %v the stream must be held", time.Since(begin), *hold)
		}
		return nil
	})

	guardMu.Lock()
	s.Blocked = slices.Clone(blocked)
	guardMu.Unlock()
	line, _ := json.Marshal(s)
	fmt.Println(string(line))
	for _, r := range []string{s.Unary, s.IndexError, s.Stream, s.Write} {
		if r != "PASS" {
			os.Exit(1)
		}
	}
}

func writeHandshake(ctx context.Context, auth, addr string, opts []option.ClientOption) error {
	if auth == "emulator" {
		opts = []option.ClientOption{
			option.WithEndpoint(addr), option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		}
	}
	c, err := vkit.NewClient(ctx, opts...)
	if err != nil {
		return err
	}
	defer c.Close()
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ws, err := c.Write(wctx)
	if err != nil {
		return err
	}
	db := "projects/" + project + "/databases/(default)"
	if err := ws.Send(&firestorepb.WriteRequest{Database: db}); err != nil {
		return err
	}
	hs, err := ws.Recv()
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	if len(hs.GetStreamToken()) == 0 || len(hs.GetWriteResults()) != 0 {
		return fmt.Errorf("handshake response %v", hs)
	}
	err = ws.Send(&firestorepb.WriteRequest{StreamToken: hs.GetStreamToken(), Writes: []*firestorepb.Write{
		{Operation: &firestorepb.Write_Delete{Delete: db + "/documents/spike/unary"}}}})
	if err != nil {
		return err
	}
	res, err := ws.Recv()
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if len(res.GetWriteResults()) != 1 || res.GetCommitTime() == nil {
		return fmt.Errorf("write response %v", res)
	}
	return ws.CloseSend()
}

func result(f func() error) string {
	if err := f(); err != nil {
		return "FAIL: " + err.Error()
	}
	return "PASS"
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "spike-go:", err)
	os.Exit(2)
}
