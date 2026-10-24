package blob

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The lock across processes: the test binary runs again as the other process (lockHelper).

const helperEnv = "ARUMA_BLOB_LOCK_HELPER"

// TestLockHelper is the other process, not a test: with helperEnv=hold:<root> it takes the
// lock, says "locked", and holds it until its standard input closes, then exits without
// Release; with try:<root> it tries for 300 ms and says "acquired" or "refused".
func TestLockHelper(t *testing.T) {
	mode, root, ok := strings.Cut(os.Getenv(helperEnv), ":")
	if !ok {
		t.Skip("the other process of the lock tests")
	}
	switch mode {
	case "hold":
		if _, err := AcquireLock(context.Background(), root, nil); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Println("locked")
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0) // the exit releases the lock, not Release
	case "try":
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		l, err := AcquireLock(ctx, root, nil)
		switch {
		case err == nil:
			l.Release()
			fmt.Println("acquired")
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Println("refused")
		default:
			fmt.Println("error:", err)
		}
		os.Exit(0)
	}
}

// helper starts the other process in mode on root, and returns it with its standard input.
func helper(t *testing.T, mode, root string) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode+":"+root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})
	return cmd, stdin, bufio.NewReader(stdout)
}

func line(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	s, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("the other process: %q, %v", s, err)
	}
	return strings.TrimSpace(s)
}

func TestLockRefusesASecondProcess(t *testing.T) {
	for _, end := range []string{"exit", "kill"} {
		t.Run(end, func(t *testing.T) {
			root := t.TempDir()
			cmd, stdin, out := helper(t, "hold", root)
			if got := line(t, out); got != "locked" {
				t.Fatalf("the other process: %s", got)
			}
			defer func(poll, every time.Duration) { lockPoll, lockLogEvery = poll, every }(lockPoll, lockLogEvery)
			lockPoll, lockLogEvery = 10*time.Millisecond, 50*time.Millisecond
			var logged atomic.Int64
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			_, err := AcquireLock(ctx, root, func(waited time.Duration) {
				if waited < 50*time.Millisecond {
					t.Errorf("log after %v", waited)
				}
				logged.Add(1)
			})
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "LOCK is held by another process") {
				t.Fatalf("AcquireLock while another process holds it: %v", err)
			}
			if n := logged.Load(); n < 2 || n > 6 {
				t.Errorf("log called %d times in 300 ms, every 50 ms", n)
			}

			// The lock goes with the process, however it ends.
			if end == "exit" {
				stdin.Close()
			} else {
				cmd.Process.Signal(syscall.SIGKILL)
			}
			ctx, cancel = context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			l, err := AcquireLock(ctx, root, nil)
			if err != nil {
				t.Fatalf("after the other process's %s: %v", end, err)
			}
			l.Release()
		})
	}
}

func TestLockIsRefusedToAnotherProcess(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(t.Context(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, out := helper(t, "try", root)
	if got := line(t, out); got != "refused" {
		t.Errorf("another process while this one holds the lock: %s", got)
	}
	l.Release()
	_, _, out = helper(t, "try", root)
	if got := line(t, out); got != "acquired" {
		t.Errorf("another process after Release: %s", got)
	}
}

func TestLockInOneProcess(t *testing.T) {
	root := t.TempDir()
	if _, err := AcquireLock(t.Context(), "relative", nil); err == nil {
		t.Error("a relative root is accepted")
	}
	if _, err := AcquireLock(t.Context(), filepath.Join(root, "missing"), nil); err == nil {
		t.Error("a missing root is accepted")
	}
	l, err := AcquireLock(t.Context(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second open file of LOCK, in the same process, waits too.
	defer func(poll time.Duration) { lockPoll = poll }(lockPoll)
	lockPoll = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := AcquireLock(ctx, root, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second AcquireLock: %v", err)
	}
	// A waiter gets the lock when the holder releases it.
	got := make(chan error, 1)
	go func() {
		l2, err := AcquireLock(t.Context(), root, nil)
		if err == nil {
			l2.Release()
		}
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Errorf("a second Release: %v", err)
	}
	if err := <-got; err != nil {
		t.Errorf("the waiter: %v", err)
	}
	if _, _, err := l.Instance(); !errors.Is(err, errReleased) {
		t.Errorf("Instance after Release: %v", err)
	}
	if err := l.SetInstance("x"); !errors.Is(err, errReleased) {
		t.Errorf("SetInstance after Release: %v", err)
	}
}

func TestInstance(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(t.Context(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if id, ok, err := l.Instance(); id != "" || ok || err != nil {
		t.Errorf("no marker: %q, %v, %v", id, ok, err)
	}
	// A leftover of a SetInstance cut by a crash is no marker, and the next one replaces it.
	os.WriteFile(filepath.Join(root, instanceTemp), []byte("6f1e"), 0o600)
	if _, ok, err := l.Instance(); ok || err != nil {
		t.Errorf("INSTANCE.tmp alone: %v, %v", ok, err)
	}
	for _, bad := range []string{"", "a b", "a\nb", "a/b", strings.Repeat("a", 129), "é"} {
		if err := l.SetInstance(bad); err == nil {
			t.Errorf("SetInstance(%q) is accepted", bad)
		}
	}
	id := "6f1e2d3c-4b5a-4987-8a6b-5c4d3e2f1a0b"
	if err := l.SetInstance(id); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := l.Instance(); got != id || !ok || err != nil {
		t.Errorf("Instance = %q, %v, %v", got, ok, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, instanceName)); string(b) != id+"\n" {
		t.Errorf("INSTANCE holds %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, instanceTemp)); err == nil {
		t.Error("INSTANCE.tmp is left")
	}
	if err := l.SetInstance("7a1e2d3c-4b5a-4987-8a6b-5c4d3e2f1a0b"); err == nil {
		t.Error("SetInstance replaces a marker")
	}
	if got, _, _ := l.Instance(); got != id {
		t.Errorf("the marker is now %q", got)
	}
	// A marker no write of this package leaves is an error, not an id.
	for _, content := range []string{"", "\n", "6f1e 2d3c\n", strings.Repeat("a", 200), "id\n\n"} {
		os.WriteFile(filepath.Join(root, instanceName), []byte(content), 0o600)
		if _, _, err := l.Instance(); err == nil {
			t.Errorf("INSTANCE = %q is accepted", content)
		}
	}
	os.WriteFile(filepath.Join(root, instanceName), []byte("no-newline"), 0o600)
	if got, ok, err := l.Instance(); got != "no-newline" || !ok || err != nil {
		t.Errorf("a marker written by hand: %q, %v, %v", got, ok, err)
	}
}

// FreeBytes against df(1), which other writers on the same disk may move a little meanwhile.
func TestFreeBytes(t *testing.T) {
	d := newTestDisk(t)
	got, err := d.FreeBytes()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("df", "-Pk", d.root).Output()
	if err != nil {
		t.Skipf("df: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		t.Fatalf("df: %q", out)
	}
	kb, err := strconv.ParseInt(fields[3], 10, 64) // Available, in 1024-byte blocks
	if err != nil {
		t.Fatalf("df: %q", out)
	}
	want := kb * 1024
	if diff := got - want; diff < -(256<<20) || diff > 256<<20 {
		t.Errorf("FreeBytes = %d, df says %d", got, want)
	}
	if got <= 0 {
		t.Errorf("FreeBytes = %d", got)
	}
}
