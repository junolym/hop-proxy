package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"testing"
	"time"
)

// Client wraps http.Client with cookie management and automatic CSRF token injection.
//
// Go's cookiejar may not send cookies on POST requests when the Host header
// differs from the URL host, so extra cookies (session, CSRF) are tracked
// and attached directly in RoundTrip via req.AddCookie.
type Client struct {
	*http.Client
	BaseURL string // e.g. "http://localhost:18080"
	Host    string // value for the Host header on every request

	extraCookies   []*http.Cookie
	extraCookiesMu sync.Mutex
}

// NewClient creates an HTTP client with a cookie jar.
// baseURL is the server address (e.g. "http://localhost:18080").
// host is the Host header value to send on every request.
func NewClient(baseURL, host string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		Client:  &http.Client{Jar: jar, Timeout: 30 * time.Second},
		BaseURL: baseURL,
		Host:    host,
	}
}

// SetCookie stores a cookie to be attached to every subsequent request.
// If a cookie with the same name already exists it is replaced.
func (c *Client) SetCookie(name, value string) {
	c.extraCookiesMu.Lock()
	defer c.extraCookiesMu.Unlock()
	for i, ck := range c.extraCookies {
		if ck.Name == name {
			c.extraCookies[i].Value = value
			return
		}
	}
	c.extraCookies = append(c.extraCookies, &http.Cookie{Name: name, Value: value})
}

// GetCookie returns a tracked extra cookie value (empty string if not found).
func (c *Client) GetCookie(name string) string {
	c.extraCookiesMu.Lock()
	defer c.extraCookiesMu.Unlock()
	for _, ck := range c.extraCookies {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

// Do sends an HTTP request, attaching extra cookies and setting the Host header.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.Host != "" {
		req.Host = c.Host
	}
	req.Header.Set("Accept-Encoding", "identity")

	c.extraCookiesMu.Lock()
	for _, ck := range c.extraCookies {
		req.AddCookie(ck)
	}
	c.extraCookiesMu.Unlock()

	return c.Client.Do(req)
}

// NewRequest creates a request to c.BaseURL+path.
// If body is non-nil, Content-Type is set to application/json.
func (c *Client) NewRequest(method, path string, body interface{}) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// Get sends a GET request and returns the response.
func (c *Client) Get(t *testing.T, path string) *http.Response {
	t.Helper()
	req, err := c.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("NewRequest GET %s: %v", path, err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// Post sends a POST request with a JSON body and CSRF token header.
func (c *Client) Post(t *testing.T, path string, body interface{}) *http.Response {
	t.Helper()
	return c.writeMethod(t, http.MethodPost, path, body)
}

// Put sends a PUT request with a JSON body and CSRF token header.
func (c *Client) Put(t *testing.T, path string, body interface{}) *http.Response {
	t.Helper()
	return c.writeMethod(t, http.MethodPut, path, body)
}

// Delete sends a DELETE request with a CSRF token header.
func (c *Client) Delete(t *testing.T, path string) *http.Response {
	t.Helper()
	return c.writeMethod(t, http.MethodDelete, path, nil)
}

// writeMethod handles POST/PUT/DELETE with automatic CSRF token attachment.
func (c *Client) writeMethod(t *testing.T, method, path string, body interface{}) *http.Response {
	t.Helper()
	req, err := c.NewRequest(method, path, body)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, path, err)
	}
	csrfToken := c.GetCookie("hopproxy_csrf")
	if csrfToken != "" {
		req.Header.Set("X-CSRF-Token", csrfToken)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// DecodeJSON reads and JSON-decodes an HTTP response body into v.
func DecodeJSON(r io.Reader, v interface{}) error {
	return json.NewDecoder(r).Decode(v)
}
