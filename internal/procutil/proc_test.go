package procutil

import (
	"io"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestKillTree(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", "10", "127.0.0.1")
	} else {
		cmd = exec.Command("sleep", "5")
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = KillTree(cmd)
	_ = cmd.Wait()
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("KillTree took too long: %s", elapsed)
	}
}
