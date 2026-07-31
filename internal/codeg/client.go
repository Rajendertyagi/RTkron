package codeg

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/hashicorp/go-retryablehttp"
    "github.com/sony/gobreaker"
)

type Client struct {
    baseURL string
    apiKey  string
    http    *retryablehttp.Client
    breaker *gobreaker.CircuitBreaker
    timeout time.Duration
}

func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
    retryClient := retryablehttp.NewClient()
    retryClient.RetryMax = 3
    retryClient.RetryWaitMin = 200 * time.Millisecond
    retryClient.RetryWaitMax = 2 * time.Second
    retryClient.Logger = nil

    cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
        Name:        "codeg",
        MaxRequests: 5,
        Interval:    60 * time.Second,
        Timeout:     30 * time.Second,
    })

    return &Client{
        baseURL: baseURL,
        apiKey:  apiKey,
        http:    retryClient,
        breaker: cb,
        timeout: timeout,
    }
}

func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
    var bodyReader io.Reader
    if body != nil {
        bs, err := json.Marshal(body)
        if err != nil {
            return nil, err
        }
        bodyReader = bytes.NewReader(bs)
    }
    req, err := retryablehttp.NewRequest(method, c.baseURL+path, bodyReader)
    if err != nil {
        return nil, err
    }
    req = req.WithContext(ctx)
    req.Header.Set("Content-Type", "application/json")
    if c.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+c.apiKey)
    }

    var resp *retryablehttp.Response
    operation := func() (interface{}, error) {
        r, err := c.http.Do(req)
        if err != nil {
            return nil, err
        }
        resp = r
        return r, nil
    }

    _, err = c.breaker.Execute(operation)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    bs, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("codeg error status=%d body=%s", resp.StatusCode, string(bs))
    }
    return bs, nil
}

// Placeholder helpers
func (c *Client) AcpGetSessionSnapshot(ctx context.Context, sessionID string) ([]byte, error) {
    ctx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()
    return c.doRequest(ctx, http.MethodGet, "/acp/session/"+sessionID+"/snapshot", nil)
}

func (c *Client) AcpRespondPermission(ctx context.Context, pendingRequestID, decision, reason string) ([]byte, error) {
    ctx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()
    payload := map[string]interface{}{
        "pending_request_id": pendingRequestID,
        "decision":           decision,
        "reason":             reason,
    }
    return c.doRequest(ctx, http.MethodPost, "/acp/respond_permission", payload)
}

func (c *Client) AcpPrompt(ctx context.Context, payload interface{}, idempotencyKey string) ([]byte, error) {
    ctx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()
    // include idempotency header if provided
    // retryablehttp doesn't expose header injection easily here; create a raw request
    bs, err := json.Marshal(payload)
    if err != nil {
        return nil, err
    }
    req, err := retryablehttp.NewRequest(http.MethodPost, c.baseURL+"/acp/prompt", bytes.NewReader(bs))
    if err != nil {
        return nil, err
    }
    req = req.WithContext(ctx)
    req.Header.Set("Content-Type", "application/json")
    if idempotencyKey != "" {
        req.Header.Set("Idempotency-Key", idempotencyKey)
    }
    if c.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+c.apiKey)
    }
    resp, err := c.http.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    out, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("acp_prompt error status=%d body=%s", resp.StatusCode, string(out))
    }
    return out, nil
}
