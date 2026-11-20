package command

import "testing"

func TestSpecsCountAndRisk(t *testing.T) {
	specs := All()
	if len(specs) != 14 {
		t.Fatalf("expected 14 specs, got %d", len(specs))
	}
	risk := make(map[string]bool, len(specs))
	for _, s := range specs {
		risk[s.Name] = s.LowRisk
	}
	for _, name := range []string{"get_cwd", "list_dir", "read_file"} {
		low, ok := risk[name]
		if !ok {
			t.Errorf("missing spec %s", name)
			continue
		}
		if !low {
			t.Errorf("%s should be low risk", name)
		}
	}
	for _, name := range []string{"write_file", "delete_file", "exec_cmd", "copy", "move"} {
		low, ok := risk[name]
		if !ok {
			t.Errorf("missing spec %s", name)
			continue
		}
		if low {
			t.Errorf("%s should be high risk", name)
		}
	}
}

func TestRequiresAuthRisk(t *testing.T) {
	cases := []struct {
		mode    int
		lowRisk bool
		want    bool
	}{
		{0, true, true},
		{0, false, true},
		{1, true, false},
		{1, false, true},
		{2, true, false},
		{2, false, false},
	}
	for _, c := range cases {
		if got := RequiresAuthRisk(c.mode, c.lowRisk); got != c.want {
			t.Errorf("RequiresAuthRisk(%d,%v)=%v want %v", c.mode, c.lowRisk, got, c.want)
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
