package controlplane

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

const maxGitHubWebhookBodyBytes = 2 << 20

type githubWebhookAPI struct {
	store  *Store
	secret []byte
	logger *slog.Logger
}

// NewGitHubWebhookHandler exposes only the signed GitHub delivery route over
// the optional public TLS listener.
func NewGitHubWebhookHandler(store *Store, secret []byte, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	api := &githubWebhookAPI{store: store, secret: append([]byte(nil), secret...), logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			writeError(w, &ServiceError{Code: "tls_required", Message: "the GitHub webhook API requires TLS", Status: http.StatusUpgradeRequired})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/webhooks/github", api.receive)
	return mux
}

func (a *githubWebhookAPI) receive(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		writeError(w, &ServiceError{Code: "tls_required", Message: "the GitHub webhook API requires TLS", Status: http.StatusUpgradeRequired})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitHubWebhookBodyBytes))
	if err != nil {
		writeError(w, &ServiceError{Code: "webhook_body_too_large", Message: "GitHub webhook body is limited to 2 MiB", Status: http.StatusRequestEntityTooLarge})
		return
	}
	if !validGitHubWebhookSignature(a.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeError(w, &ServiceError{Code: "invalid_webhook_signature", Message: "GitHub webhook signature is invalid", Status: http.StatusUnauthorized})
		return
	}
	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	if event != "pull_request" {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "matched": 0})
		return
	}
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
			Title   string `json:"title"`
			Base    struct {
				Ref string `json:"ref"`
			} `json:"base"`
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, invalid("invalid_webhook_payload", "GitHub webhook payload is not valid JSON"))
		return
	}
	if payload.Action != "opened" && payload.Action != "synchronize" {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "matched": 0})
		return
	}
	delivery := GitHubPullRequestWebhook{
		DeliveryID: r.Header.Get("X-GitHub-Delivery"), Action: payload.Action,
		RepositoryIdentity: "github.com/" + payload.Repository.FullName,
		PullRequest: protocol.GitHubPullRequestMatch{
			Number: payload.PullRequest.Number, URL: payload.PullRequest.HTMLURL,
			Title: payload.PullRequest.Title, State: "open", BaseBranch: payload.PullRequest.Base.Ref,
			HeadCommit: payload.PullRequest.Head.SHA,
		},
	}
	matched, err := a.store.AcceptGitHubPullRequestWebhook(r.Context(), delivery, body)
	if err != nil {
		a.logger.Error("github_webhook_failed", "delivery_id", delivery.DeliveryID, "error", err)
		writeError(w, err)
		return
	}
	a.logger.Info("github_webhook_accepted", "delivery_id", delivery.DeliveryID,
		"repository", delivery.RepositoryIdentity, "action", delivery.Action, "matched", matched)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "matched": matched})
}

func validGitHubWebhookSignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if len(secret) == 0 || !strings.HasPrefix(header, prefix) {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}
