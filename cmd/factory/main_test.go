package main

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestParseArgsForceFlag(t *testing.T) {
	opts, rest, err := parseArgs([]string{"init", "somedir", "--force"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Force {
		t.Fatal("expected force to be set")
	}
	if len(rest) != 2 || rest[0] != "init" || rest[1] != "somedir" {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestRunInitBootstrapsDir(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"init", dir}); err != nil {
		t.Fatalf("run init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".factory", "STANDARDS.md")); err != nil {
		t.Fatalf("expected STANDARDS.md: %v", err)
	}
}
