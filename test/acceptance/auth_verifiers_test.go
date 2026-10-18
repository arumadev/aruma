package acceptance

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// The tamper suite's library verifiers: Aruma's Node and Go verification libraries, each run
// as a helper process from the library's own tree (sdk/node/admin-auth/test/tamper-verify.js,
// sdk/go/adminauth/internal/tamperverify) that answers one JSON line per token. They take part
// when ARUMA_VERIFY_NODE and ARUMA_VERIFY_GO name the commands; `test/smoke/run.sh
// tamper-libs` builds both and sets them, in CI's smoke job.

func init() {
	if cmd := os.Getenv("ARUMA_VERIFY_NODE"); cmd != "" {
		nodeLibVerify = processVerifier(cmd)
	}
	if cmd := os.Getenv("ARUMA_VERIFY_GO"); cmd != "" {
		goLibVerify = processVerifier(cmd)
	}
}

type verifyRequest struct {
	URL          string `json:"url"`
	Key          string `json:"key"`
	Token        string `json:"token"`
	CheckRevoked bool   `json:"checkRevoked"`
}

type verifyResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// verifierProc is a running helper; requests are answered in order, one at a time.
type verifierProc struct {
	mu  sync.Mutex
	in  io.Writer
	out *bufio.Scanner
}

func startVerifierProc(command string) (*verifierProc, error) {
	argv := strings.Fields(command)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "FIREBASE_AUTH_EMULATOR_HOST=")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// The helper exits when the test binary does and its stdin closes.
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	return &verifierProc{in: in, out: sc}, nil
}

func (v *verifierProc) ask(req verifyRequest) (verifyResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var res verifyResponse
	b, _ := json.Marshal(req)
	if _, err := v.in.Write(append(b, '\n')); err != nil {
		return res, err
	}
	if !v.out.Scan() {
		return res, fmt.Errorf("verifier exited: %v", v.out.Err())
	}
	return res, json.Unmarshal(v.out.Bytes(), &res)
}

// processVerifier starts the helper on first use and asks it about each token, with a key of
// the token's project.
func processVerifier(command string) libVerifier {
	var once sync.Once
	var proc *verifierProc
	var startErr error
	return func(t *testing.T, p *authProject, token string, checkRevoked bool) error {
		t.Helper()
		once.Do(func() { proc, startErr = startVerifierProc(command) })
		if startErr != nil {
			t.Fatal(startErr)
		}
		res, err := proc.ask(verifyRequest{URL: p.env.URL, Key: string(verifierKey(p)), Token: token, CheckRevoked: checkRevoked})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK {
			return nil
		}
		return errors.New(res.Error)
	}
}

var verifierKeys sync.Map // *authProject → service account key file

// verifierKey is the key file the libraries use for p: a service account of p's own project,
// as a developer's server has.
func verifierKey(p *authProject) []byte {
	if k, ok := verifierKeys.Load(p); ok {
		return k.([]byte)
	}
	_, key := p.owner.serviceAccountKey(p.id, "tamper-verifier")
	verifierKeys.Store(p, key)
	return key
}
