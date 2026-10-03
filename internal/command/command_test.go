package command

import "testing"

func TestSpecsCountAndRisk(t *testing.T) {
	if len(All()) != 14 {
		t.Fatalf("expected 14 specs, got %d", len(All()))
	}
	for _, name := range []string{"get_cwd", "list_dir", "read_file"} {
		if !IsLowRisk(name) {
			t.Errorf("%s should be low risk", name)
		}
	}
	for _, name := range []string{"write_file", "delete_file", "exec_cmd", "copy", "move"} {
		if IsLowRisk(name) {
			t.Errorf("%s should be high risk", name)
		}
	}
}

func TestRequiresAuth(t *testing.T) {
	cases := []struct {
		mode int
		name string
		want bool
	}{
		{0, "read_file", true},
		{0, "exec_cmd", true},
		{1, "read_file", false},
		{1, "list_dir", false},
		{1, "exec_cmd", true},
		{2, "write_file", false},
		{2, "exec_cmd", false},
	}
	for _, c := range cases {
		if got := RequiresAuth(c.mode, c.name); got != c.want {
			t.Errorf("RequiresAuth(%d,%s)=%v want %v", c.mode, c.name, got, c.want)
		}
	}
}

func TestCheckSafety(t *testing.T) {
	blocked := []string{"rm -rf /", "RM -RF /", "rm -r -f /", "format c:", "mkfs.ext4 /dev/sda", "dd if=/dev/zero of=/dev/sda"}
	for _, cmd := range blocked {
		if CheckSafety("exec_cmd", map[string]any{"command": cmd}) {
			t.Errorf("expected %q to be blocked", cmd)
		}
	}
	allowed := []string{"ls -la", "rm -rf ./build", "echo hello", "cat file.txt"}
	for _, cmd := range allowed {
		if !CheckSafety("exec_cmd", map[string]any{"command": cmd}) {
			t.Errorf("expected %q to be allowed", cmd)
		}
	}
}

func TestCheckSafetyRmVariants(t *testing.T) {
	blocked := []string{
		"rm -rf /", "rm -r -f /", "rm --recursive /", "rm -rf /*", "rm -rf *", "RM -RF /",
	}
	for _, cmd := range blocked {
		if CheckSafety("exec_cmd", map[string]any{"command": cmd}) {
			t.Errorf("expected %q to be blocked", cmd)
		}
	}
	if !CheckSafety("write_file", map[string]any{"path": "/"}) {
		t.Error("non-exec actions should always pass the safety check")
	}
}
