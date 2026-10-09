package local

import (
	"os"
	"path/filepath"
	"runtime"
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

	if _, err := f.EditFile(path, "two", "TWO", false); err != nil {
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

func TestDispatchEditFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "a\nb\n"); err != nil {
		t.Fatal(err)
	}
	out, err := f.Dispatch("edit_file", map[string]any{
		"path": path, "old_text": "b\n", "new_text": "B\n", "replace_all": "false",
	})
	if err != nil || !strings.Contains(out, "Successfully") {
		t.Fatalf("dispatch edit_file: %q %v", out, err)
	}
	if got, _ := f.ReadFile(path, 0, 0); got != "a\nB\n" {
		t.Fatalf("after dispatch edit = %q", got)
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

func TestEditFileExactReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "alpha\nbeta\ngamma\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(path, "beta", "BETA", false); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if out, _ := f.ReadFile(path, 0, 0); out != "alpha\nBETA\ngamma\n" {
		t.Fatalf("replace got %q", out)
	}
}

func TestEditFileMultilineAndDelete(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()

	// Multi-line replacement, preserving surrounding lines.
	multi := filepath.Join(dir, "multi.txt")
	if _, err := f.WriteFile(multi, "keep1\nold a\nold b\nkeep2\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(multi, "old a\nold b", "new a\nnew b\nnew c", false); err != nil {
		t.Fatal(err)
	}
	if out, _ := f.ReadFile(multi, 0, 0); out != "keep1\nnew a\nnew b\nnew c\nkeep2\n" {
		t.Fatalf("multiline got %q", out)
	}

	// Deleting a line by replacing it (with its newline) with "".
	del := filepath.Join(dir, "del.txt")
	if _, err := f.WriteFile(del, "l1\nl2\nl3\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(del, "l2\n", "", false); err != nil {
		t.Fatal(err)
	}
	if out, _ := f.ReadFile(del, 0, 0); out != "l1\nl3\n" {
		t.Fatalf("delete got %q", out)
	}
}

func TestEditFileInsertBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ins.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "a\nc\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(path, "c\n", "b\nc\n", false); err != nil {
		t.Fatal(err)
	}
	if out, _ := f.ReadFile(path, 0, 0); out != "a\nb\nc\n" {
		t.Fatalf("insert got %q", out)
	}
}

func TestEditFileRejectsMissingMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "miss.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "hello world\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(path, "not present", "x", false); err == nil {
		t.Fatal("expected an error when old_text is not found")
	}
}

func TestEditFileRejectsAmbiguousMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amb.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "dup\ndup\n"); err != nil {
		t.Fatal(err)
	}
	_, err := f.EditFile(path, "dup", "x", false)
	if err == nil || !strings.Contains(err.Error(), "multiple locations") {
		t.Fatalf("expected ambiguous-match error, got %v", err)
	}
	// The file must be untouched when the edit is rejected.
	if out, _ := f.ReadFile(path, 0, 0); out != "dup\ndup\n" {
		t.Fatalf("file altered on rejected edit: %q", out)
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "all.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "x a x b x\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(path, "x", "y", true); err != nil {
		t.Fatal(err)
	}
	if out, _ := f.ReadFile(path, 0, 0); out != "y a y b y\n" {
		t.Fatalf("replace_all got %q", out)
	}
}

func TestEditFileCRLFTolerance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crlf.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "one\r\ntwo\r\nthree\r\n"); err != nil {
		t.Fatal(err)
	}
	// old_text uses LF line endings, the file uses CRLF.
	if _, err := f.EditFile(path, "two\nthree", "TWO\nTHREE", false); err != nil {
		t.Fatalf("crlf edit: %v", err)
	}
	if out, _ := f.ReadFile(path, 0, 0); out != "one\r\nTWO\r\nTHREE\r\n" {
		t.Fatalf("crlf got %q", out)
	}
}

func TestEditFileNoDuplicateLines(t *testing.T) {
	// Regression: changing a unique line must not duplicate it or leave the
	// original behind (the failure mode of the previous line-number editor).
	path := filepath.Join(t.TempDir(), "dup.txt")
	f := NewFileModule()
	content := "header\nif x:\n    do_old()\nfooter\n"
	if _, err := f.WriteFile(path, content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(path, "    do_old()", "    do_new()", false); err != nil {
		t.Fatal(err)
	}
	out, _ := f.ReadFile(path, 0, 0)
	if strings.Contains(out, "do_old") {
		t.Fatalf("old line was duplicated/retained: %q", out)
	}
	if strings.Count(out, "do_new") != 1 {
		t.Fatalf("expected exactly one new line, got %q", out)
	}
}

func TestEditFileOverlappingMatchIsAmbiguous(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlap.txt")
	f := NewFileModule()
	if _, err := f.WriteFile(path, "aaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.EditFile(path, "aaaa", "b", false); err == nil {
		t.Fatal("expected overlapping matches to be rejected as ambiguous")
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

func TestWriteFileOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	p := filepath.Join(dir, "w.txt")
	if _, err := f.WriteFile(p, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteFile(p, "second"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.ReadFile(p, 0, 0); got != "second" {
		t.Fatalf("content = %q, want second", got)
	}
	// No temporary edit files must be left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".localaw-tmp-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWriteFilePreservesPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not meaningful on Windows")
	}
	dir := t.TempDir()
	f := NewFileModule()
	p := filepath.Join(dir, "perm.txt")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteFile(p, "y"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}
}

func TestCreateDirIdempotent(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	p := filepath.Join(dir, "d")
	if _, err := f.CreateDir(p); err != nil {
		t.Fatal(err)
	}
	out, err := f.CreateDir(p)
	if err != nil {
		t.Fatalf("second CreateDir: %v", err)
	}
	if !strings.Contains(out, "already exists") {
		t.Fatalf("result = %q", out)
	}
}

func TestCreateFileIdempotentKeepsContent(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	p := filepath.Join(dir, "f.txt")
	if _, err := f.CreateFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteFile(p, "keep"); err != nil {
		t.Fatal(err)
	}
	out, err := f.CreateFile(p)
	if err != nil {
		t.Fatalf("second CreateFile: %v", err)
	}
	if !strings.Contains(out, "already exists") {
		t.Fatalf("result = %q", out)
	}
	if got, _ := f.ReadFile(p, 0, 0); got != "keep" {
		t.Fatalf("content was truncated: %q", got)
	}
}

func TestCreateDirOnFileErrors(t *testing.T) {
	dir := t.TempDir()
	f := NewFileModule()
	p := filepath.Join(dir, "occupied")
	if _, err := f.WriteFile(p, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateDir(p); err == nil {
		t.Fatal("expected an error when the path is occupied by a file")
	}
}

func TestWriteFileFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	f := NewFileModule()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := f.WriteFile(link, "new"); err != nil {
		t.Fatalf("write via symlink: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("target content = %q, want new", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced instead of followed: %v", err)
	}
}

func TestDeleteDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	f := NewFileModule()
	link := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := f.Delete(link); err != nil {
		t.Fatalf("delete dangling symlink: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link still present: %v", err)
	}
}
