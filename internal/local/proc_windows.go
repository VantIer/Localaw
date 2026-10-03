//go:build windows

package local

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

const (
	// errorNotSameDevice is Windows ERROR_NOT_SAME_DEVICE (0x11), returned by
	// MoveFileEx when the source and destination are on different volumes.
	errorNotSameDevice = syscall.Errno(0x11)
	cpUTF8             = 65001
)

// shellCommand builds a cmd.exe invocation. The command line is passed verbatim
// through SysProcAttr.CmdLine so Go's CRT-style argument escaping does not
// mangle embedded quotes: exec.Command("cmd", "/C", command) would rewrite
// `"C:\Program Files\x.exe"` as `\"C:\Program Files\x.exe\"`, which cmd.exe
// then rejects.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	c := exec.CommandContext(ctx, "cmd.exe")
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd.exe /c " + command}
	return c
}

// killTree terminates the command's whole process tree: CommandContext alone
// only kills cmd.exe, leaving children running with the inherited stdout pipe
// open.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	return cmd.Process.Kill()
}

func isCrossDeviceError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == errorNotSameDevice
	}
	return false
}

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleOutputCP  = kernel32.NewProc("GetConsoleOutputCP")
	procGetOEMCP            = kernel32.NewProc("GetOEMCP")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	procWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")
)

// consoleCodePage returns the code page used for redirected console output,
// falling back to the OEM code page when the process has no console.
func consoleCodePage() uint32 {
	if cp, _, _ := procGetConsoleOutputCP.Call(); cp != 0 {
		return uint32(cp)
	}
	if cp, _, _ := procGetOEMCP.Call(); cp != 0 {
		return uint32(cp)
	}
	return 0
}

// decodeConsole converts bytes emitted by a Windows child process on a pipe
// into UTF-8. Console programs write in the active console/OEM code page (e.g.
// CP936 on Simplified Chinese Windows), not UTF-8, so a raw passthrough would
// be replaced by U+FFFD when the result is JSON-encoded.
func decodeConsole(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	cp := consoleCodePage()
	if cp != 0 && cp != cpUTF8 {
		return decodeWithCodePage(b, cp)
	}
	// The console reports UTF-8 (or is unknown): keep genuine UTF-8, but recover
	// legacy code-page bytes (e.g. a CP936 adapter name mixed into an otherwise
	// ASCII-output command) that are not valid UTF-8.
	if utf8.Valid(b) {
		return b
	}
	return decodeWithCodePage(b, oemCodePage())
}

// oemCodePage returns the system OEM code page.
func oemCodePage() uint32 {
	if cp, _, _ := procGetOEMCP.Call(); cp != 0 {
		return uint32(cp)
	}
	return 0
}

// decodeWithCodePage converts bytes in the given Windows code page to UTF-8.
func decodeWithCodePage(b []byte, cp uint32) []byte {
	if len(b) == 0 || cp == 0 || cp == cpUTF8 {
		return b
	}

	// code page bytes -> UTF-16
	n, _, _ := procMultiByteToWideChar.Call(
		uintptr(cp), 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		0, 0,
	)
	if n == 0 {
		return b
	}
	wide := make([]uint16, int(n))
	procMultiByteToWideChar.Call(
		uintptr(cp), 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&wide[0])), uintptr(n),
	)

	// UTF-16 -> UTF-8
	m, _, _ := procWideCharToMultiByte.Call(
		uintptr(cpUTF8), 0,
		uintptr(unsafe.Pointer(&wide[0])), uintptr(n),
		0, 0, 0, 0,
	)
	if m == 0 {
		return b
	}
	out := make([]byte, int(m))
	procWideCharToMultiByte.Call(
		uintptr(cpUTF8), 0,
		uintptr(unsafe.Pointer(&wide[0])), uintptr(n),
		uintptr(unsafe.Pointer(&out[0])), uintptr(m), 0, 0,
	)
	return out
}
