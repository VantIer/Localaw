package toolreg

import "testing"

func TestRegistryBuiltins(t *testing.T) {
	r := New(nil)
	tools := r.Tools()
	if len(tools) == 0 {
		t.Fatal("no built-in tools")
	}
	if d, ok := r.Get("write_file"); !ok || d.Source != SourceBuiltin || d.LowRisk {
		t.Fatalf("write_file: %+v ok=%v", d, ok)
	}
	if d, ok := r.Get("read_file"); !ok || !d.LowRisk {
		t.Fatalf("read_file: %+v ok=%v", d, ok)
	}
	if !r.RequiresAuth(0, "read_file") {
		t.Fatal("mode 0 must require authorization")
	}
	if r.RequiresAuth(1, "read_file") {
		t.Fatal("mode 1 must auto-run low-risk tools")
	}
	if !r.RequiresAuth(1, "write_file") {
		t.Fatal("mode 1 must authorize high-risk tools")
	}
	if r.RequiresAuth(2, "write_file") {
		t.Fatal("mode 2 must auto-run everything")
	}
	oa := r.OpenAITools()
	if len(oa) != len(tools) {
		t.Fatalf("OpenAITools = %d, tools = %d", len(oa), len(tools))
	}
	for _, tool := range oa {
		if tool.Function.Name == "write_file" {
			if _, ok := tool.Function.Parameters["properties"]; !ok {
				t.Fatal("write_file schema has no properties")
			}
			return
		}
	}
	t.Fatal("write_file tool missing")
}
