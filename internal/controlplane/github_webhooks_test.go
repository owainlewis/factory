package controlplane

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestGitHubWebhookSignatureDispatchAndRedelivery(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "webhook-definition", "Review pull request")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/factory")
	registerDefinitionWorker(t, store, "webhook-worker", protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: repository.RemoteIdentity,
	}, protocol.CapabilityReady, []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}})
	detail, created, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "webhook-automation", Title: "Review incoming pull requests",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Parameters: map[string]string{"severity": "high"},
		Trigger: protocol.AutomationTrigger{
			Type:    protocol.AutomationTriggerGitHubWebhook,
			Actions: []string{"synchronize", "opened"},
		},
	})
	if err != nil || !created {
		t.Fatalf("create webhook Automation: created=%t err=%v", created, err)
	}
	if detail.Automation.DefinitionID != definition.ID ||
		strings.Join(detail.Automation.Trigger.Actions, ",") != "opened,synchronize" {
		t.Fatalf("webhook Automation = %#v", detail.Automation)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.UpdateDefinition(context.Background(), definition.ID, protocol.UpdateDefinitionRequest{
		RequestKey: "remove-gh-after-webhook-enable", ExpectedGeneration: definition.Generation,
		Name: definition.Name, Prompt: definition.Prompt, Runtime: definition.Runtime,
		AllowedTools: []string{"git"}, TimeoutSeconds: definition.TimeoutSeconds,
		Inputs: definition.Inputs,
	})
	assertErrorCode(t, err, "definition_required_by_webhook")

	secret := []byte("0123456789abcdef0123456789abcdef")
	server := httptest.NewTLSServer(NewGitHubWebhookHandler(store, secret, slog.Default()))
	defer server.Close()
	body := []byte(`{"action":"opened","repository":{"full_name":"owainlewis/factory"},"pull_request":{"number":232,"html_url":"https://github.com/owainlewis/factory/pull/232","title":"Review this change","base":{"ref":"main"},"head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)

	invalid := webhookRequest(t, server.URL, body, "delivery-232", "sha256="+strings.Repeat("0", 64))
	response, err := server.Client().Do(invalid)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d", response.StatusCode)
	}
	ignoredBody := []byte(`{"action":"closed","repository":{"full_name":"owainlewis/factory"},"pull_request":{"number":232}}`)
	ignored := webhookRequest(t, server.URL, ignoredBody, "delivery-ignored", signWebhook(secret, ignoredBody))
	response, err = server.Client().Do(ignored)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("ignored signed action status = %d", response.StatusCode)
	}

	signature := signWebhook(secret, body)
	for attempt := 0; attempt < 2; attempt++ {
		request := webhookRequest(t, server.URL, body, "delivery-232", signature)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("delivery attempt %d status = %d body=%s", attempt, response.StatusCode, responseBody)
		}
	}

	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Occurrences) != 1 {
		t.Fatalf("redelivery created %d occurrences", len(current.Occurrences))
	}
	occurrence := current.Occurrences[0]
	if occurrence.DeliveryID != "delivery-232" || occurrence.Event != "pull_request" ||
		occurrence.Action != "opened" || occurrence.PullRequestNumber != 232 || occurrence.RunID == "" {
		t.Fatalf("webhook occurrence = %#v", occurrence)
	}
	run, err := store.Run(context.Background(), occurrence.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.SourceKind != "webhook" || len(run.Jobs) != 1 ||
		run.Jobs[0].Job.RepositoryRemoteIdentity != repository.RemoteIdentity {
		t.Fatalf("webhook Run = %#v", run)
	}
	if run.Run.DeliveryID != "delivery-232" || run.Run.Event != "pull_request" ||
		run.Run.Action != "opened" || run.Run.PullRequestNumber != 232 ||
		run.Run.ObservedHeadCommit != strings.Repeat("a", 40) {
		t.Fatalf("webhook Run identity = %#v", run.Run)
	}
	if !strings.Contains(run.Jobs[0].ResolvedPrompt, `"delivery_id":"delivery-232"`) ||
		!strings.Contains(run.Jobs[0].ResolvedPrompt, "Use authenticated gh CLI") {
		t.Fatalf("webhook prompt = %q", run.Jobs[0].ResolvedPrompt)
	}
	var runCount, deliveryCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE source_kind = 'webhook'`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM github_webhook_deliveries`).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || deliveryCount != 1 {
		t.Fatalf("redelivery counts: runs=%d deliveries=%d", runCount, deliveryCount)
	}
}

func TestGitHubWebhookAutomationRequiresGitHubCLI(t *testing.T) {
	store := newTestStore(t)
	definition, _, err := store.CreateDefinition(context.Background(), protocol.CreateDefinitionRequest{
		RequestKey: "webhook-without-gh", Name: "Cannot review GitHub",
		Prompt: "Review the pull request.", Runtime: protocol.RuntimeCodex,
		AllowedTools: []string{"git"}, TimeoutSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/no-gh")
	_, _, err = store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "webhook-no-gh-automation", Title: "Invalid webhook review",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	assertErrorCode(t, err, "webhook_gh_required")
}

func TestGitHubWebhookDispatchContinuesAfterOneAutomationFails(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "webhook-fanout-definition", "Review fanout")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/fanout")
	var automationIDs []string
	for index, title := range []string{"Fanout review A", "Fanout review B"} {
		detail, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
			RequestKey: "webhook-fanout-" + string(rune('a'+index)), Title: title,
			DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
			Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
			t.Fatal(err)
		}
		automationIDs = append(automationIDs, detail.Automation.ID)
	}
	failedAutomationID := automationIDs[0]
	if automationIDs[1] < failedAutomationID {
		failedAutomationID = automationIDs[1]
	}
	if _, err := store.db.Exec(`UPDATE automation_github_webhook_triggers SET parameters_json = '{"undeclared":"value"}' WHERE automation_id = ?`, failedAutomationID); err != nil {
		t.Fatal(err)
	}
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "fanout-delivery", Action: "opened", RepositoryIdentity: repository.RemoteIdentity,
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: 10, URL: "https://github.com/owainlewis/fanout/pull/10", Title: "Fan out",
			BaseBranch: "main", HeadCommit: strings.Repeat("b", 40),
		},
	}
	if _, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("fanout")); err == nil {
		t.Fatal("fanout delivery unexpectedly succeeded despite one broken Automation")
	}
	rows, err := store.db.Query(`
		SELECT occurrence.automation_id, occurrence.state
		FROM automation_occurrences occurrence
		JOIN automation_github_webhook_occurrences webhook ON webhook.occurrence_id = occurrence.id
		WHERE webhook.delivery_id = ? ORDER BY occurrence.automation_id
	`, delivery.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states := map[string]string{}
	for rows.Next() {
		var automationID, state string
		if err := rows.Scan(&automationID, &state); err != nil {
			t.Fatal(err)
		}
		states[automationID] = state
	}
	if states[failedAutomationID] != "failed" {
		t.Fatalf("failed Automation state = %q", states[failedAutomationID])
	}
	for _, automationID := range automationIDs {
		if automationID != failedAutomationID && states[automationID] != "dispatched" {
			t.Fatalf("later Automation %s state = %q", automationID, states[automationID])
		}
	}
	var runCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE source_kind = 'webhook'`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("fanout successful Run count = %d", runCount)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), failedAutomationID, false, false); err != nil {
		t.Fatal(err)
	}
	if admitted, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("fanout")); err != nil || admitted != 0 {
		t.Fatalf("disabled redelivery admitted=%d err=%v", admitted, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE source_kind = 'webhook'`).Scan(&runCount); err != nil || runCount != 1 {
		t.Fatalf("disabled redelivery Run count=%d err=%v", runCount, err)
	}
}

func TestRestoringWebhookDefinitionPreservesDisabledRepositoryBlock(t *testing.T) {
	store := newTestStore(t)
	definition := createTestDefinition(t, store, "restore-webhook-definition", "Restore webhook")
	repository := createManagedTestRepository(t, store, "github.com/owainlewis/restore-webhook")
	detail, _, err := store.CreateAutomation(context.Background(), protocol.CreateAutomationRequest{
		RequestKey: "restore-webhook-automation", Title: "Restore webhook review",
		DefinitionID: definition.ID, RepositoryIDs: []string{repository.ID},
		Trigger: protocol.AutomationTrigger{Type: protocol.AutomationTriggerGitHubWebhook, Actions: []string{"opened"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAutomationEnabled(context.Background(), detail.Automation.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, false); err != nil {
		t.Fatal(err)
	}
	archived, err := store.SetDefinitionArchived(context.Background(), definition.ID, true, definition.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDefinitionArchived(context.Background(), definition.ID, false, archived.Generation); err != nil {
		t.Fatal(err)
	}
	current, err := store.Automation(context.Background(), detail.Automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Automation.Health.Status != "blocked" || current.Automation.Health.Code != "repository_disabled" ||
		!strings.Contains(current.Automation.Health.Message, "Enable the selected repository") {
		t.Fatalf("restored webhook Automation health = %#v", current.Automation)
	}
}

func TestGitHubWebhookDeliveryIDRejectsDifferentPayload(t *testing.T) {
	store := newTestStore(t)
	delivery := GitHubPullRequestWebhook{
		DeliveryID: "same-delivery", Action: "opened", RepositoryIdentity: "github.com/owainlewis/factory",
		PullRequest: protocol.GitHubPullRequestMatch{Number: 1, URL: "https://github.com/owainlewis/factory/pull/1", BaseBranch: "main", HeadCommit: strings.Repeat("a", 40)},
	}
	if _, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("first")); err != nil {
		t.Fatal(err)
	}
	_, err := store.AcceptGitHubPullRequestWebhook(context.Background(), delivery, []byte("different"))
	assertErrorCode(t, err, "delivery_id_conflict")
}

func webhookRequest(t *testing.T, serverURL string, body []byte, deliveryID, signature string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, serverURL+"/api/v1/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	request.Header.Set("X-Hub-Signature-256", signature)
	return request
}

func signWebhook(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
