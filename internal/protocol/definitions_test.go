package protocol

import "testing"

func TestDefinitionSnapshotOwnsMutableContent(t *testing.T) {
	definition := Definition{
		ID: "definition-1", Name: "Review", Prompt: "Review this repository.",
		Runtime: RuntimeCodex, AllowedTools: []string{"gh", "git"},
		TimeoutSeconds: 3600, Inputs: map[string]string{"severity": "high"}, Generation: 3,
	}
	snapshot := definition.Snapshot()
	definition.AllowedTools[0] = "changed"
	definition.Inputs["severity"] = "low"
	if snapshot.AllowedTools[0] != "gh" || snapshot.Inputs["severity"] != "high" {
		t.Fatalf("snapshot shared mutable Definition content: %#v", snapshot)
	}
}
