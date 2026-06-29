package runner

import "testing"

func TestParseMode(t *testing.T) {
	mode, err := ParseMode("execute")
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModeExecute {
		t.Fatalf("mode = %q", mode)
	}
}

func TestParseModeDefaultsToPlan(t *testing.T) {
	mode, err := ParseMode("")
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModePlan {
		t.Fatalf("mode = %q", mode)
	}
}

func TestParseModeRejectsUnknownMode(t *testing.T) {
	if _, err := ParseMode("chaos"); err == nil {
		t.Fatal("expected unknown mode to fail")
	}
}
