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
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/openapi"
	"github.com/Life-USTC/Bot/internal/textutil"
)

type Client struct {
	server     string
	httpClient *http.Client
}

const userAgent = "life-ustc-bot/1.0"

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

func (c *Client) Typed(ctx context.Context, token string) *openapi.Client {
	token = strings.TrimSpace(token)
	return &openapi.Client{
		Server: c.server,
		Client: c.httpClient,
		RequestEditors: []openapi.RequestEditorFn{
			func(ctx context.Context, req *http.Request) error {
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				req.Header.Set("User-Agent", userAgent)
				return nil
			},
		},
	}
}

func (c *Client) Health(ctx context.Context) error {
	var out map[string]any
	resp, err := c.Typed(ctx, "").GetMetadata(ctx)
	return typedJSON(resp, err, "metadata", &out)
}

func (c *Client) CurrentSemester(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, "").GetCurrentSemester(ctx)
	err = typedJSON(resp, err, "current semester", &out)
	return out, err
}

func (c *Client) SearchCourses(ctx context.Context, search string, limit int) ([]map[string]any, error) {
	params := openapi.ListCoursesParams{}
	setSearchLimit(&params.Search, &params.Limit, search, limit)
	resp, err := c.Typed(ctx, "").ListCourses(ctx, &params)
	return typedDataList(resp, err, "courses")
}

func (c *Client) SearchSections(ctx context.Context, search string, limit int) ([]map[string]any, error) {
	params := openapi.ListSectionsParams{}
	setSearchLimit(&params.Search, &params.Limit, search, limit)
	resp, err := c.Typed(ctx, "").ListSections(ctx, &params)
	return typedDataList(resp, err, "sections")
}

func (c *Client) SearchTeachers(ctx context.Context, search string, limit int) ([]map[string]any, error) {
	params := openapi.ListTeachersParams{}
	setSearchLimit(&params.Search, &params.Limit, search, limit)
	resp, err := c.Typed(ctx, "").ListTeachers(ctx, &params)
	return typedDataList(resp, err, "teachers")
}

type SearchCoursesOptions struct {
	Keyword          string
	EducationLevelID int64
	CategoryID       int64
	ClassTypeID      int64
	Limit            int
}

type SearchSectionsOptions struct {
	Keyword      string
	CourseID     int64
	CourseJwID   int64
	SemesterID   int64
	SemesterJwID int64
	CampusID     int64
	DepartmentID int64
	TeacherID    int64
	TeacherCode  string
	Limit        int
}

type SearchTeachersOptions struct {
	Keyword      string
	DepartmentID int64
	Limit        int
}

func (c *Client) SearchCoursesWithFilters(ctx context.Context, opts SearchCoursesOptions) ([]map[string]any, error) {
	params := openapi.ListCoursesParams{}
	setSearchLimit(&params.Search, &params.Limit, opts.Keyword, opts.Limit)
	if opts.EducationLevelID > 0 {
		params.EducationLevelId = &opts.EducationLevelID
	}
	if opts.CategoryID > 0 {
		params.CategoryId = &opts.CategoryID
	}
	if opts.ClassTypeID > 0 {
		params.ClassTypeId = &opts.ClassTypeID
	}
	resp, err := c.Typed(ctx, "").ListCourses(ctx, &params)
	return typedDataList(resp, err, "courses")
}

func (c *Client) SearchSectionsWithFilters(ctx context.Context, opts SearchSectionsOptions) ([]map[string]any, error) {
	params := openapi.ListSectionsParams{}
	setSearchLimit(&params.Search, &params.Limit, opts.Keyword, opts.Limit)
	if opts.CourseID > 0 {
		params.CourseId = &opts.CourseID
	}
	if opts.CourseJwID > 0 {
		params.CourseJwId = &opts.CourseJwID
	}
	if opts.SemesterID > 0 {
		params.SemesterId = &opts.SemesterID
	}
	if opts.SemesterJwID > 0 {
		params.SemesterJwId = &opts.SemesterJwID
	}
	if opts.CampusID > 0 {
		params.CampusId = &opts.CampusID
	}
	if opts.DepartmentID > 0 {
		params.DepartmentId = &opts.DepartmentID
	}
	if opts.TeacherID > 0 {
		params.TeacherId = &opts.TeacherID
	}
	if opts.TeacherCode != "" {
		params.TeacherCode = &opts.TeacherCode
	}
	resp, err := c.Typed(ctx, "").ListSections(ctx, &params)
	return typedDataList(resp, err, "sections")
}

func (c *Client) SearchTeachersWithFilters(ctx context.Context, opts SearchTeachersOptions) ([]map[string]any, error) {
	params := openapi.ListTeachersParams{}
	setSearchLimit(&params.Search, &params.Limit, opts.Keyword, opts.Limit)
	if opts.DepartmentID > 0 {
		params.DepartmentId = &opts.DepartmentID
	}
	resp, err := c.Typed(ctx, "").ListTeachers(ctx, &params)
	return typedDataList(resp, err, "teachers")
}

func (c *Client) ListSemesters(ctx context.Context, page, limit int) ([]map[string]any, error) {
	params := openapi.ListSemestersParams{}
	if page > 0 {
		params.Page = int64Ptr(int64(page))
	}
	if limit <= 0 {
		limit = 20
	}
	params.Limit = int64Ptr(int64(limit))
	resp, err := c.Typed(ctx, "").ListSemesters(ctx, &params)
	return typedDataList(resp, err, "semesters")
}

func (c *Client) GetCourseByJwID(ctx context.Context, jwId int64) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, "").GetCourse(ctx, jwId)
	err = typedJSON(resp, err, "course", &out)
	return out, err
}

func (c *Client) GetSectionByJwID(ctx context.Context, jwId int64) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, "").GetSection(ctx, jwId)
	err = typedJSON(resp, err, "section", &out)
	return out, err
}

func (c *Client) GetTeacherByID(ctx context.Context, id int64) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, "").GetTeacher(ctx, id)
	err = typedJSON(resp, err, "teacher", &out)
	return out, err
}

func (c *Client) ListBusRoutes(ctx context.Context, originCampusID, destinationCampusID int64) (map[string]any, error) {
	params := openapi.GetApiBusRoutesParams{}
	if originCampusID > 0 {
		params.OriginCampusId = &originCampusID
	}
	if destinationCampusID > 0 {
		params.DestinationCampusId = &destinationCampusID
	}
	var out map[string]any
	resp, err := c.Typed(ctx, "").GetApiBusRoutes(ctx, &params)
	err = typedJSON(resp, err, "bus routes", &out)
	return out, err
}

func (c *Client) UnsubscribeSectionByJwID(ctx context.Context, token string, jwId int64) (map[string]any, error) {
	sub, err := c.CurrentSubscription(ctx, token)
	if err != nil {
		return nil, err
	}
	sections := lifedata.SubscriptionSections(sub)
	sectionID := 0
	for _, section := range sections {
		if lifedata.FirstInt(section, "jwId") == int(jwId) {
			sectionID = lifedata.FirstInt(section, "id")
			break
		}
	}
	if sectionID == 0 {
		return nil, fmt.Errorf("section jwId %d is not in current subscription", jwId)
	}
	var out map[string]any
	resp, err := c.Typed(ctx, token).BatchUpdateCalendarSubscription(ctx, openapi.BatchUpdateCalendarSubscriptionJSONRequestBody{
		Action:     openapi.CalendarSubscriptionBatchRequestSchemaActionRemove,
		SectionIds: &[]int{sectionID},
	})
	err = typedJSON(resp, err, "unsubscribe section", &out)
	return out, err
}

func (c *Client) ListSubscribedSections(ctx context.Context, token string) ([]map[string]any, error) {
	sub, err := c.CurrentSubscription(ctx, token)
	if err != nil {
		return nil, err
	}
	return lifedata.SubscriptionSections(sub), nil
}

func (c *Client) ListSchedulesBySection(ctx context.Context, token string, sectionJwId int64, dateFrom, dateTo string) ([]map[string]any, error) {
	limit := int64(100)
	params := openapi.GetSectionSchedulesParams{
		DateFrom: &dateFrom,
		DateTo:   &dateTo,
		Limit:    &limit,
	}
	var out []map[string]any
	resp, err := c.Typed(ctx, token).GetSectionSchedules(ctx, sectionJwId, &params)
	if err := typedJSON(resp, err, "section schedules", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListExamsBySection(ctx context.Context, token string, sectionJwId int64) ([]map[string]any, error) {
	section, err := c.GetSectionByJwID(ctx, sectionJwId)
	if err != nil {
		return nil, err
	}
	return lifedata.MapSlice(section["exams"]), nil
}

func (c *Client) ListHomeworksBySection(ctx context.Context, token string, sectionJwId int64) ([]map[string]any, error) {
	params := openapi.ListHomeworksParams{SectionJwId: &sectionJwId}
	var out struct {
		Homeworks []map[string]any `json:"homeworks"`
	}
	resp, err := c.Typed(ctx, token).ListHomeworks(ctx, &params)
	if err := typedJSON(resp, err, "section homeworks", &out); err != nil {
		return nil, err
	}
	return out.Homeworks, nil
}

func (c *Client) GetMyDashboard(ctx context.Context, token string) (map[string]any, error) {
	params := openapi.GetApiMeOverviewParams{}
	var out map[string]any
	resp, err := c.Typed(ctx, token).GetApiMeOverview(ctx, &params)
	err = typedJSON(resp, err, "dashboard", &out)
	return out, err
}

func (c *Client) GetUpcomingDeadlines(ctx context.Context, token string, dayLimit int) (map[string]any, error) {
	params := openapi.GetApiMeOverviewParams{}
	if dayLimit > 0 {
		params.HomeworkWindowDays = int64Ptr(int64(dayLimit))
	}
	params.Limit = int64Ptr(int64(50))
	var out map[string]any
	resp, err := c.Typed(ctx, token).GetApiMeOverview(ctx, &params)
	err = typedJSON(resp, err, "upcoming deadlines", &out)
	return out, err
}

func int64Ptr(v int64) *int64 {
	return &v
}

func (c *Client) Bus(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, "").QueryBus(ctx, &openapi.QueryBusParams{})
	err = typedJSON(resp, err, "bus", &out)
	return out, err
}

type BusPreferences struct {
	PreferredOriginCampusID      *int `json:"preferredOriginCampusId"`
	PreferredDestinationCampusID *int `json:"preferredDestinationCampusId"`
	ShowDepartedTrips            bool `json:"showDepartedTrips"`
}

func (c *Client) BusPreferences(ctx context.Context, token string) (BusPreferences, error) {
	var out struct {
		Preference BusPreferences `json:"preference"`
	}
	resp, err := c.Typed(ctx, token).GetBusPreferences(ctx)
	err = typedJSON(resp, err, "bus preferences", &out)
	return out.Preference, err
}

func (c *Client) SetBusPreferences(ctx context.Context, token string, preferences BusPreferences) (BusPreferences, error) {
	var out struct {
		Preference BusPreferences `json:"preference"`
	}
	body := openapi.SetBusPreferencesJSONRequestBody{
		PreferredDestinationCampusId: preferences.PreferredDestinationCampusID,
		PreferredOriginCampusId:      preferences.PreferredOriginCampusID,
		ShowDepartedTrips:            preferences.ShowDepartedTrips,
	}
	resp, err := c.Typed(ctx, token).SetBusPreferences(ctx, body)
	err = typedJSON(resp, err, "set bus preferences", &out)
	return out.Preference, err
}

func (c *Client) Me(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, token).GetMe(ctx)
	err = typedJSON(resp, err, "me", &out)
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

type TodoListOptions struct {
	Completed string
	Priority  string
	DueBefore string
	DueAfter  string
}

type TodoCreateOptions struct {
	Title    string
	Content  string
	Priority string
	DueAt    string
}

type TodoUpdateOptions struct {
	Title     string
	Content   string
	Priority  string
	DueAt     string
	Completed *bool
}

func (c *Client) Todos(ctx context.Context, token string, completed string) ([]map[string]any, error) {
	return c.TodosWithOptions(ctx, token, TodoListOptions{Completed: completed})
}

func (c *Client) TodosWithOptions(ctx context.Context, token string, opts TodoListOptions) ([]map[string]any, error) {
	params := openapi.ListTodosParams{}
	if completed := strings.TrimSpace(opts.Completed); completed != "" {
		value := openapi.ListTodosParamsCompleted(completed)
		params.Completed = &value
	}
	if priority := strings.TrimSpace(opts.Priority); priority != "" {
		value := openapi.ListTodosParamsPriority(priority)
		params.Priority = &value
	}
	if dueBefore := strings.TrimSpace(opts.DueBefore); dueBefore != "" {
		params.DueBefore = &dueBefore
	}
	if dueAfter := strings.TrimSpace(opts.DueAfter); dueAfter != "" {
		params.DueAfter = &dueAfter
	}
	var out struct {
		Todos []map[string]any `json:"todos"`
	}
	resp, err := c.Typed(ctx, token).ListTodos(ctx, &params)
	if err := typedJSON(resp, err, "todos", &out); err != nil {
		return nil, err
	}
	return out.Todos, nil
}

func (c *Client) CreateTodo(ctx context.Context, token, title string) (map[string]any, error) {
	return c.CreateTodoWithOptions(ctx, token, TodoCreateOptions{Title: title})
}

func (c *Client) CreateTodoWithOptions(ctx context.Context, token string, opts TodoCreateOptions) (map[string]any, error) {
	opts.Title = strings.TrimSpace(opts.Title)
	if opts.Title == "" {
		return nil, errors.New("todo title is required")
	}
	body := openapi.CreateTodoJSONRequestBody{Title: opts.Title}
	if content := strings.TrimSpace(opts.Content); content != "" {
		body.Content = &content
	}
	if priority := strings.TrimSpace(opts.Priority); priority != "" {
		value := openapi.TodoCreateRequestSchemaPriority(priority)
		body.Priority = &value
	}
	if dueAt := strings.TrimSpace(opts.DueAt); dueAt != "" {
		value := openapi.TodoCreateRequestSchema_DueAt{}
		_ = value.FromTodoCreateRequestSchemaDueAt0(dueAt)
		body.DueAt = &value
	}
	var out map[string]any
	resp, err := c.Typed(ctx, token).CreateTodo(ctx, body)
	err = typedJSON(resp, err, "create todo", &out)
	return out, err
}

func (c *Client) CompleteTodo(ctx context.Context, token, id string) error {
	return c.SetTodoCompleted(ctx, token, id, true)
}

func (c *Client) SetTodoCompleted(ctx context.Context, token, id string, completed bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("todo id is required")
	}
	return typedResponse(c.Typed(ctx, token).UpdateTodo(ctx, id, openapi.UpdateTodoJSONRequestBody{Completed: &completed}))
}

func (c *Client) UpdateTodo(ctx context.Context, token, id string, opts TodoUpdateOptions) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("todo id is required")
	}
	body := openapi.UpdateTodoJSONRequestBody{}
	changed := false
	if title := strings.TrimSpace(opts.Title); title != "" {
		body.Title = &title
		changed = true
	}
	if content := strings.TrimSpace(opts.Content); content != "" {
		body.Content = &content
		changed = true
	}
	if priority := strings.TrimSpace(opts.Priority); priority != "" {
		value := openapi.TodoUpdateRequestSchemaPriority(priority)
		body.Priority = &value
		changed = true
	}
	if dueAt := strings.TrimSpace(opts.DueAt); dueAt != "" {
		value := openapi.TodoUpdateRequestSchema_DueAt{}
		_ = value.FromTodoUpdateRequestSchemaDueAt0(dueAt)
		body.DueAt = &value
		changed = true
	}
	if opts.Completed != nil {
		body.Completed = opts.Completed
		changed = true
	}
	if !changed {
		return errors.New("todo update requires at least one change")
	}
	return typedResponse(c.Typed(ctx, token).UpdateTodo(ctx, id, body))
}

func (c *Client) DeleteTodo(ctx context.Context, token, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("todo id is required")
	}
	return typedResponse(c.Typed(ctx, token).DeleteTodo(ctx, id))
}

func (c *Client) SubscribedHomeworks(ctx context.Context, token string) ([]map[string]any, error) {
	var out struct {
		Homeworks []map[string]any `json:"homeworks"`
	}
	resp, err := c.Typed(ctx, token).GetSubscribedHomeworks(ctx)
	if err := typedJSON(resp, err, "subscribed homeworks", &out); err != nil {
		return nil, err
	}
	return out.Homeworks, nil
}

func (c *Client) SetHomeworkCompletion(ctx context.Context, token, id string, completed bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("homework id is required")
	}
	return typedResponse(c.Typed(ctx, token).SetHomeworkCompletion(ctx, id, openapi.SetHomeworkCompletionJSONRequestBody{Completed: completed}))
}

type TodoCompletionItem struct {
	TodoID    string
	Completed bool
}

func (c *Client) SetTodoCompletions(ctx context.Context, token string, items []TodoCompletionItem) error {
	body := openapi.TodoCompletionBatchRequestSchema{}
	for _, item := range items {
		body.Items = append(body.Items, struct {
			Completed bool   `json:"completed"`
			TodoId    string `json:"todoId"`
		}{Completed: item.Completed, TodoId: item.TodoID})
	}
	resp, err := c.Typed(ctx, token).PatchApiTodosBatch(ctx, body)
	return typedResponse(resp, err)
}

type HomeworkCompletionItem struct {
	HomeworkID string
	Completed  bool
}

func (c *Client) SetHomeworkCompletions(ctx context.Context, token string, items []HomeworkCompletionItem) error {
	body := openapi.HomeworkCompletionBatchRequestSchema{}
	for _, item := range items {
		body.Items = append(body.Items, struct {
			Completed  bool   `json:"completed"`
			HomeworkId string `json:"homeworkId"`
		}{Completed: item.Completed, HomeworkId: item.HomeworkID})
	}
	resp, err := c.Typed(ctx, token).PutApiHomeworksCompletions(ctx, body)
	return typedResponse(resp, err)
}

func (c *Client) BulkSubscribeSections(ctx context.Context, token string, importCodes []string) (map[string]any, error) {
	var out map[string]any
	codes := textutil.NonEmpty(importCodes...)
	if len(codes) == 0 {
		return nil, errors.New("section or course code is required")
	}
	resp, err := c.Typed(ctx, token).BatchUpdateCalendarSubscription(ctx, openapi.BatchUpdateCalendarSubscriptionJSONRequestBody{
		Action: openapi.CalendarSubscriptionBatchRequestSchemaActionAdd,
		Codes:  &codes,
	})
	err = typedJSON(resp, err, "bulk subscribe sections", &out)
	if err == nil && out != nil {
		if _, ok := out["alreadySubscribedCount"]; !ok {
			out["alreadySubscribedCount"] = out["unchangedCount"]
		}
	}
	return out, err
}

func (c *Client) CurrentSubscription(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, token).GetCurrentCalendarSubscription(ctx)
	err = typedJSON(resp, err, "current subscription", &out)
	return out, err
}

func (c *Client) MatchSectionCodes(ctx context.Context, token string, codes []string, semesterID string) (map[string]any, error) {
	codes = textutil.NonEmpty(codes...)
	if len(codes) == 0 {
		return nil, errors.New("section code is required")
	}
	body := openapi.MatchSectionCodesJSONRequestBody{Codes: codes}
	semesterID = strings.TrimSpace(semesterID)
	if semesterID != "" {
		semester := openapi.MatchSectionCodesRequestSchema_SemesterId{}
		_ = semester.FromMatchSectionCodesRequestSchemaSemesterId0(semesterID)
		body.SemesterId = &semester
	}
	var out map[string]any
	resp, err := c.Typed(ctx, token).MatchSectionCodes(ctx, body)
	err = typedJSON(resp, err, "match section codes", &out)
	return out, err
}

func (c *Client) ReplaceCalendarSubscription(ctx context.Context, token string, sectionIDs []int) (map[string]any, error) {
	var out map[string]any
	resp, err := c.Typed(ctx, token).BatchUpdateCalendarSubscription(ctx, openapi.BatchUpdateCalendarSubscriptionJSONRequestBody{
		Action:     openapi.CalendarSubscriptionBatchRequestSchemaActionSet,
		SectionIds: &sectionIDs,
	})
	err = typedJSON(resp, err, "replace calendar subscription", &out)
	return out, err
}

func (c *Client) Schedules(ctx context.Context, token string, values url.Values) ([]map[string]any, error) {
	var out dataList
	resp, err := c.Typed(ctx, token).ListSchedules(ctx, listSchedulesParams(values))
	if err := typedJSON(resp, err, "schedules", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) SubscribedSchedules(ctx context.Context, token string, values url.Values) ([]map[string]any, error) {
	var out struct {
		Schedules []map[string]any `json:"schedules"`
	}
	resp, err := c.Typed(ctx, token).GetApiMeSubscriptionsSchedules(ctx, subscribedSchedulesParams(values))
	if err := typedJSON(resp, err, "subscribed schedules", &out); err != nil {
		return nil, err
	}
	return out.Schedules, nil
}

func ScheduleQuery(sectionID, dateFrom, dateTo string) url.Values {
	values := url.Values{}
	values.Set("sectionId", strings.TrimSpace(sectionID))
	values.Set("dateFrom", strings.TrimSpace(dateFrom))
	values.Set("dateTo", strings.TrimSpace(dateTo))
	values.Set("limit", "100")
	return values
}

func SubscribedScheduleQuery(dateFrom, dateTo string) url.Values {
	values := url.Values{}
	values.Set("dateFrom", strings.TrimSpace(dateFrom))
	values.Set("dateTo", strings.TrimSpace(dateTo))
	values.Set("limit", "300")
	return values
}

type dataList struct {
	Data []map[string]any `json:"data"`
}

func typedResponse(resp *http.Response, err error) error {
	_, err = typedResponseBytes(resp, err)
	return err
}

func typedJSON(resp *http.Response, err error, label string, out any) error {
	body, err := typedResponseBytes(resp, err)
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w", label, err)
		}
	}
	return nil
}

func typedDataList(resp *http.Response, err error, label string) ([]map[string]any, error) {
	var out dataList
	if err := typedJSON(resp, err, label, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func typedResponseBytes(resp *http.Response, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if resp.StatusCode >= 400 {
			method, path := responseMethodPath(resp)
			return nil, HTTPError{
				Method:     method,
				Path:       path,
				StatusCode: resp.StatusCode,
				Body:       "read response body: " + err.Error(),
			}
		}
		return nil, err
	}
	if resp.StatusCode >= 400 {
		method, path := responseMethodPath(resp)
		return nil, HTTPError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       trimBody(body),
		}
	}
	return body, nil
}

func responseMethodPath(resp *http.Response) (string, string) {
	if resp != nil && resp.Request != nil {
		path := ""
		if resp.Request.URL != nil {
			path = resp.Request.URL.Path
		}
		return resp.Request.Method, path
	}
	return "", ""
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
	req.Header.Set("User-Agent", userAgent)
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

func setSearchLimit(searchTarget **string, limitTarget **int64, search string, limit int) {
	search = strings.TrimSpace(search)
	*searchTarget = &search
	if limit <= 0 {
		limit = 5
	}
	value := int64(limit)
	*limitTarget = &value
}

func listSchedulesParams(values url.Values) *openapi.ListSchedulesParams {
	params := &openapi.ListSchedulesParams{}
	if value := strings.TrimSpace(values.Get("sectionId")); value != "" {
		params.SectionId = int64PtrFromString(value)
	}
	if value := strings.TrimSpace(values.Get("dateFrom")); value != "" {
		params.DateFrom = &value
	}
	if value := strings.TrimSpace(values.Get("dateTo")); value != "" {
		params.DateTo = &value
	}
	if value := strings.TrimSpace(values.Get("limit")); value != "" {
		params.Limit = int64PtrFromString(value)
	}
	return params
}

func subscribedSchedulesParams(values url.Values) *openapi.GetApiMeSubscriptionsSchedulesParams {
	params := &openapi.GetApiMeSubscriptionsSchedulesParams{}
	if value := strings.TrimSpace(values.Get("dateFrom")); value != "" {
		params.DateFrom = &value
	}
	if value := strings.TrimSpace(values.Get("dateTo")); value != "" {
		params.DateTo = &value
	}
	if value := strings.TrimSpace(values.Get("limit")); value != "" {
		params.Limit = int64PtrFromString(value)
	}
	return params
}

func int64PtrFromString(value string) *int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func trimBody(body []byte) string {
	return textutil.TrimBytesRunes(body, 200)
}
