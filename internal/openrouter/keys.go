// Package openrouter is a small client for OpenRouter's key management API
// (https://openrouter.ai/docs: provisioning keys). A provisioning key can
// create, read, change and delete ordinary API keys; it cannot call models.
// The client never logs, and its errors carry the HTTP status and
// OpenRouter's short error message only, never a request or response body,
// so neither key can leak through an error.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/trust"
)

// DefaultBase is OpenRouter's API root. Apps call models under it; the
// key management API is under its /keys.
const DefaultBase = "https://openrouter.ai/api/v1"

// Resets are the allowance periods OpenRouter accepts; "" is no reset.
var Resets = []string{"daily", "weekly", "monthly"}

// ValidReset says whether r is a reset period OpenRouter accepts, or "".
func ValidReset(r string) bool {
	if r == "" {
		return true
	}
	for _, v := range Resets {
		if r == v {
			return true
		}
	}
	return false
}

// keysTransport is the key API's pool: model-provider traffic, so it
// trusts the inference use (internal/trust).
var keysTransport = trust.Lazy(trust.Inference)

// Client calls the key management API with a provisioning key.
type Client struct {
	Base            string // DefaultBase when empty
	ProvisioningKey string
	HTTP            *http.Client
}

// Budget is a key's allowance: Limit US dollars of credit, renewed every
// Reset period ("" never).
type Budget struct {
	Limit float64
	Reset string
}

// Key is what OpenRouter says about one key. Usage is lifetime spend;
// UsagePeriod is the spend in the current Reset period (lifetime when
// there is no reset). Remaining is nil when OpenRouter gives none.
type Key struct {
	Hash        string   `json:"hash"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Disabled    bool     `json:"disabled"`
	Limit       *float64 `json:"limit"`
	Remaining   *float64 `json:"limit_remaining"`
	Reset       *string  `json:"limit_reset"`
	Usage       float64  `json:"usage"`
	UsageDaily  float64  `json:"usage_daily"`
	UsageWeekly float64  `json:"usage_weekly"`
	UsageMonth  float64  `json:"usage_monthly"`
}

// UsagePeriod is the spend over the key's own reset period.
func (k Key) UsagePeriod() float64 {
	if k.Reset == nil {
		return k.Usage
	}
	switch *k.Reset {
	case "daily":
		return k.UsageDaily
	case "weekly":
		return k.UsageWeekly
	case "monthly":
		return k.UsageMonth
	}
	return k.Usage
}

// Error is a refusal or failure from OpenRouter: its status and short
// message. NotFound says the key does not exist there.
type Error struct {
	Op      string
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("OpenRouter %s: HTTP %d", e.Op, e.Status)
	}
	return fmt.Sprintf("OpenRouter %s: HTTP %d: %s", e.Op, e.Status, e.Message)
}

// NotFound says OpenRouter answered that the key does not exist.
func (e *Error) NotFound() bool { return e.Status == http.StatusNotFound }

func (c *Client) base() string {
	if c.Base == "" {
		return DefaultBase
	}
	return strings.TrimSuffix(c.Base, "/")
}

func (c *Client) do(ctx context.Context, op, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rd)
	if err != nil {
		return fmt.Errorf("OpenRouter %s: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.ProvisioningKey)
	req.Header.Set("Content-Type", "application/json")
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 20 * time.Second, Transport: keysTransport}
	}
	resp, err := h.Do(req)
	if err != nil {
		// A transport error names the URL, never a header.
		return fmt.Errorf("OpenRouter %s: %w", op, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if len(msg) > 160 {
			msg = msg[:160] + "…"
		}
		return &Error{Op: op, Status: resp.StatusCode, Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("OpenRouter %s: reading the answer: not the expected JSON", op)
	}
	return nil
}

func budgetFields(b Budget) map[string]any {
	m := map[string]any{"limit": b.Limit}
	if b.Reset == "" {
		m["limit_reset"] = nil // explicit: no reset
	} else {
		m["limit_reset"] = b.Reset
	}
	return m
}

// Create makes a key with name and budget. The key's secret value is in
// the answer once, and never again.
func (c *Client) Create(ctx context.Context, name string, b Budget) (Key, string, error) {
	body := budgetFields(b)
	body["name"] = name
	var out struct {
		Data Key    `json:"data"`
		Key  string `json:"key"`
	}
	if err := c.do(ctx, "create key", http.MethodPost, "/keys", body, &out); err != nil {
		return Key{}, "", err
	}
	if out.Data.Hash == "" || out.Key == "" {
		return Key{}, "", fmt.Errorf("OpenRouter create key: the answer has no key")
	}
	return out.Data, out.Key, nil
}

// Get reads one key's state, spend and allowance.
func (c *Client) Get(ctx context.Context, hash string) (Key, error) {
	var out struct {
		Data Key `json:"data"`
	}
	err := c.do(ctx, "read key", http.MethodGet, "/keys/"+hash, nil, &out)
	return out.Data, err
}

// SetBudget sets both the limit and the reset period, "" clearing it.
func (c *Client) SetBudget(ctx context.Context, hash string, b Budget) error {
	return c.do(ctx, "update key", http.MethodPatch, "/keys/"+hash, budgetFields(b), nil)
}

// Delete revokes a key. A key OpenRouter does not have is an *Error with
// NotFound: the caller decides whether that is done.
func (c *Client) Delete(ctx context.Context, hash string) error {
	return c.do(ctx, "delete key", http.MethodDelete, "/keys/"+hash, nil, nil)
}

// List reads the keys the provisioning key manages: the connector's check
// that the provisioning key is accepted.
func (c *Client) List(ctx context.Context) ([]Key, error) {
	var out struct {
		Data []Key `json:"data"`
	}
	err := c.do(ctx, "list keys", http.MethodGet, "/keys", nil, &out)
	return out.Data, err
}
