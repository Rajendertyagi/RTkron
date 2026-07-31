package codeg

import (
    "context"
    "fmt"
    "io"
    "time"

    "github.com/hashicorp/go-retryablehttp"
)

type Client struct {
    baseURL string
    apiKey  string
    http    *retryablehttp.Client
    timeout time.Duration
}

func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
    retryClient := retryablehttp.NewClient()
    retryClient.RetryMax = 4
    retryClient.RetryWaitMin = 200 * time.Millisecond
    retryClient.RetryWaitMax = 2 * time.Second
    retryClient.Backoff = retryablehttp.DefaultBackoff
    retryClient.Logger = nil

    return &Client{
        baseURL: baseURL,
        apiKey:  apiKey,
        http:    retryClient,
        timeout: timeout,
    }
}

func (c *Client) SendPrompt(ctx context.Context, payload []byte) ([]byte, error) {
    ctx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()

    req, err := retryablehttp.NewRequest("POST", c.baseURL+"/acp/prompt", payload)
    if err != nil {
        return nil, fmt.Errorf("create request: %w", err)
    }
    req = req.WithContext(ctx)
    req.Header.Set("Content-Type", "application/json")
    if c.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+c.apiKey)
    }

    resp, err := c.http.Do(req)
    if err != nil {
        return nil, fmt.Errorf("request failed: %w", err)
    }
    defer resp.Body.Close()

    bs, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("read body: %w", err)
    }
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("codeg error status=%d body=%s", resp.StatusCode, string(bs))
    }
    return bs, nil
}

func (c *Client) AcpGetSessionSnapshot(ctx context.Context, sessionID string) ([]byte, error) {
    ctx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()
    req, err := retryablehttp.NewRequest("GET", c.baseURL+"/acp/session/"+sessionID+"/snapshot", nil)
    if err != nil {
        return nil, err
    }
    req = req.WithContext(ctx)
    if c.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+c.apiKey)
    }
    resp, err := c.http.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    bs, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("snapshot error status=%d body=%s", resp.StatusCode, string(bs))
    }
    return bs, nil
}

func (c *Client) AcpRespondPermission(ctx context.Context, pendingRequestID, decision, reason string) ([]byte, error) {
    ctx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()
    payload := fmt.Sprintf(`{"pending_request_id":"%s","decision":"%s","reason":"%s"}`, pendingRequestID, decision, reason)
    req, err := retryablehttp.NewRequest("POST", c.baseURL+"/acp/respond_permission", []byte(payload))
    if err != nil {
        return nil, err
    }
    req = req.WithContext(ctx)
    req.Header.Set("Content-Type", "application/json")
    if c.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+c.apiKey)
    }
    resp, err := c.http.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    bs, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("respond_permission error status=%d body=%s", resp.StatusCode, string(bs))
    }
    return bs, nil
}