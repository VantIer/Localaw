package local

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFileOperations(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	path := filepath.Join(dir, "a.txt")

	if _, err := f.WriteFile(path, "one\ntwo\nthree"); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := f.ReadFile(path, 0, 0)
	if err != nil || !strings.Contains(out, "two") {
		t.Fatalf("read whole: %q %v", out, err)
	}

	out, err = f.ReadFile(path, 2, 2)
	if err != nil || strings.TrimSpace(out) != "two" {
		t.Fatalf("read range: %q %v", out, err)
	}

	if _, err := f.EditFile(path, "modify", 2, 2, "TWO"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	out, _ = f.ReadFile(path, 2, 2)
	if strings.TrimSpace(out) != "TWO" {
		t.Fatalf("after edit: %q", out)
	}

	if _, err := f.ListDir(dir); err != nil {
		t.Fatalf("list: %v", err)
	}
	entries, err := f.ListEntries(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v %v", entries, err)
	}

	sub := filepath.Join(dir, "sub")
	if _, err := f.CreateDir(sub); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dst := filepath.Join(sub, "b.txt")
	if _, err := f.Copy(path, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	moved := filepath.Join(sub, "c.txt")
	if _, err := f.Move(dst, moved); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := f.Delete(moved); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestDispatchUnknownAction(t *testing.T) {
	f := NewFileModule()
	if _, err := f.Dispatch("nope", nil); err == nil {
		t.Fatal("expected an error for an unknown action")
	}
}

func TestReadFileRangeValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "l1\nl2\nl3\nl4\nl5"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadFile(path, 3, 2); err == nil {
		t.Fatal("expected an error when end_line < start_line")
	}
	if out, err := f.ReadFile(path, 0, 2); err != nil || strings.TrimSpace(out) != "l1\nl2" {
		t.Fatalf("read 0..2 = %q %v", out, err)
	}
	if out, err := f.ReadFile(path, 4, 0); err != nil || !strings.Contains(out, "l4") || !strings.Contains(out, "l5") {
		t.Fatalf("read 4..end = %q %v", out, err)
	}
	if _, err := f.ReadFile(path, 0, -1); err == nil {
		t.Fatal("expected an error for a negative end_line")
	}
}

func TestEditFileValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "l1\nl2\nl3\nl4\nl5"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(path, "modify", 4, 2, "x"); err == nil {
		t.Fatal("expected an error when end_line < start_line")
	}
	// del with endLine omitted (0) must delete to the end, not duplicate content.
	if _, err := f.EditFile(path, "del", 2, 0, ""); err != nil {
		t.Fatal(err)
	}
	if out, _ := f.ReadFile(path, 0, 0); strings.TrimSpace(out) != "l1" {
		t.Fatalf("after del to end = %q", out)
	}
}

func TestEditFileAddAndAppend(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()

	mid := filepath.Join(dir, "mid.txt")
	if _, err := f.WriteFile(mid, "a\nc"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(mid, "add", 2, 0, "b"); err != nil {
		t.Fatal(err)
	}
	if out, _ := f.ReadFile(mid, 0, 0); strings.TrimSpace(out) != "a\nb\nc" {
		t.Fatalf("insert got %q", out)
	}

	end := filepath.Join(dir, "end.txt")
	if _, err := f.WriteFile(end, "a\nb"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(end, "add", 3, 0, "c"); err != nil {
		t.Fatal(err)
	}
	if out, _ := f.ReadFile(end, 0, 0); strings.TrimSpace(out) != "a\nb\nc" {
		t.Fatalf("append got %q", out)
	}
}

func TestCopySamePathKeepsSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "keep me"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Copy(path, path); err != nil {
		t.Fatalf("copy same path: %v", err)
	}
	if out, _ := f.ReadFile(path, 0, 0); out != "keep me" {
		t.Fatalf("source was altered: %q", out)
	}
}

func TestCopyDirectoryIntoSubdirRejected(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	src := filepath.Join(dir, "a")
	if _, err := f.CreateDir(src); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteFile(filepath.Join(src, "x.txt"), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Copy(src, filepath.Join(src, "sub", "b")); err == nil {
		t.Fatal("expected an error copying a directory into its own subdirectory")
	}
}

func TestRenameRejectsPathSeparator(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	path := filepath.Join(dir, "f.txt")
	if _, err := f.WriteFile(path, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Rename(path, "../evil.txt"); err == nil {
		t.Fatal("expected rename to reject a parent-relative new_name")
	}
	if _, err := f.Rename(path, "sub/evil.txt"); err == nil {
		t.Fatal("expected rename to reject a sub-path new_name")
	}
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	path := filepath.Join(dir, "old.txt")
	if _, err := f.WriteFile(path, "x"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.Rename(path, "new.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := f.ReadFile(filepath.Join(dir, "new.txt"), 0, 0); err != nil {
		t.Fatalf("read renamed: %v", err)
	}
}
