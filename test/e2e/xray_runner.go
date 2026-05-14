//go:build e2e

// Package e2e contains HUE's real-binary end-to-end suite.
//
// xray_runner.go owns the lifecycle of a real xray-core process: it
// resolves the platform-appropriate binary under testdata/xray/, starts
// it with a generated config, waits for the inbound port to accept
// connections, and tears the process down on test cleanup.
//
// Binary management: `make e2e-deps` downloads xray-core releases into
// testdata/xray/<os>-<arch>/xray. The directory is gitignored. CI
// caches it.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// XrayBinaryPath returns the on-disk path the runner expects xray-core
// to live at. Caller can override via XRAY_BIN env to point at an
// already-installed binary.
func XrayBinaryPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("XRAY_BIN"); p != "" {
		return p
	}
	repo, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	bin := "xray"
	if runtime.GOOS == "windows" {
		bin = "xray.exe"
	}
	return filepath.Join(repo, "test", "e2e", "testdata", "xray", platform, bin)
}

// EnsureXrayAvailable skips the test if no usable xray binary is on
// disk and XRAY_BIN is unset. Run `make e2e-deps` first.
func EnsureXrayAvailable(t *testing.T) string {
	t.Helper()
	path := XrayBinaryPath(t)
	st, err := os.Stat(path)
	if err != nil {
		t.Skipf("xray binary not found at %s — run `make e2e-deps` (err: %v)", path, err)
	}
	if st.IsDir() {
		t.Skipf("xray path %s is a directory, not a binary", path)
	}
	if runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		t.Skipf("xray binary %s is not executable", path)
	}
	return path
}

// XrayRunner manages a single xray-core child process.
type XrayRunner struct {
	bin        string
	configPath string
	cmd        *exec.Cmd
	stdout     *os.File
	stderr     *os.File
}

// StartXray spawns xray with the given JSON config (written to a temp
// file under t.TempDir()). It waits up to 10s for the inbound port to
// accept TCP connections, then returns. Cleanup is registered via
// t.Cleanup to kill the process.
func StartXray(t *testing.T, configJSON []byte, waitPort int) *XrayRunner {
	t.Helper()
	bin := EnsureXrayAvailable(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, configJSON, 0o600); err != nil {
		t.Fatalf("write xray config: %v", err)
	}

	stdout, err := os.Create(filepath.Join(dir, "xray.stdout.log"))
	if err != nil {
		t.Fatalf("create stdout log: %v", err)
	}
	stderr, err := os.Create(filepath.Join(dir, "xray.stderr.log"))
	if err != nil {
		t.Fatalf("create stderr log: %v", err)
	}

	cmd := exec.Command(bin, "run", "-config", configPath)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start xray: %v", err)
	}

	r := &XrayRunner{
		bin:        bin,
		configPath: configPath,
		cmd:        cmd,
		stdout:     stdout,
		stderr:     stderr,
	}
	t.Cleanup(r.Stop)

	if waitPort > 0 {
		if err := waitForTCP(waitPort, 10*time.Second); err != nil {
			r.Stop()
			t.Fatalf("xray inbound :%d never came up: %v\nstderr: %s\nstdout: %s",
				waitPort, err, tailFile(stderr.Name()), tailFile(stdout.Name()))
		}
	}
	return r
}

// Stop terminates the xray process. Safe to call multiple times.
func (r *XrayRunner) Stop() {
	if r == nil || r.cmd == nil || r.cmd.Process == nil {
		return
	}
	_ = r.cmd.Process.Kill()
	_, _ = r.cmd.Process.Wait()
	r.cmd.Process = nil
	if r.stdout != nil {
		_ = r.stdout.Close()
	}
	if r.stderr != nil {
		_ = r.stderr.Close()
	}
}

// ConfigPath is the on-disk path of the running xray's config — useful
// for re-rendering + signaling reload in follow-up tests.
func (r *XrayRunner) ConfigPath() string { return r.configPath }

// FreePort grabs an OS-allocated TCP port and immediately closes the
// listener. Race window between FreePort and the next process binding
// is small enough for E2E use; production code shouldn't rely on this.
func FreePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("FreePort listen: %v", err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}

func waitForTCP(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("timeout waiting for port")
}

func tailFile(path string) string {
	const cap = 4 * 1024
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no log: " + err.Error() + ")"
	}
	if len(b) > cap {
		return string(b[len(b)-cap:])
	}
	return string(b)
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repo root (go.mod) not found")
		}
		dir = parent
	}
}

// WithContextTimeout is a small helper used by the suite to bound
// long-running orchestration steps (RPC calls, port polls). Returns a
// context plus a t.Cleanup-friendly cancel.
func WithContextTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx, cancel
}
