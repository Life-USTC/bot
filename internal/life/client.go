package life

import (
	"bytes"
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

func (c *Client) Me(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	err := c.getAuth(ctx, "/api/me", nil, token, &out)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		err = c.getAuth(ctx, "/api/auth/oauth2/userinfo", nil, token, &out)
	}
	return out, err
}

func (c *Client) Todos(ctx context.Context, token string, completed string) ([]map[string]any, error) {
	values := url.Values{}
	if completed != "" {
		values.Set("completed", completed)
	}
	var out struct {
		Todos []map[string]any `json:"todos"`
	}
	if err := c.getAuth(ctx, "/api/todos", values, token, &out); err != nil {
		return nil, err
	}
	return out.Todos, nil
}

func (c *Client) CreateTodo(ctx context.Context, token, title string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{"title": title})
	var out map[string]any
	err := c.postAuth(ctx, "/api/todos", token, body, &out)
	return out, err
}

func (c *Client) CompleteTodo(ctx context.Context, token, id string) error {
	body, _ := json.Marshal(map[string]any{"completed": true})
	return c.patchAuth(ctx, "/api/todos/"+url.PathEscape(id), token, body, nil)
}

func (c *Client) SubscribedHomeworks(ctx context.Context, token string) ([]map[string]any, error) {
	var out struct {
		Homeworks []map[string]any `json:"homeworks"`
	}
	if err := c.getAuth(ctx, "/api/me/subscriptions/homeworks", nil, token, &out); err != nil {
		return nil, err
	}
	return out.Homeworks, nil
}

func (c *Client) SetHomeworkCompletion(ctx context.Context, token, id string, completed bool) error {
	body, _ := json.Marshal(map[string]any{"completed": completed})
	return c.putAuth(ctx, "/api/homeworks/"+url.PathEscape(id)+"/completion", token, body, nil)
}

func (c *Client) CurrentSubscription(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	err := c.getAuth(ctx, "/api/calendar-subscriptions/current", nil, token, &out)
	return out, err
}

func (c *Client) Schedules(ctx context.Context, token string, values url.Values) ([]map[string]any, error) {
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.getAuth(ctx, "/api/schedules", values, token, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
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
	return c.do(ctx, http.MethodGet, path, values, "", nil, out)
}

func (c *Client) getAuth(ctx context.Context, path string, values url.Values, token string, out any) error {
	return c.do(ctx, http.MethodGet, path, values, token, nil, out)
}

func (c *Client) postAuth(ctx context.Context, path string, token string, body []byte, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, token, body, out)
}

func (c *Client) patchAuth(ctx context.Context, path string, token string, body []byte, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, token, body, out)
}

func (c *Client) putAuth(ctx context.Context, path string, token string, body []byte, out any) error {
	return c.do(ctx, http.MethodPut, path, nil, token, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, values url.Values, token string, body []byte, out any) error {
	u := c.server + path
	if len(values) > 0 {
		u += "?" + values.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, trimBody(body))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

func (c *Client) Decode(body []byte, path string, out any) error {
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
