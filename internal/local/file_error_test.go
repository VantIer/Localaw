package local

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileValidationErrorsAreClassified(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := NewFileModule().CreateFile(filepath.Join(dir, "sub"))
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}

	if _, err := NewFileModule().Delete(""); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty delete err = %v, want ErrInvalid", err)
	}
}
