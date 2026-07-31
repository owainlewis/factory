package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

type controlPlaneClient struct {
	baseURL string
	http    *http.Client
}

type controlPlaneError struct {
	Status  int
	Code    string
	Message string
}

func (err *controlPlaneError) Error() string {
	return fmt.Sprintf("control plane returned %d %s: %s", err.Status, err.Code, err.Message)
}

func newControlPlaneClient(server string, httpClient *http.Client) *controlPlaneClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &controlPlaneClient{baseURL: strings.TrimRight(server, "/"), http: httpClient}
}

func (client *controlPlaneClient) workers(ctx context.Context) ([]protocol.Worker, error) {
	var response struct {
		Workers []protocol.Worker `json:"workers"`
	}
	if _, err := client.request(ctx, http.MethodGet, "/api/v1/workers", nil, &response); err != nil {
		return nil, err
	}
	return response.Workers, nil
}

func (client *controlPlaneClient) createTask(
	ctx context.Context,
	input protocol.CreateTaskRequest,
) (protocol.TaskDetail, error) {
	var task protocol.TaskDetail
	_, err := client.request(ctx, http.MethodPost, "/api/v1/tasks", input, &task)
	return task, err
}

func (client *controlPlaneClient) request(
	ctx context.Context,
	method string,
	path string,
	input any,
	output any,
) (int, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, fmt.Errorf("encode control-plane request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return 0, fmt.Errorf("create control-plane request: %w", err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("call control plane: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 1<<20+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return response.StatusCode, fmt.Errorf("read control-plane response: %w", err)
	}
	if len(responseBody) > 1<<20 {
		return response.StatusCode, errors.New("control-plane response exceeded 1 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var value protocol.ErrorBody
		if err := json.Unmarshal(responseBody, &value); err == nil && value.Error.Code != "" {
			return response.StatusCode, &controlPlaneError{
				Status: response.StatusCode, Code: value.Error.Code, Message: value.Error.Message,
			}
		}
		return response.StatusCode, fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	if output != nil {
		if err := json.Unmarshal(responseBody, output); err != nil {
			return response.StatusCode, fmt.Errorf("decode control-plane response: %w", err)
		}
	}
	return response.StatusCode, nil
}
