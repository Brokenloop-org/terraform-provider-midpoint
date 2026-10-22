// Package client is a small, framework-agnostic client for the midPoint REST
// API (JSON only, HTTP basic authentication).
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// restPath is the midPoint REST base path below the server URL.
const restPath = "/midpoint/ws/rest"

const (
	defaultTimeout   = 60 * time.Second
	defaultUserAgent = "terraform-provider-midpoint"
)

// Config configures a Client.
type Config struct {
	// URL is the midPoint server URL, e.g. https://midpoint.example.com.
	URL      string
	Username string
	Password string
	// Insecure skips TLS certificate verification.
	Insecure bool
	// CABundle holds PEM-encoded CA certificates that replace the system
	// roots when non-empty.
	CABundle []byte
	// Timeout bounds each request; zero means 60 seconds.
	Timeout   time.Duration
	UserAgent string
}

// Client talks to one midPoint server.
type Client struct {
	base      string
	username  string
	password  string
	userAgent string
	http      *http.Client
}

// ErrNotFound reports that midPoint answered 404 for an object.
var ErrNotFound = errors.New("object not found")

// APIError is a non-2xx midPoint response.
type APIError struct {
	Status int
	// Message is the message of midPoint's OperationResult, if any.
	Message string
	// Body is the raw response body.
	Body string
}

func (e *APIError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = strings.TrimSpace(e.Body)
	}
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	return fmt.Sprintf("midPoint returned HTTP %d: %s", e.Status, detail)
}

// Is makes errors.Is(err, ErrNotFound) true for 404 responses.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	// Errors below never quote cfg.URL: it might carry credentials.
	u, err := url.Parse(cfg.URL)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("invalid midPoint URL: %w", err)
	}
	if u.User != nil {
		return nil, errors.New("midPoint URL must not contain credentials; use username/password")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("invalid midPoint URL: must be an absolute http or https URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + restPath
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	tlsConfig := &tls.Config{InsecureSkipVerify: cfg.Insecure}
	if len(cfg.CABundle) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CABundle) {
			return nil, errors.New("CA bundle contains no valid PEM certificates")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	return &Client{
		base:      u.String(),
		username:  cfg.Username,
		password:  cfg.Password,
		userAgent: userAgent,
		http:      &http.Client{Timeout: timeout, Transport: transport},
	}, nil
}

// do sends one request below the REST base path. Repeated query keys (such as
// options) are sent as separate parameters. A non-nil body is sent as JSON.
// Non-2xx responses are returned as *APIError. The response body is fully
// read and closed.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, []byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return nil, nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error already names the method and URL.
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("%s %s: reading response: %w", method, target, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp, respBody, &APIError{
			Status:  resp.StatusCode,
			Message: resultMessage(respBody),
			Body:    string(respBody),
		}
	}
	return resp, respBody, nil
}

// resultMessage extracts the message of a midPoint OperationResult body,
// which is either the top-level object or nested under "operationResult".
func resultMessage(body []byte) string {
	var doc map[string]any
	if decode(body, &doc) != nil {
		return ""
	}
	if msg, ok := doc["message"].(string); ok {
		return msg
	}
	if nested, ok := doc["operationResult"].(map[string]any); ok {
		if msg, ok := nested["message"].(string); ok {
			return msg
		}
	}
	return ""
}

// decode unmarshals JSON keeping numbers as json.Number.
func decode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

// withOptions returns query values carrying each option as its own
// "options" parameter.
func withOptions(options []string) url.Values {
	q := url.Values{}
	for _, o := range options {
		q.Add("options", o)
	}
	return q
}
