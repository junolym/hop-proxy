package harness

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	defaultPollInterval = 500 * time.Millisecond
	defaultTimeout      = 120 * time.Second
)

// WaitForServer polls the server until /api/version returns 200.
func WaitForServer(baseURL string, host string, timeout ...time.Duration) error {
	t := defaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}
	client := &http.Client{}
	return pollUntil(func() error {
		req, err := http.NewRequest("GET", baseURL+"/api/version", nil)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}, t)
}

// WaitForBackend polls a backend URL until it returns 200.
func WaitForBackend(url string, timeout ...time.Duration) error {
	t := defaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}
	return pollUntil(func() error {
		resp, err := http.Get(url + "/health")
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}, t)
}

// WaitForSetup polls until the server reports not-initialized (fresh DB).
func WaitForSetup(baseURL string, host string, timeout ...time.Duration) error {
	t := defaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}
	client := &http.Client{}
	return pollUntil(func() error {
		req, err := http.NewRequest("GET", baseURL+"/api/setup/status", nil)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}, t)
}

// WaitForClientOnline polls until a specific client appears online via the API.
// This requires an authenticated API client.
func WaitForClientOnline(baseURL string, host string, sessionToken string, clientID string, timeout ...time.Duration) error {
	t := defaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}

	client := &http.Client{}
	return pollUntil(func() error {
		req, err := http.NewRequest("GET", baseURL+"/api/clients", nil)
		if err != nil {
			return err
		}
		if host != "" {
			req.Host = host
		}
		req.AddCookie(&http.Cookie{
			Name:  "hopproxy_session",
			Value: sessionToken,
		})

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}

		// Parse the response to check if client is online.
		// Response format: {"data":[...], "message":""}
		var result struct {
			Data []struct {
				ID     string `json:"id"`
				Online bool   `json:"online"`
			} `json:"data"`
		}
		if err := decodeJSON(resp.Body, &result); err != nil {
			return fmt.Errorf("parse error: %w", err)
		}

		for _, c := range result.Data {
			if c.ID == clientID && c.Online {
				return nil // found and online
			}
		}
		return fmt.Errorf("client %s not online yet", clientID)
	}, t)
}

// WaitForAllClients waits for N clients to be online.
func WaitForAllClients(baseURL string, host string, sessionToken string, count int, timeout ...time.Duration) error {
	t := defaultTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}

	client := &http.Client{}
	return pollUntil(func() error {
		req, err := http.NewRequest("GET", baseURL+"/api/clients", nil)
		if err != nil {
			return err
		}
		if host != "" {
			req.Host = host
		}
		req.AddCookie(&http.Cookie{
			Name:  "hopproxy_session",
			Value: sessionToken,
		})

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}

		var result struct {
			Data []struct {
				Online bool `json:"online"`
			} `json:"data"`
		}
		if err := decodeJSON(resp.Body, &result); err != nil {
			return fmt.Errorf("parse error: %w", err)
		}

		onlineCount := 0
		for _, c := range result.Data {
			if c.Online {
				onlineCount++
			}
		}
		if onlineCount >= count {
			return nil
		}
		return fmt.Errorf("waiting for clients: %d/%d online", onlineCount, count)
	}, t)
}

// pollUntil calls fn repeatedly until it returns nil or the timeout expires.
func pollUntil(fn func() error, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	// Try immediately on first call
	if err := fn(); err == nil {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %v", timeout)
		case <-ticker.C:
			if err := fn(); err == nil {
				return nil
			}
		}
	}
}
