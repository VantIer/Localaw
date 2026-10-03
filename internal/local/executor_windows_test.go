//go:build windows

package local

import (
	"context"
	"testing"
	"unicode/utf8"
)

func TestDecodeConsoleGBK(t *testing.T) {
	// "以太网" encoded in GBK (CP936).
	in := []byte{0xD2, 0xD4, 0xCC, 0xAB, 0xCD, 0xF8}
	out := decodeWithCodePage(in, 936)
	if string(out) != "以太网" {
		t.Fatalf("decodeWithCodePage(936) = %q, want 以太网", out)
	}
	if !utf8.Valid(out) {
		t.Fatal("decoded bytes are not valid UTF-8")
	}
}

func TestShellOutputValidUTF8(t *testing.T) {
	e := NewExecutor(NewFileModule())
	res, err := e.Execute(context.Background(), "exec_cmd", map[string]any{"command": "ipconfig"})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(res) {
		t.Fatalf("shell result is not valid UTF-8: %q", res)
	}
}
