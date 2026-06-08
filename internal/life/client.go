package life

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/textutil"
)

type Client struct {
	server     string
	httpClient *http.Client
}

type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s returned %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func NewClient(server string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		server:     textutil.TrimTrailingSlash(server),
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
	return c.list(ctx, "/api/courses", searchQuery(search, limit))
}

func (c *Client) SearchSections(ctx context.Context, search string, limit int) ([]map[string]any, error) {
	return c.list(ctx, "/api/sections", searchQuery(search, limit))
}

func searchQuery(search string, limit int) url.Values {
	values := url.Values{}
	search = strings.TrimSpace(search)
	values.Set("search", search)
	setLimit(values, limit)
	return values
}

func (c *Client) Bus(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.get(ctx, "/api/bus", nil, &out)
	return out, err
}

func (c *Client) Me(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	err := c.getAuth(ctx, "/api/me", nil, token, &out)
	if IsUnauthorized(err) {
		err = c.getAuth(ctx, "/api/auth/oauth2/userinfo", nil, token, &out)
	}
	return out, err
}

func IsUnauthorized(err error) bool {
	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusUnauthorized
	}
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, " returned 401:") || strings.HasSuffix(text, " returned 401")
}

func (c *Client) Todos(ctx context.Context, token string, completed string) ([]map[string]any, error) {
	values := url.Values{}
	completed = strings.TrimSpace(completed)
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
	title = strings.TrimSpace(title)
	body, err := jsonBody(map[string]any{"title": title})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.postAuth(ctx, "/api/todos", token, body, &out)
	return out, err
}

func (c *Client) CompleteTodo(ctx context.Context, token, id string) error {
	id = strings.TrimSpace(id)
	body, err := completionBody(true)
	if err != nil {
		return err
	}
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
	id = strings.TrimSpace(id)
	body, err := completionBody(completed)
	if err != nil {
		return err
	}
	return c.putAuth(ctx, "/api/homeworks/"+url.PathEscape(id)+"/completion", token, body, nil)
}

func (c *Client) CurrentSubscription(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	err := c.getAuth(ctx, "/api/calendar-subscriptions/current", nil, token, &out)
	return out, err
}

func (c *Client) MatchSectionCodes(ctx context.Context, token string, codes []string, semesterID string) (map[string]any, error) {
	req := map[string]any{"codes": textutil.NonEmpty(codes...)}
	semesterID = strings.TrimSpace(semesterID)
	if semesterID != "" {
		req["semesterId"] = semesterID
	}
	body, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.postAuth(ctx, "/api/sections/match-codes", token, body, &out)
	return out, err
}

func (c *Client) ReplaceCalendarSubscription(ctx context.Context, token string, sectionIDs []int) (map[string]any, error) {
	body, err := jsonBody(map[string]any{"sectionIds": sectionIDs})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.postAuth(ctx, "/api/calendar-subscriptions", token, body, &out)
	return out, err
}

func (c *Client) Schedules(ctx context.Context, token string, values url.Values) ([]map[string]any, error) {
	var out dataList
	if err := c.getAuth(ctx, "/api/schedules", values, token, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func ScheduleQuery(sectionID, dateFrom, dateTo string) url.Values {
	values := url.Values{}
	values.Set("sectionId", strings.TrimSpace(sectionID))
	values.Set("dateFrom", strings.TrimSpace(dateFrom))
	values.Set("dateTo", strings.TrimSpace(dateTo))
	values.Set("limit", "100")
	return values
}

func (c *Client) list(ctx context.Context, path string, values url.Values) ([]map[string]any, error) {
	var out dataList
	if err := c.get(ctx, path, values, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

type dataList struct {
	Data []map[string]any `json:"data"`
}

func jsonBody(value any) ([]byte, error) {
	return json.Marshal(value)
}

func completionBody(completed bool) ([]byte, error) {
	return jsonBody(map[string]any{"completed": completed})
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
	token = strings.TrimSpace(token)
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
		if resp.StatusCode >= 400 {
			return HTTPError{
				Method:     method,
				Path:       path,
				StatusCode: resp.StatusCode,
				Body:       "read response body: " + err.Error(),
			}
		}
		return err
	}
	if resp.StatusCode >= 400 {
		return HTTPError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       trimBody(body),
		}
	}
	if out != nil && len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
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
	return textutil.TrimBytesRunes(body, 200)
}
