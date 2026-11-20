package config

import "testing"

func TestNormalizeWebHostIPv6(t *testing.T) {
	c := Default()
	c.Web.ListenHost = "::1"
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Web.ListenHost != "[::1]" {
		t.Fatalf("listen host = %q, want [::1]", c.Web.ListenHost)
	}

	c = Default()
	c.Web.ListenHost = "[::1]"
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Web.ListenHost != "[::1]" {
		t.Fatalf("bracketed host was changed: %q", c.Web.ListenHost)
	}

	c = Default()
	c.Web.ListenHost = "  localhost  "
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Web.ListenHost != "localhost" {
		t.Fatalf("hostname = %q, want localhost", c.Web.ListenHost)
	}
}
