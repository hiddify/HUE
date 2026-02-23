package main

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestCmdHueBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skip build smoke test in short mode")
	}

	cmd := exec.Command("go", "build", ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/hue failed on %s: %v\noutput:\n%s", runtime.GOOS, err, string(out))
	}
}
