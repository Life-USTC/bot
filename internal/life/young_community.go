package life

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/openapi"
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
	params := openapi.GetApiCatalogYoungEventsParams{
		Page: int64Ptr(int64(page)), PageSize: int64Ptr(int64(pageSize)),
		Search: stringPtr(strings.TrimSpace(query.Search)), Category: stringPtr(strings.TrimSpace(query.Category)),
		OrganizerId: stringPtr(strings.TrimSpace(query.OrganizerID)), DateFrom: stringPtr(strings.TrimSpace(query.DateFrom)), DateTo: stringPtr(strings.TrimSpace(query.DateTo)),
	}
	if query.Active != nil {
		value := openapi.GetApiCatalogYoungEventsParamsActive(strconv.FormatBool(*query.Active))
		params.Active = &value
	}
	if query.DateUnknown != nil {
		value := openapi.GetApiCatalogYoungEventsParamsDateUnknown(strconv.FormatBool(*query.DateUnknown))
		params.DateUnknown = &value
	}
	if value := strings.TrimSpace(query.TimeBasis); value != "" {
		basis := openapi.GetApiCatalogYoungEventsParamsTimeBasis(value)
		params.TimeBasis = &basis
	}
	var out YoungEventPage
	resp, err := c.Typed(ctx, token).GetApiCatalogYoungEvents(ctx, &params)
	err = typedJSON[openapi.PaginatedYoungEventResponseSchema](resp, err, "GetApiCatalogYoungEvents", &out)
	if err != nil {
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
		if page.Meta.UnknownDateCount > result.UnknownDateCount {
			result.UnknownDateCount = page.Meta.UnknownDateCount
		}
		if result.Source == nil && page.Meta.Source != nil {
			result.Source = page.Meta.Source
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

	var out YoungOrganizerPage
	resp, err := c.Typed(ctx, "").GetApiCatalogYoungOrganizers(ctx, &openapi.GetApiCatalogYoungOrganizersParams{Page: int64Ptr(int64(normalizedPage(page))), PageSize: int64Ptr(int64(normalizedPageSize(pageSize))), Search: stringPtr(strings.TrimSpace(search))})
	err = typedJSON[openapi.PaginatedYoungOrganizerResponseSchema](resp, err, "GetApiCatalogYoungOrganizers", &out)
	return out, err
}

func (c *Client) GetYoungOrganizer(ctx context.Context, organizerID string) (YoungOrganizer, error) {
	organizerID = strings.TrimSpace(organizerID)
	if organizerID == "" {
		return YoungOrganizer{}, errors.New("young organizer id is required")
	}
	var out YoungOrganizer
	resp, err := c.Typed(ctx, "").GetApiCatalogYoungOrganizersOrganizerId(ctx, organizerID)
	err = typedJSON[openapi.YoungOrganizerSummarySchema](resp, err, "GetApiCatalogYoungOrganizersOrganizerId", &out)
	return out, err
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
	resp, err := c.Typed(ctx, token).GetApiWorkspaceYoungEventSubscriptions(ctx, &openapi.GetApiWorkspaceYoungEventSubscriptionsParams{Page: int64Ptr(int64(normalizedPage(page))), PageSize: int64Ptr(int64(normalizedPageSize(pageSize)))})
	err = typedJSON[openapi.YoungEventSubscriptionListSchema](resp, err, "GetApiWorkspaceYoungEventSubscriptions", &out)
	return out, err
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
	youngID = strings.TrimSpace(youngID)
	if youngID == "" {
		return YoungEventSubscription{}, errors.New("young event id is required")
	}
	var out YoungEventSubscription
	resp, err := c.Typed(ctx, token).GetApiWorkspaceYoungEventSubscriptionsYoungId(ctx, youngID)
	err = typedJSON[openapi.YoungEventSubscriptionStateSchema](resp, err, "GetApiWorkspaceYoungEventSubscriptionsYoungId", &out)
	return out, err
}

// SetYoungEventSubscriptionWithOptions leaves reminder fields out when their
// pointers are nil. This lets the server apply its documented defaults for a
// newly enabled subscription while still allowing each reminder to be
// changed independently.
func (c *Client) SetYoungEventSubscriptionWithOptions(ctx context.Context, token, youngID string, subscribed bool, remindSignup, remindDeadline, remindStart *bool) (YoungEventSubscription, error) {
	youngID = strings.TrimSpace(youngID)
	if youngID == "" {
		return YoungEventSubscription{}, errors.New("young event id is required")
	}
	var out YoungEventSubscription
	resp, err := c.Typed(ctx, token).PutApiWorkspaceYoungEventSubscriptionsYoungId(ctx, youngID, openapi.PutApiWorkspaceYoungEventSubscriptionsYoungIdJSONRequestBody{Subscribed: subscribed, RemindSignup: remindSignup, RemindDeadline: remindDeadline, RemindStart: remindStart})
	err = typedJSON[openapi.YoungEventSubscriptionStateSchema](resp, err, "PutApiWorkspaceYoungEventSubscriptionsYoungId", &out)
	return out, err
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
	resp, err := c.Typed(ctx, token).GetApiWorkspaceYoungOrganizerSubscriptions(ctx, &openapi.GetApiWorkspaceYoungOrganizerSubscriptionsParams{Page: int64Ptr(int64(normalizedPage(page))), PageSize: int64Ptr(int64(normalizedPageSize(pageSize)))})
	err = typedJSON[openapi.YoungOrganizerSubscriptionListSchema](resp, err, "GetApiWorkspaceYoungOrganizerSubscriptions", &out)
	return out, err
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
	organizerID = strings.TrimSpace(organizerID)
	if organizerID == "" {
		return YoungOrganizerSubscription{}, errors.New("young organizer id is required")
	}
	var out YoungOrganizerSubscription
	resp, err := c.Typed(ctx, token).GetApiWorkspaceYoungOrganizerSubscriptionsOrganizerId(ctx, organizerID)
	err = typedJSON[openapi.YoungOrganizerSubscriptionStateSchema](resp, err, "GetApiWorkspaceYoungOrganizerSubscriptionsOrganizerId", &out)
	return out, err
}

func (c *Client) SetYoungOrganizerSubscription(ctx context.Context, token, organizerID string, subscribed bool) (YoungOrganizerSubscription, error) {
	organizerID = strings.TrimSpace(organizerID)
	if organizerID == "" {
		return YoungOrganizerSubscription{}, errors.New("young organizer id is required")
	}
	var out YoungOrganizerSubscription
	resp, err := c.Typed(ctx, token).PutApiWorkspaceYoungOrganizerSubscriptionsOrganizerId(ctx, organizerID, openapi.PutApiWorkspaceYoungOrganizerSubscriptionsOrganizerIdJSONRequestBody{Subscribed: subscribed})
	err = typedJSON[openapi.YoungOrganizerSubscriptionStateSchema](resp, err, "PutApiWorkspaceYoungOrganizerSubscriptionsOrganizerId", &out)
	return out, err
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
	params := openapi.GetApiWorkspaceYoungNotificationsParams{Page: int64Ptr(int64(normalizedPage(page))), PageSize: int64Ptr(int64(normalizedPageSize(pageSize)))}
	if unread != nil {
		value := openapi.GetApiWorkspaceYoungNotificationsParamsUnread(strconv.FormatBool(*unread))
		params.Unread = &value
	}
	var out YoungNotificationPage
	resp, err := c.Typed(ctx, token).GetApiWorkspaceYoungNotifications(ctx, &params)
	err = typedJSON[openapi.YoungNotificationListSchema](resp, err, "GetApiWorkspaceYoungNotifications", &out)
	return out, err
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
	return typedResponse[openapi.YoungNotificationReadSchema](c.Typed(ctx, token).PostApiWorkspaceYoungNotificationsIdRead(ctx, id))
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

	var out PersonalCalendarEventPage
	resp, err := c.Typed(ctx, token).GetApiWorkspaceCalendarEvents(ctx, &openapi.GetApiWorkspaceCalendarEventsParams{Page: int64Ptr(int64(normalizedPage(page))), PageSize: int64Ptr(int64(normalizedPageSize(pageSize))), DateFrom: stringPtr(strings.TrimSpace(dateFrom)), DateTo: stringPtr(strings.TrimSpace(dateTo))})
	err = typedJSON[openapi.PersonalCalendarPageSchema](resp, err, "GetApiWorkspaceCalendarEvents", &out)
	return out, err
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
	var out YoungCommentPage
	resp, err := c.Typed(ctx, token).ListComments(ctx, &openapi.ListCommentsParams{TargetType: "young-event", YoungId: &youngID, Page: int64Ptr(int64(normalizedPage(page))), PageSize: int64Ptr(int64(normalizedPageSize(pageSize)))})
	err = typedJSON[openapi.CommentsListResponseSchema](resp, err, "ListComments", &out)
	return out, err
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
			break
		}
		page++
	}
	for _, root := range all {
		cursor := lifedata.FirstString(root, "repliesNextCursor")
		seen := map[string]bool{}
		for cursor != "" {
			if seen[cursor] {
				return nil, errors.New("repeated comment reply cursor")
			}
			seen[cursor] = true
			var page struct {
				Thread     []map[string]any `json:"thread"`
				NextCursor string           `json:"nextCursor"`
			}
			id := lifedata.FirstString(root, "id")
			resp, err := c.Typed(ctx, token).GetApiCommunityCommentsIdReplies(ctx, id, &openapi.GetApiCommunityCommentsIdRepliesParams{Cursor: &cursor, PageSize: int64Ptr(20)})
			err = typedJSON[openapi.CommentRepliesResponseSchema](resp, err, "GetApiCommunityCommentsIdReplies", &page)
			if err != nil {
				return nil, err
			}
			for _, node := range page.Thread {
				mergeYoungCommentNode(root, node)
			}
			cursor = page.NextCursor
		}
	}
	return all, nil
}

func mergeYoungCommentNode(target, source map[string]any) {
	if lifedata.FirstString(target, "id") != lifedata.FirstString(source, "id") {
		return
	}
	children := lifedata.MapSlice(target["replies"])
	for _, incoming := range lifedata.MapSlice(source["replies"]) {
		found := false
		for _, child := range children {
			if lifedata.FirstString(child, "id") == lifedata.FirstString(incoming, "id") {
				mergeYoungCommentNode(child, incoming)
				found = true
				break
			}
		}
		if !found {
			children = append(children, incoming)
		}
	}
	for key, value := range source {
		if key != "replies" {
			target[key] = value
		}
	}
	target["replies"] = children
}

func (c *Client) CreateYoungComment(ctx context.Context, token, youngID, body, parentID, visibility string, anonymous bool) (map[string]any, error) {
	youngID, body = strings.TrimSpace(youngID), strings.TrimSpace(body)
	if youngID == "" || body == "" {
		return nil, errors.New("young event id and comment body are required")
	}
	payload := openapi.CreateCommentJSONRequestBody{TargetType: "young-event", YoungId: &youngID, Body: body, IsAnonymous: &anonymous, ParentId: stringPtr(strings.TrimSpace(parentID))}
	if value := strings.TrimSpace(visibility); value != "" {
		converted := openapi.CommentCreateRequestSchemaVisibility(value)
		payload.Visibility = &converted
	}
	var out map[string]any
	resp, err := c.Typed(ctx, token).CreateComment(ctx, payload)
	err = typedJSON[openapi.IdResponseSchema](resp, err, "CreateComment", &out)
	return out, err
}

func (c *Client) UpdateYoungComment(ctx context.Context, token, id, body string) (map[string]any, error) {
	id, body = strings.TrimSpace(id), strings.TrimSpace(body)
	if id == "" || body == "" {
		return nil, errors.New("comment id and body are required")
	}
	var out map[string]any
	resp, err := c.Typed(ctx, token).UpdateComment(ctx, id, openapi.UpdateCommentJSONRequestBody{Body: body})
	err = typedJSON[openapi.CommentUpdateResponseSchema](resp, err, "UpdateComment", &out)
	return out, err
}

func (c *Client) DeleteYoungComment(ctx context.Context, token, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("comment id is required")
	}
	return typedResponse[openapi.SuccessResponseSchema](c.Typed(ctx, token).DeleteComment(ctx, id))
}

func (c *Client) ReactYoungComment(ctx context.Context, token, id, reaction string, remove bool) error {
	id, reaction = strings.TrimSpace(id), strings.TrimSpace(reaction)
	if id == "" || reaction == "" {
		return errors.New("comment id and reaction are required")
	}
	if remove {
		return typedResponse[openapi.SuccessResponseSchema](c.Typed(ctx, token).RemoveCommentReaction(ctx, id, &openapi.RemoveCommentReactionParams{Type: openapi.RemoveCommentReactionParamsType(reaction)}))
	}
	return typedResponse[openapi.SuccessResponseSchema](c.Typed(ctx, token).AddCommentReaction(ctx, id, openapi.AddCommentReactionJSONRequestBody{Type: openapi.CommentReactionRequestSchemaType(reaction)}))
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
