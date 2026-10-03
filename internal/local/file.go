// Package local implements Localaw's on-machine execution layer: file
// operations, shell commands and the linear command queue.
package local

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"localaw/internal/command"
)

// ReadFileLimit bounds whole-file reads (characters), matching the reference
// implementation.
const ReadFileLimit = 51200

// readChunkSize is the maximum amount read from disk in one step, so large
// files are never loaded into memory in a single allocation.
const readChunkSize = 1 << 20 // 1 MiB

// Entry is a directory listing item for the web file manager.
type Entry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// FileModule performs file operations on the local filesystem.
type FileModule struct{}

// NewFileModule creates a file module.
func NewFileModule() *FileModule { return &FileModule{} }

// GetCwd returns the absolute working directory of the process.
func (f *FileModule) GetCwd() (string, error) {
	return os.Getwd()
}

// ListEntries returns structured directory contents (web file manager).
func (f *FileModule) ListEntries(path string) ([]Entry, error) {
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		e := Entry{Name: it.Name(), IsDir: it.IsDir()}
		if fi, err := it.Info(); err == nil {
			e.Size = fi.Size()
		}
		out = append(out, e)
	}
	return out, nil
}

// ListDir renders directory contents as text for the LLM tool result.
func (f *FileModule) ListDir(path string) (string, error) {
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return fmt.Sprintf("%s is a file", path), nil
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, it := range items {
		kind := "FILE"
		var size int64
		if it.IsDir() {
			kind = "DIR"
		} else if fi, err := it.Info(); err == nil {
			size = fi.Size()
		}
		fmt.Fprintf(&b, "%-6s %12d %s\n", kind, size, it.Name())
	}
	if b.Len() == 0 {
		return "Empty directory", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// CreateDir creates a directory and its parents.
func (f *FileModule) CreateDir(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("directory already exists: %s", path)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully created directory: %s", path), nil
}

// CreateFile creates an empty file (parents included).
func (f *FileModule) CreateFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	fh, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return "", err
	}
	fh.Close()
	return fmt.Sprintf("Successfully created file: %s", path), nil
}

// Delete removes a file or directory.
func (f *FileModule) Delete(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return "", err
		}
	} else if err := os.Remove(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully deleted: %s", path), nil
}

// Rename renames a file or directory within its parent.
func (f *FileModule) Rename(path, newName string) (string, error) {
	if path == "" || newName == "" {
		return "", fmt.Errorf("path and new_name are required")
	}
	// new_name must be a plain name: no path separators and not a relative
	// navigation token, otherwise it could move across directories.
	if newName == "." || newName == ".." || filepath.Base(newName) != newName {
		return "", fmt.Errorf("new_name must be a plain name without path separators: %s", newName)
	}
	target := filepath.Join(filepath.Dir(path), newName)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("target name already exists: %s", newName)
	}
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully renamed: %s -> %s", path, newName), nil
}

// ReadFile reads a text file, optionally a 1-based inclusive line range. Reads
// are streamed so a very large file is never loaded into memory in one piece.
func (f *FileModule) ReadFile(path string, startLine, endLine int) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	if startLine < 0 {
		startLine = 0
	}
	if endLine < 0 {
		return "", fmt.Errorf("end_line must be >= 0")
	}
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()

	if startLine <= 0 && endLine <= 0 {
		return readWholeLimited(fh, ReadFileLimit)
	}
	start := startLine
	if start <= 0 {
		start = 1
	}
	if endLine > 0 && endLine < start {
		return "", fmt.Errorf("end_line (%d) must be >= start_line (%d)", endLine, startLine)
	}
	return readLineRange(fh, start, endLine)
}

// readWholeLimited reads at most limit runes, never reading more than
// readChunkSize bytes at a time, and notes when the file was longer.
func readWholeLimited(fh *os.File, limit int) (string, error) {
	var sb strings.Builder
	buf := make([]byte, readChunkSize)
	runes := 0
	eof := false
	for runes < limit {
		n, err := fh.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
			runes = utf8.RuneCountInString(sb.String())
		}
		if err == io.EOF {
			eof = true
			break
		}
		if err != nil {
			return "", err
		}
	}
	s := sb.String()
	if runes > limit {
		s = string([]rune(s)[:limit])
	}
	if runes >= limit && !eof {
		s += fmt.Sprintf("\n\n[Output truncated at %d characters; use start_line/end_line to read further.]", limit)
	}
	return s, nil
}

// readLineRange streams lines, returning the 1-based inclusive [start,end]
// range without holding the whole file in memory. end<=0 means "to the end".
func readLineRange(fh *os.File, start, end int) (string, error) {
	r := bufio.NewReaderSize(fh, readChunkSize)
	var kept []string
	lineNo := 0
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			lineNo++
			if lineNo >= start {
				kept = append(kept, strings.TrimSuffix(line, "\n"))
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		if end > 0 && lineNo >= end {
			break
		}
	}
	if start > lineNo {
		return "", fmt.Errorf("start line %d exceeds file line count (%d)", start, lineNo)
	}
	return strings.Join(kept, "\n"), nil
}

// WriteFile writes content to a file (parents included).
func (f *FileModule) WriteFile(path, content string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully wrote to: %s", path), nil
}

// EditFile edits a file by inserting, deleting or replacing lines. The original
// is streamed through a temporary file in the same directory which is then
// atomically renamed over the target, so memory use is bounded regardless of
// file size and a crash mid-write cannot corrupt the original.
func (f *FileModule) EditFile(path, operation string, startLine, endLine int, content string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	switch operation {
	case "add", "del", "modify":
	default:
		return "", fmt.Errorf("unknown operation: %s (use add, del, or modify)", operation)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	start := startLine - 1
	if start < 0 {
		start = 0
	}
	if operation != "add" && endLine > 0 && endLine < startLine {
		return "", fmt.Errorf("end_line (%d) must be >= start_line (%d)", endLine, startLine)
	}

	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".localaw-edit-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	w := bufio.NewWriter(tmp)
	r := bufio.NewReaderSize(src, readChunkSize)

	// 1. Copy the lines that precede the edit point.
	copied, last, wrote, err := copyLines(w, r, start)
	if err != nil {
		tmp.Close()
		return "", err
	}
	if copied < start {
		tmp.Close()
		return "", fmt.Errorf("start line %d exceeds file line count", startLine)
	}

	// 2. Skip the lines being deleted or replaced.
	if operation != "add" {
		skip := endLine - start
		if endLine <= 0 || skip < 0 {
			skip = int(^uint(0) >> 1) // to the end
		}
		if err := skipLines(r, skip); err != nil {
			tmp.Close()
			return "", err
		}
	}

	// 3. Write the replacement content, adding separators only where needed to
	//    keep the surrounding lines intact.
	_, peekErr := r.Peek(1)
	hasSuffix := peekErr == nil
	if operation != "del" {
		if wrote && last != '\n' {
			if err := w.WriteByte('\n'); err != nil {
				tmp.Close()
				return "", err
			}
		}
		if _, err := w.WriteString(content); err != nil {
			tmp.Close()
			return "", err
		}
		if len(content) > 0 {
			wrote = true
			last = content[len(content)-1]
		}
		if hasSuffix && (!wrote || last != '\n') {
			if err := w.WriteByte('\n'); err != nil {
				tmp.Close()
				return "", err
			}
		}
	}

	// 4. Copy the remainder of the file unchanged.
	if _, err := io.Copy(w, r); err != nil {
		tmp.Close()
		return "", err
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return "", err
	}
	// Close both handles before renaming: Windows cannot replace an open file.
	if err := src.Close(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully performed %s on file: %s", operation, path), nil
}

// copyLines copies up to n lines from r to dst, returning the number copied,
// the last byte written and whether anything was written.
func copyLines(dst *bufio.Writer, r *bufio.Reader, n int) (int, byte, bool, error) {
	copied := 0
	var last byte
	wrote := false
	for copied < n {
		b, err := r.ReadBytes('\n')
		if len(b) > 0 {
			if _, werr := dst.Write(b); werr != nil {
				return copied, last, wrote, werr
			}
			last = b[len(b)-1]
			wrote = true
			copied++
		}
		if err != nil {
			if err == io.EOF {
				return copied, last, wrote, nil
			}
			return copied, last, wrote, err
		}
	}
	return copied, last, wrote, nil
}

// skipLines discards up to n lines from r.
func skipLines(r *bufio.Reader, n int) error {
	for i := 0; i < n; i++ {
		b, err := r.ReadBytes('\n')
		if len(b) == 0 && err == io.EOF {
			return nil
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
	return nil
}

// Copy copies a file or directory.
func (f *FileModule) Copy(src, dest string) (string, error) {
	if src == "" || dest == "" {
		return "", fmt.Errorf("src and dest are required")
	}
	// Guard against copying a file onto itself, which would truncate the
	// source when the destination is opened with O_TRUNC.
	if filepath.Clean(src) == filepath.Clean(dest) {
		return fmt.Sprintf("Source and destination are the same; nothing to copy: %s", src), nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		// Reject any parent/child relationship between src and dest, which
		// would make a recursive copy walk into its own output.
		if as, e1 := filepath.Abs(src); e1 == nil {
			if ad, e2 := filepath.Abs(dest); e2 == nil {
				if isSubPath(as, ad) || isSubPath(ad, as) {
					return "", fmt.Errorf("cannot copy a directory into itself or a parent/child path: %s -> %s", src, dest)
				}
			}
		}
		if err := copyTree(src, dest); err != nil {
			return "", err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := copyFile(src, dest); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Successfully copied: %s -> %s", src, dest), nil
}

// Move moves a file or directory.
func (f *FileModule) Move(src, dest string) (string, error) {
	if src == "" || dest == "" {
		return "", fmt.Errorf("src and dest are required")
	}
	if _, err := os.Stat(src); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, dest); err != nil {
		// Only cross-device moves need a copy + delete fallback; any other
		// rename failure (existing target, permissions, ...) is reported as-is.
		if !isCrossDeviceError(err) {
			return "", err
		}
		if _, cerr := f.Copy(src, dest); cerr != nil {
			return "", err
		}
		if _, derr := f.Delete(src); derr != nil {
			return "", derr
		}
	}
	return fmt.Sprintf("Successfully moved: %s -> %s", src, dest), nil
}

// Dispatch routes a file-oriented action to its implementation.
func (f *FileModule) Dispatch(action string, params map[string]any) (string, error) {
	switch action {
	case "get_cwd":
		return f.GetCwd()
	case "list_dir":
		return f.ListDir(command.String(params["path"]))
	case "make_dir":
		return f.CreateDir(command.String(params["path"]))
	case "create_file":
		return f.CreateFile(command.String(params["path"]))
	case "delete_file", "delete_dir":
		return f.Delete(command.String(params["path"]))
	case "rename_file", "rename_dir":
		return f.Rename(command.String(params["path"]), command.String(params["new_name"]))
	case "read_file":
		return f.ReadFile(command.String(params["path"]), toInt(params["start_line"]), toInt(params["end_line"]))
	case "write_file":
		return f.WriteFile(command.String(params["path"]), command.String(params["content"]))
	case "edit_file":
		return f.EditFile(
			command.String(params["path"]),
			command.String(params["operation"]),
			toInt(params["start_line"]),
			toInt(params["end_line"]),
			command.String(params["content"]),
		)
	case "copy":
		return f.Copy(command.String(params["src"]), command.String(params["dest"]))
	case "move":
		return f.Move(command.String(params["src"]), command.String(params["dest"]))
	}
	return "", fmt.Errorf("unknown file action: %s", action)
}

func toInt(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
		return 0
	}
	return 0
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyTree(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

// isSubPath reports whether child is parent itself or lies inside parent.
func isSubPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
