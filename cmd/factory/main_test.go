package main

import "testing"

func TestParseArgsDefaultMode(t *testing.T) {
	opts, rest, err := parseArgs([]string{"run", "cortex", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != "plan" {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if len(rest) != 3 {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestParseArgsModeFlagAfterWorkflow(t *testing.T) {
	opts, rest, err := parseArgs([]string{"run", "cortex", "standards", "--mode", "execute"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != "execute" {
		t.Fatalf("mode = %q", opts.Mode)
	}
	if len(rest) != 3 {
		t.Fatalf("rest = %#v", rest)
	}
}
