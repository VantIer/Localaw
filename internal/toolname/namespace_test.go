package toolname

import "testing"

func TestIsExposed(t *testing.T) {
	if !IsExposed(Prefix + "server__tool") {
		t.Fatal("expected namespaced tool to be exposed")
	}
	if IsExposed("read_file") {
		t.Fatal("built-in tool must not be treated as exposed")
	}
}
