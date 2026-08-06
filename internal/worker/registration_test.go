package worker

import (
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestHealthRegistrationChangedWhenOneRuntimeLosesReadiness(t *testing.T) {
	previous := health{
		State: "healthy",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimePi, Status: protocol.CapabilityReady},
		},
	}
	next := previous
	next.Capabilities = append([]protocol.Capability(nil), previous.Capabilities...)
	next.Capabilities[0].Status = protocol.CapabilityUnauthenticated

	if !healthRegistrationChanged(previous, next) {
		t.Fatal("runtime readiness change did not invalidate the advertised registration")
	}
}

func TestHealthRegistrationUnchangedForEquivalentCapabilities(t *testing.T) {
	previous := health{
		State: "healthy",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	}
	next := previous
	next.Capabilities = append([]protocol.Capability(nil), previous.Capabilities...)
	next.SourceAccess = append([]protocol.SourceAccess(nil), previous.SourceAccess...)

	if healthRegistrationChanged(previous, next) {
		t.Fatal("equivalent health invalidated the advertised registration")
	}
}

func TestStaleRegistrationCannotReenableClaimingAfterCapabilityChange(t *testing.T) {
	manager := &Manager{
		health: health{
			State: "healthy",
			Capabilities: []protocol.Capability{
				{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
				{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimePi, Status: protocol.CapabilityReady},
			},
		},
		registered: true,
	}
	_, staleGeneration := manager.registrationSnapshot()
	next := manager.health
	next.Capabilities = append([]protocol.Capability(nil), manager.health.Capabilities...)
	next.Capabilities[0].Status = protocol.CapabilityUnauthenticated
	manager.setHealth(next)

	manager.stateMutex.Lock()
	manager.completeRegistrationLocked(staleGeneration)
	registered := manager.registered
	manager.stateMutex.Unlock()
	if registered {
		t.Fatal("stale registration re-enabled claiming after runtime readiness changed")
	}
}
