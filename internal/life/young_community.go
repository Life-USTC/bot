package life

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// YoungOrganizer is the stable public organizer directory record. Event
// lists are intentionally fetched through ListYoungEventsWithQuery instead of
// being embedded here, so an organizer detail response cannot truncate an
// organizer's event history.
type YoungOrganizer struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	NormalizedName string `json:"normalizedName"`
	TotalCount     int    `json:"totalCount"`
	ActiveCount    int    `json:"activeCount"`
	UpcomingCount  int    `json:"upcomingCount"`
	HistoryCount   int    `json:"historyCount"`
}

type YoungOrganizerPage struct {
	Data       []YoungOrganizer     `json:"data"`
	Pagination YoungEventPagination `json:"pagination"`
}

type YoungEventQuery struct {
	Page        int
	PageSize    int
	Search      string
	Category    string
	Active      *bool
	DateUnknown *bool
	OrganizerID string
	DateFrom    string
	DateTo      string
	TimeBasis   string
}

// YoungEventList returns a public page with the final catalog filters. The
// token is accepted for callers that already have one, but public catalog
// commands pass an empty token and do not require OAuth.
func (c *Client) ListYoungEventsWithQuery(ctx context.Context, token string, query YoungEventQuery) (YoungEventPage, error) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 {
		pageSize = 100
	}
	values := url.Values{}
	values.Set("page", strconv.Itoa(page))
	values.Set("pageSize", strconv.Itoa(pageSize))
	if value := strings.TrimSpace(query.Search); value != "" {
		values.Set("search", value)
	}
	if value := strings.TrimSpace(query.Category); value != "" {
		values.Set("category", value)
	}
	if query.Active != nil {
		values.Set("active", strconv.FormatBool(*query.Active))
	}
	if query.DateUnknown != nil {
		values.Set("dateUnknown", strconv.FormatBool(*query.DateUnknown))
	}
	if value := strings.TrimSpace(query.OrganizerID); value != "" {
		values.Set("organizerId", value)
	}
	if value := strings.TrimSpace(query.DateFrom); value != "" {
		values.Set("dateFrom", value)
	}
	if value := strings.TrimSpace(query.DateTo); value != "" {
		values.Set("dateTo", value)
	}
	if value := strings.TrimSpace(query.TimeBasis); value != "" {
		values.Set("timeBasis", value)
	}
	var out YoungEventPage
	if err := c.getAuth(ctx, "/api/catalog/young-events", values, token, &out); err != nil {
		return YoungEventPage{}, err
	}
	return out, nil
}

// YoungEventCollection is the complete result of a public event query. The
// catalog repeats the unknown-date count and source freshness metadata on
// every page; retaining them here prevents full-pagination callers from
// dropping that context.
type YoungEventCollection struct {
	Data             []YoungEvent
	Pagination       YoungEventPagination
	UnknownDateCount int
	Source           map[string]any
}

// ListAllYoungEventsWithQuery follows every page. Public calendar and
// organizer commands use this method so a page-size cap cannot hide events.
func (c *Client) ListAllYoungEventsWithQuery(ctx context.Context, token string, query YoungEventQuery) ([]YoungEvent, error) {
	result, err := c.ListAllYoungEventsWithQueryMetadata(ctx, token, query)
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

// ListAllYoungEventsWithQueryMetadata is the metadata-preserving form of
// ListAllYoungEventsWithQuery used by calendar and organizer views.
func (c *Client) ListAllYoungEventsWithQueryMetadata(ctx context.Context, token string, query YoungEventQuery) (YoungEventCollection, error) {
	query.Page = 1
	query.PageSize = normalizedPageSize(query.PageSize)
	result := YoungEventCollection{}
	for {
		page, err := c.ListYoungEventsWithQuery(ctx, token, query)
		if err != nil {
			return YoungEventCollection{}, err
		}
		result.Data = append(result.Data, page.Data...)
		if page.UnknownDateCount > result.UnknownDateCount {
			result.UnknownDateCount = page.UnknownDateCount
		}
		if result.Source == nil && page.Source != nil {
			result.Source = page.Source
		}
		if page.Pagination.Total > result.Pagination.Total {
			result.Pagination.Total = page.Pagination.Total
		}
		if page.Pagination.TotalPages > result.Pagination.TotalPages {
			result.Pagination.TotalPages = page.Pagination.TotalPages
		}
		if page.Pagination.Total > 0 {
			expectedPages := (page.Pagination.Total + query.PageSize - 1) / query.PageSize
			if expectedPages > result.Pagination.TotalPages {
				result.Pagination.TotalPages = expectedPages
			}
		}
		if !youngPageHasNext(query.Page, page.Pagination, len(page.Data), query.PageSize) {
			break
		}
		query.Page++
	}
	result.Pagination.Page = 1
	result.Pagination.PageSize = query.PageSize
	if result.Pagination.Total < len(result.Data) {
		result.Pagination.Total = len(result.Data)
	}
	if result.Pagination.TotalPages == 0 && len(result.Data) > 0 {
		result.Pagination.TotalPages = (len(result.Data) + query.PageSize - 1) / query.PageSize
	}
	return result, nil
}

func youngPageHasNext(page int, pagination YoungEventPagination, dataLen, pageSize int) bool {
	if dataLen == 0 {
		return false
	}
	if pagination.TotalPages > 0 {
		return page < pagination.TotalPages
	}
	if pagination.Total > 0 {
		return page*pageSize < pagination.Total
	}
	return dataLen >= pageSize
}

func (c *Client) ListYoungOrganizers(ctx context.Context, page, pageSize int, search string) (YoungOrganizerPage, error) {
	page = normalizedPage(page)
	pageSize = normalizedPageSize(pageSize)
	values := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}}
	if search = strings.TrimSpace(search); search != "" {
		values.Set("search", search)
	}
	var out YoungOrganizerPage
	if err := c.getAuth(ctx, "/api/catalog/young-organizers", values, "", &out); err != nil {
		return YoungOrganizerPage{}, err
	}
	return out, nil
}

func (c *Client) GetYoungOrganizer(ctx context.Context, organizerID string) (YoungOrganizer, error) {
	organizerID = strings.TrimSpace(organizerID)
	if organizerID == "" {
		return YoungOrganizer{}, errors.New("young organizer id is required")
	}
	var out YoungOrganizer
	if err := c.getAuth(ctx, "/api/catalog/young-organizers/"+url.PathEscape(organizerID), nil, "", &out); err != nil {
		return YoungOrganizer{}, err
	}
	return out, nil
}

// YoungOrganizerURL builds the public Life web link for an organizer.
func (c *Client) YoungOrganizerURL(organizerID string) string {
	organizerID = strings.TrimSpace(organizerID)
	if organizerID == "" {
		return ""
	}
	return strings.TrimRight(c.server, "/") + "/catalog/young-events/organizers/" + url.PathEscape(organizerID)
}

type YoungEventSubscription struct {
	YoungID        string      `json:"youngId"`
	Subscribed     bool        `json:"subscribed"`
	RemindSignup   bool        `json:"remindSignup"`
	RemindDeadline bool        `json:"remindDeadline"`
	RemindStart    bool        `json:"remindStart"`
	CreatedAt      string      `json:"createdAt,omitempty"`
	Event          *YoungEvent `json:"event,omitempty"`
}

type YoungEventSubscriptionPage struct {
	Data       []YoungEventSubscription `json:"data"`
	Pagination YoungEventPagination     `json:"pagination"`
}

func (c *Client) ListYoungEventSubscriptions(ctx context.Context, token string, page, pageSize int) (YoungEventSubscriptionPage, error) {
	var out YoungEventSubscriptionPage
	if err := c.getWorkspacePage(ctx, "/api/workspace/young-event-subscriptions", token, page, pageSize, nil, &out); err != nil {
		return YoungEventSubscriptionPage{}, err
	}
	return out, nil
}

func (c *Client) ListAllYoungEventSubscriptions(ctx context.Context, token string) ([]YoungEventSubscription, error) {
	page, pageSize := 1, 100
	var all []YoungEventSubscription
	for {
		result, err := c.ListYoungEventSubscriptions(ctx, token, page, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Data...)
		if !youngPageHasNext(page, result.Pagination, len(result.Data), pageSize) {
			return all, nil
		}
		page++
	}
}

func (c *Client) GetYoungEventSubscription(ctx context.Context, token, youngID string) (YoungEventSubscription, error) {
	return c.youngEventSubscriptionMutation(ctx, token, http.MethodGet, youngID, nil)
}

// SetYoungEventSubscriptionWithOptions leaves reminder fields out when their
// pointers are nil. This lets the server apply its documented defaults for a
// newly enabled subscription while still allowing each reminder to be
// changed independently.
func (c *Client) SetYoungEventSubscriptionWithOptions(ctx context.Context, token, youngID string, subscribed bool, remindSignup, remindDeadline, remindStart *bool) (YoungEventSubscription, error) {
	body := map[string]any{"subscribed": subscribed}
	if remindSignup != nil {
		body["remindSignup"] = *remindSignup
	}
	if remindDeadline != nil {
		body["remindDeadline"] = *remindDeadline
	}
	if remindStart != nil {
		body["remindStart"] = *remindStart
	}
	return c.youngEventSubscriptionMutation(ctx, token, http.MethodPut, youngID, body)
}

func (c *Client) youngEventSubscriptionMutation(ctx context.Context, token, method, youngID string, body any) (YoungEventSubscription, error) {
	youngID = strings.TrimSpace(youngID)
	if youngID == "" {
		return YoungEventSubscription{}, errors.New("young event id is required")
	}
	var out YoungEventSubscription
	encoded, err := marshalJSON(body)
	if err != nil {
		return YoungEventSubscription{}, err
	}
	if err := c.do(ctx, method, "/api/workspace/young-event-subscriptions/"+url.PathEscape(youngID), nil, token, encoded, &out); err != nil {
		return YoungEventSubscription{}, err
	}
	return out, nil
}

type YoungOrganizerSubscription struct {
	OrganizerID string          `json:"organizerId"`
	Subscribed  bool            `json:"subscribed"`
	CreatedAt   string          `json:"createdAt,omitempty"`
	Organizer   *YoungOrganizer `json:"organizer,omitempty"`
}

type YoungOrganizerSubscriptionPage struct {
	Data       []YoungOrganizerSubscription `json:"data"`
	Pagination YoungEventPagination         `json:"pagination"`
}

func (c *Client) ListYoungOrganizerSubscriptions(ctx context.Context, token string, page, pageSize int) (YoungOrganizerSubscriptionPage, error) {
	var out YoungOrganizerSubscriptionPage
	if err := c.getWorkspacePage(ctx, "/api/workspace/young-organizer-subscriptions", token, page, pageSize, nil, &out); err != nil {
		return YoungOrganizerSubscriptionPage{}, err
	}
	return out, nil
}

func (c *Client) ListAllYoungOrganizerSubscriptions(ctx context.Context, token string) ([]YoungOrganizerSubscription, error) {
	page, pageSize := 1, 100
	var all []YoungOrganizerSubscription
	for {
		result, err := c.ListYoungOrganizerSubscriptions(ctx, token, page, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Data...)
		if !youngPageHasNext(page, result.Pagination, len(result.Data), pageSize) {
			return all, nil
		}
		page++
	}
}

func (c *Client) GetYoungOrganizerSubscription(ctx context.Context, token, organizerID string) (YoungOrganizerSubscription, error) {
	return c.youngOrganizerSubscriptionMutation(ctx, token, http.MethodGet, organizerID, nil)
}

func (c *Client) SetYoungOrganizerSubscription(ctx context.Context, token, organizerID string, subscribed bool) (YoungOrganizerSubscription, error) {
	return c.youngOrganizerSubscriptionMutation(ctx, token, http.MethodPut, organizerID, map[string]any{"subscribed": subscribed})
}

func (c *Client) youngOrganizerSubscriptionMutation(ctx context.Context, token, method, organizerID string, body any) (YoungOrganizerSubscription, error) {
	organizerID = strings.TrimSpace(organizerID)
	if organizerID == "" {
		return YoungOrganizerSubscription{}, errors.New("young organizer id is required")
	}
	var out YoungOrganizerSubscription
	encoded, err := marshalJSON(body)
	if err != nil {
		return YoungOrganizerSubscription{}, err
	}
	if err := c.do(ctx, method, "/api/workspace/young-organizer-subscriptions/"+url.PathEscape(organizerID), nil, token, encoded, &out); err != nil {
		return YoungOrganizerSubscription{}, err
	}
	return out, nil
}

type YoungNotification struct {
	ID          string `json:"id"`
	YoungID     string `json:"youngId,omitempty"`
	OrganizerID string `json:"organizerId,omitempty"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	CreatedAt   string `json:"createdAt"`
	ReadAt      string `json:"readAt,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

type YoungNotificationPage struct {
	Data       []YoungNotification  `json:"data"`
	Pagination YoungEventPagination `json:"pagination"`
}

func (c *Client) ListYoungNotifications(ctx context.Context, token string, page, pageSize int, unread *bool) (YoungNotificationPage, error) {
	values := url.Values{}
	if unread != nil {
		values.Set("unread", strconv.FormatBool(*unread))
	}
	var out YoungNotificationPage
	if err := c.getWorkspacePage(ctx, "/api/workspace/young-notifications", token, page, pageSize, values, &out); err != nil {
		return YoungNotificationPage{}, err
	}
	return out, nil
}

func (c *Client) ListAllYoungNotifications(ctx context.Context, token string, unread *bool) ([]YoungNotification, error) {
	page, pageSize := 1, 100
	var all []YoungNotification
	for {
		result, err := c.ListYoungNotifications(ctx, token, page, pageSize, unread)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Data...)
		if !youngPageHasNext(page, result.Pagination, len(result.Data), pageSize) {
			break
		}
		page++
	}
	return all, nil
}

func (c *Client) MarkYoungNotificationRead(ctx context.Context, token, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("young notification id is required")
	}
	return c.do(ctx, http.MethodPost, "/api/workspace/young-notifications/"+url.PathEscape(id)+"/read", nil, token, nil, nil)
}

type PersonalCalendarEvent struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	At       string `json:"at"`
	EndsAt   string `json:"endsAt"`
	Title    string `json:"title"`
	Location string `json:"location"`
	URL      string `json:"url"`
	YoungID  string `json:"youngId,omitempty"`
}

type PersonalCalendarEventPage struct {
	Data       []PersonalCalendarEvent `json:"data"`
	Pagination YoungEventPagination    `json:"pagination"`
}

func (c *Client) ListPersonalCalendarEvents(ctx context.Context, token, dateFrom, dateTo string, page, pageSize int) (PersonalCalendarEventPage, error) {
	values := url.Values{}
	if dateFrom = strings.TrimSpace(dateFrom); dateFrom != "" {
		values.Set("dateFrom", dateFrom)
	}
	if dateTo = strings.TrimSpace(dateTo); dateTo != "" {
		values.Set("dateTo", dateTo)
	}
	var out PersonalCalendarEventPage
	if err := c.getWorkspacePage(ctx, "/api/workspace/calendar/events", token, page, pageSize, values, &out); err != nil {
		return PersonalCalendarEventPage{}, err
	}
	return out, nil
}

func (c *Client) ListAllPersonalCalendarEvents(ctx context.Context, token, dateFrom, dateTo string) ([]PersonalCalendarEvent, error) {
	pageSize := 100
	page := 1
	var all []PersonalCalendarEvent
	for {
		result, err := c.ListPersonalCalendarEvents(ctx, token, dateFrom, dateTo, page, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Data...)
		if !youngPageHasNext(page, result.Pagination, len(result.Data), pageSize) {
			break
		}
		page++
	}
	return all, nil
}

type YoungCommentPage struct {
	Data       []map[string]any     `json:"data"`
	Pagination YoungEventPagination `json:"pagination"`
}

func (c *Client) ListYoungComments(ctx context.Context, token, youngID string, page, pageSize int) (YoungCommentPage, error) {
	youngID = strings.TrimSpace(youngID)
	if youngID == "" {
		return YoungCommentPage{}, errors.New("young event id is required")
	}
	values := url.Values{"targetType": {"young-event"}, "youngId": {youngID}}
	var out YoungCommentPage
	if err := c.getWorkspacePage(ctx, "/api/community/comments", token, page, pageSize, values, &out); err != nil {
		return YoungCommentPage{}, err
	}
	return out, nil
}

func (c *Client) ListAllYoungComments(ctx context.Context, token, youngID string) ([]map[string]any, error) {
	page, pageSize := 1, 100
	var all []map[string]any
	for {
		result, err := c.ListYoungComments(ctx, token, youngID, page, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Data...)
		if !youngPageHasNext(page, result.Pagination, len(result.Data), pageSize) {
			return all, nil
		}
		page++
	}
}

func (c *Client) CreateYoungComment(ctx context.Context, token, youngID, body, parentID, visibility string, anonymous bool) (map[string]any, error) {
	youngID = strings.TrimSpace(youngID)
	body = strings.TrimSpace(body)
	if youngID == "" || body == "" {
		return nil, errors.New("young event id and comment body are required")
	}
	payload := map[string]any{"targetType": "young-event", "youngId": youngID, "body": body, "isAnonymous": anonymous}
	if value := strings.TrimSpace(parentID); value != "" {
		payload["parentId"] = value
	}
	if value := strings.TrimSpace(visibility); value != "" {
		payload["visibility"] = value
	}
	var out map[string]any
	encoded, err := marshalJSON(payload)
	if err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodPost, "/api/community/comments", nil, token, encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) UpdateYoungComment(ctx context.Context, token, id, body string) (map[string]any, error) {
	id, body = strings.TrimSpace(id), strings.TrimSpace(body)
	if id == "" || body == "" {
		return nil, errors.New("comment id and body are required")
	}
	var out map[string]any
	encoded, err := marshalJSON(map[string]any{"body": body})
	if err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodPatch, "/api/community/comments/"+url.PathEscape(id), nil, token, encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DeleteYoungComment(ctx context.Context, token, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("comment id is required")
	}
	return c.do(ctx, http.MethodDelete, "/api/community/comments/"+url.PathEscape(id), nil, token, nil, nil)
}

func (c *Client) ReactYoungComment(ctx context.Context, token, id, reaction string, remove bool) error {
	id, reaction = strings.TrimSpace(id), strings.TrimSpace(reaction)
	if id == "" || reaction == "" {
		return errors.New("comment id and reaction are required")
	}
	path := "/api/community/comments/" + url.PathEscape(id) + "/reactions"
	if remove {
		values := url.Values{"type": {reaction}}
		return c.do(ctx, http.MethodDelete, path, values, token, nil, nil)
	}
	encoded, err := marshalJSON(map[string]any{"type": reaction})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, nil, token, encoded, nil)
}

func (c *Client) getWorkspacePage(ctx context.Context, path, token string, page, pageSize int, values url.Values, out any) error {
	if values == nil {
		values = url.Values{}
	}
	values.Set("page", strconv.Itoa(normalizedPage(page)))
	values.Set("pageSize", strconv.Itoa(normalizedPageSize(pageSize)))
	return c.getAuth(ctx, path, values, token, out)
}

func normalizedPage(page int) int {
	if page < 1 {
		return 1
	}
	return page
}

func normalizedPageSize(pageSize int) int {
	if pageSize < 1 {
		return 100
	}
	if pageSize > 100 {
		return 100
	}
	return pageSize
}

func marshalJSON(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return jsonMarshal(value)
}

// Kept as a tiny variable to make request body encoding easy to replace in
// tests without exposing Client internals.
var jsonMarshal = func(value any) ([]byte, error) {
	return json.Marshal(value)
}
