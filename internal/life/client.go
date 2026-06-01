package life

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	server     string
	httpClient *http.Client
}

func NewClient(server string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		server:     strings.TrimRight(server, "/"),
		httpClient: httpClient,
	}
}

func (c *Client) Health(ctx context.Context) error {
	var out map[string]any
	return c.get(ctx, "/api/metadata", nil, &out)
}

func (c *Client) CurrentSemester(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.get(ctx, "/api/semesters/current", nil, &out)
	return out, err
}

func (c *Client) SearchCourses(ctx context.Context, search string, limit int) ([]map[string]any, error) {
	values := url.Values{}
	values.Set("search", search)
	setLimit(values, limit)
	return c.list(ctx, "/api/courses", values)
}

func (c *Client) SearchSections(ctx context.Context, search string, limit int) ([]map[string]any, error) {
	values := url.Values{}
	values.Set("search", search)
	setLimit(values, limit)
	return c.list(ctx, "/api/sections", values)
}

func (c *Client) Bus(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.get(ctx, "/api/bus", nil, &out)
	return out, err
}

func (c *Client) list(ctx context.Context, path string, values url.Values) ([]map[string]any, error) {
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.get(ctx, path, values, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) get(ctx context.Context, path string, values url.Values, out any) error {
	u := c.server + path
	if len(values) > 0 {
		u += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GET %s returned %d: %s", path, resp.StatusCode, trimBody(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func setLimit(values url.Values, limit int) {
	if limit <= 0 {
		limit = 5
	}
	values.Set("limit", fmt.Sprint(limit))
}

func trimBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		return text[:200]
	}
	return text
}
