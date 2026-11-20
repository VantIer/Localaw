// Package local implements Localaw's on-machine execution layer: file
// operations, shell commands and the linear command queue.
package local

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"localaw/internal/command"
)

// ErrInvalid marks a file operation rejected because of caller input (bad
// path, wrong type, ambiguous edit, ...) rather than an OS/I/O failure.
var ErrInvalid = errors.New("invalid file operation")

type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }

func (e *invalidError) Is(target error) bool { return target == ErrInvalid }

func invalidf(format string, args ...any) error {
	return &invalidError{msg: fmt.Sprintf(format, args...)}
}

// readFileLimit bounds whole-file reads (characters), matching the reference
// implementation.
const readFileLimit = 51200

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
		return nil, invalidf("%s is not a directory", path)
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

// CreateDir creates a directory and its parents. It is idempotent: creating a
// directory that already exists reports success and changes nothing.
func (f *FileModule) CreateDir(path string) (string, error) {
	if path == "" {
		return "", invalidf("path is required")
	}
	// MkdirAll is the source of truth for the outcome (idempotent for an
	// existing directory, and an error when the path is occupied by a file);
	// the pre-check only selects the message.
	existed := false
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return "", invalidf("%s is a file", path)
		}
		existed = true
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	if existed {
		return fmt.Sprintf("Directory already exists: %s", path), nil
	}
	return fmt.Sprintf("Successfully created directory: %s", path), nil
}

// CreateFile creates an empty file (parents included). It is idempotent:
// creating a file that already exists reports success and never truncates its
// existing content.
func (f *FileModule) CreateFile(path string) (string, error) {
	if path == "" {
		return "", invalidf("path is required")
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return "", invalidf("%s is a directory", path)
		}
		return fmt.Sprintf("File already exists: %s", path), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// Lost a creation race: treat an existing file as success.
		if os.IsExist(err) {
			return fmt.Sprintf("File already exists: %s", path), nil
		}
		return "", err
	}
	fh.Close()
	return fmt.Sprintf("Successfully created file: %s", path), nil
}

// Delete removes a file or directory. A symbolic link is removed as a link
// (Lstat, not Stat), so even a dangling link can be deleted.
func (f *FileModule) Delete(path string) (string, error) {
	if path == "" {
		return "", invalidf("path is required")
	}
	info, err := os.Lstat(path)
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
		return "", invalidf("path and new_name are required")
	}
	// new_name must be a plain name: no path separators and not a relative
	// navigation token, otherwise it could move across directories.
	if newName == "." || newName == ".." || filepath.Base(newName) != newName {
		return "", invalidf("new_name must be a plain name without path separators: %s", newName)
	}
	target := filepath.Join(filepath.Dir(path), newName)
	if _, err := os.Stat(target); err == nil {
		return "", invalidf("target name already exists: %s", newName)
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
		return "", invalidf("path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", invalidf("%s is a directory", path)
	}
	if startLine < 0 {
		startLine = 0
	}
	if endLine < 0 {
		return "", invalidf("end_line must be >= 0")
	}
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()

	if startLine <= 0 && endLine <= 0 {
		return readWholeLimited(fh, readFileLimit)
	}
	start := startLine
	if start <= 0 {
		start = 1
	}
	if endLine > 0 && endLine < start {
		return "", invalidf("end_line (%d) must be >= start_line (%d)", endLine, startLine)
	}
	return readLineRange(fh, start, endLine)
}

// readWholeLimited reads at most limit runes, streaming through a buffered
// reader so a large file is never loaded into memory in one piece. It reads one
// extra rune to distinguish an exact-length file from a truncated one.
func readWholeLimited(fh *os.File, limit int) (string, error) {
	if limit <= 0 {
		return "", nil
	}
	r := bufio.NewReaderSize(fh, readChunkSize)
	var sb strings.Builder
	for count := 0; count < limit; count++ {
		ch, _, err := r.ReadRune()
		if err == io.EOF {
			return sb.String(), nil
		}
		if err != nil {
			return "", err
		}
		sb.WriteRune(ch)
	}
	if _, _, err := r.ReadRune(); err == io.EOF {
		return sb.String(), nil
	}
	return sb.String() + fmt.Sprintf("\n\n[Output truncated at %d characters; use start_line/end_line to read further.]", limit), nil
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
		return "", invalidf("start line %d exceeds file line count (%d)", start, lineNo)
	}
	return strings.Join(kept, "\n"), nil
}

// WriteFile writes content to a file (parents included). The write is atomic:
// the content goes to a temporary file in the same directory which is renamed
// over the target, so a crash mid-write cannot leave a truncated file.
func (f *FileModule) WriteFile(path, content string) (string, error) {
	if path == "" {
		return "", invalidf("path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Preserve the permissions of an existing file; new files get 0o644.
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return "", invalidf("%s is a directory", path)
		}
		perm = info.Mode().Perm()
	}
	if err := atomicWriteFile(path, content, perm); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully wrote to: %s", path), nil
}

// maxEditFileSize bounds the size of a file read into memory for an exact-match
// edit. Larger files are rejected rather than risking an out-of-memory crash.
const maxEditFileSize = 64 << 20 // 64 MiB

// EditFile replaces an exact snippet in a file. Following Zed's edit design, the
// caller supplies the literal text to find (oldText) and its replacement
// (newText) instead of line numbers. Line-number edits silently drift out of
// sync with the file and corrupt it (duplicated or misplaced lines); an exact
// match either lands precisely or fails loudly and is reported back so the model
// can re-read the file and retry.
//
// oldText must occur exactly once unless replaceAll is set. Line-ending
// differences between oldText and the file ("\n" vs "\r\n") are tolerated. The
// write is atomic: the result is written to a temporary file in the same
// directory and renamed over the target, so a crash mid-write cannot corrupt the
// original.
func (f *FileModule) EditFile(path, oldText, newText string, replaceAll bool) (string, error) {
	if path == "" {
		return "", invalidf("path is required")
	}
	if oldText == "" {
		return "", invalidf("old_text is required and must not be empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", invalidf("%s is a directory", path)
	}
	if info.Size() > maxEditFileSize {
		return "", invalidf("%s is too large to edit (limit %d bytes)", path, maxEditFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	original := string(data)

	match, ok := resolveEditText(original, oldText)
	if !ok {
		return "", invalidf("could not find matching text in %s: old_text did not match the file. "+
			"It must match byte-for-byte, including indentation and line breaks; read the file again and copy old_text exactly", path)
	}

	// Keep the file's line-ending convention: a CRLF file stays CRLF even when
	// old_text/new_text were written with LF.
	newText = applyLineEnding(newText, detectLineEnding(original))

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(original, match, newText)
	} else {
		positions := findOverlapping(original, match, 2)
		if len(positions) > 1 {
			return "", invalidf("old_text matched multiple locations in %s at lines: %s. "+
				"Include more surrounding context in old_text to make the match unique, or set replace_all to true to replace every occurrence",
				path, matchLines(original, positions))
		}
		// resolveEditText only returns a match that occurs in the file, so
		// positions contains exactly one entry here.
		pos := positions[0]
		updated = original[:pos] + newText + original[pos+len(match):]
	}

	if updated == original {
		return fmt.Sprintf("No change made to %s: new_text equals the matched text", path), nil
	}
	if err := atomicWriteFile(path, updated, info.Mode().Perm()); err != nil {
		return "", err
	}
	n := strings.Count(original, match)
	if n <= 1 {
		return fmt.Sprintf("Successfully edited file: %s", path), nil
	}
	return fmt.Sprintf("Successfully edited file: %s (%d replacements)", path, n), nil
}

// resolveEditText returns the variant of oldText that literally occurs in s. It
// first tries oldText verbatim, then retries with the opposite line-ending
// convention so text copied using "\n" still matches a CRLF file (and vice
// versa). ok is false when no variant occurs.
func resolveEditText(s, oldText string) (string, bool) {
	if strings.Contains(s, oldText) {
		return oldText, true
	}
	if strings.Contains(oldText, "\r\n") {
		if alt := strings.ReplaceAll(oldText, "\r\n", "\n"); strings.Contains(s, alt) {
			return alt, true
		}
	}
	if strings.Contains(oldText, "\n") {
		if alt := strings.ReplaceAll(oldText, "\n", "\r\n"); strings.Contains(s, alt) {
			return alt, true
		}
	}
	return "", false
}

// detectLineEnding reports the dominant line ending of s: "\r\n" when it
// contains at least one CRLF sequence, otherwise "\n".
func detectLineEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// applyLineEnding rewrites all line endings in text to eol, so a replacement
// matches the surrounding file's convention.
func applyLineEnding(text, eol string) string {
	if text == "" {
		return text
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if eol == "\r\n" {
		return strings.ReplaceAll(normalized, "\n", "\r\n")
	}
	return normalized
}

// findOverlapping returns up to limit start offsets of sub in s, counting
// overlapping occurrences (so "aa" matches twice in "aaa"). This mirrors Zed's
// ambiguity policy: an overlapping match is not a unique location.
func findOverlapping(s, sub string, limit int) []int {
	if sub == "" || limit <= 0 {
		return nil
	}
	var out []int
	start := 0
	for len(out) < limit {
		i := strings.Index(s[start:], sub)
		if i < 0 {
			break
		}
		pos := start + i
		out = append(out, pos)
		start = pos + 1
	}
	return out
}

// matchLines renders the 1-based line numbers for the given match offsets.
func matchLines(s string, positions []int) string {
	lines := make([]string, len(positions))
	for i, pos := range positions {
		lines[i] = strconv.Itoa(strings.Count(s[:pos], "\n") + 1)
	}
	return strings.Join(lines, ", ")
}

// atomicTmpPattern names the temporary files used for atomic writes.
const atomicTmpPattern = ".localaw-tmp-*"

// atomicWriteFile writes content to path via a temporary file in the target's
// directory followed by an atomic rename, so the target is never left partially
// written.
func atomicWriteFile(path, content string, perm os.FileMode) error {
	return writeStreamAtomic(path, strings.NewReader(content), perm)
}

// WriteStream streams r to path atomically: the content goes to a temporary
// file in the target's directory and is renamed into place, so a failure never
// leaves a partial file behind. A final symlink is followed so the link's target
// is written instead of replacing the link.
func (f *FileModule) WriteStream(path string, r io.Reader, perm os.FileMode) error {
	return writeStreamAtomic(path, r, perm)
}

func writeStreamAtomic(path string, r io.Reader, perm os.FileMode) error {
	// Follow a final symlink so the write lands on the link's target (as a
	// plain open/create would) instead of replacing the link itself.
	target := resolveWriteTarget(path)
	tmp, err := os.CreateTemp(filepath.Dir(target), atomicTmpPattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// resolveWriteTarget follows a final symlink so a write lands on the link's
// target rather than replacing the link. A dangling link is resolved lexically
// so its target can still be created, matching a plain open/create.
func resolveWriteTarget(path string) string {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	link, err := os.Readlink(path)
	if err != nil {
		return path
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	return link
}

// Copy copies a file or directory.
func (f *FileModule) Copy(src, dest string) (string, error) {
	if src == "" || dest == "" {
		return "", invalidf("src and dest are required")
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
					return "", invalidf("cannot copy a directory into itself or a parent/child path: %s -> %s", src, dest)
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
		return "", invalidf("src and dest are required")
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
			command.String(params["old_text"]),
			command.String(params["new_text"]),
			toBool(params["replace_all"]),
		)
	case "copy":
		return f.Copy(command.String(params["src"]), command.String(params["dest"]))
	case "move":
		return f.Move(command.String(params["src"]), command.String(params["dest"]))
	}
	return "", invalidf("unknown file action: %s", action)
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

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		return err == nil && b
	}
	return false
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
