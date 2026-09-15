package life

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/openapi"
)

// YoungEvent is the public second-classroom event contract returned by Life.
// The date fields are instants; callers should render them in the user's
// intended local timezone.
type YoungEvent struct {
	ApplyEndAt         *time.Time `json:"applyEndAt"`
	ApplyStartAt       *time.Time `json:"applyStartAt"`
	Category           string     `json:"category,omitempty"`
	Department         string     `json:"department,omitempty"`
	Description        string     `json:"description,omitempty"`
	DateUnknown        bool       `json:"dateUnknown,omitempty"`
	EndAt              *time.Time `json:"endAt"`
	Hours              *float64   `json:"hours,omitempty"`
	Capacity           *int       `json:"capacity,omitempty"`
	AppliedCount       *int       `json:"appliedCount,omitempty"`
	ImageURL           string     `json:"imageUrl,omitempty"`
	IsActive           bool       `json:"isActive,omitempty"`
	Location           *string    `json:"location"`
	Name               string     `json:"name"`
	Organizer          string     `json:"organizer,omitempty"`
	OrganizerID        string     `json:"organizerId,omitempty"`
	OrganizerName      string     `json:"organizerName,omitempty"`
	RegistrationStatus string     `json:"registrationStatus,omitempty"`
	Status             string     `json:"status,omitempty"`
	SourceMissing      bool       `json:"sourceMissing,omitempty"`
	StartAt            *time.Time `json:"startAt"`
	URL                string     `json:"url,omitempty"`
	YoungID            string     `json:"youngId"`
	CreatedAt          *time.Time `json:"createdAt,omitempty"`
	LastSeenAt         *time.Time `json:"lastSeenAt,omitempty"`
}

type YoungEventPagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

type YoungEventPage struct {
	Data             []YoungEvent         `json:"data"`
	Pagination       YoungEventPagination `json:"pagination"`
	UnknownDateCount int                  `json:"unknownDateCount,omitempty"`
	Source           map[string]any       `json:"source,omitempty"`
}

// ListYoungEvents returns public second-classroom events. Life's API uses
// page/pageSize for this endpoint, with search matching event names.
func (c *Client) ListYoungEvents(ctx context.Context, page, pageSize int, search string) (YoungEventPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	params := openapi.GetApiCatalogYoungEventsParams{
		Page:     int64Ptr(int64(page)),
		PageSize: int64Ptr(int64(pageSize)),
	}
	if search = strings.TrimSpace(search); search != "" {
		params.Search = &search
	}

	var out YoungEventPage
	resp, err := c.Typed(ctx, "").GetApiCatalogYoungEvents(ctx, &params)
	if err := typedJSON(resp, err, "young events", &out); err != nil {
		return YoungEventPage{}, err
	}
	return out, nil
}

// GetYoungEvent returns one public second-classroom event by its upstream ID.
func (c *Client) GetYoungEvent(ctx context.Context, youngID string) (YoungEvent, error) {
	youngID = strings.TrimSpace(youngID)
	if youngID == "" {
		return YoungEvent{}, errors.New("young event id is required")
	}

	var out YoungEvent
	resp, err := c.Typed(ctx, "").GetApiCatalogYoungEventsYoungId(ctx, youngID)
	if err := typedJSON(resp, err, "young event", &out); err != nil {
		return YoungEvent{}, err
	}
	return out, nil
}

// YoungEventURL builds the public Life web link for an event.
func (c *Client) YoungEventURL(youngID string) string {
	return strings.TrimRight(c.server, "/") + "/catalog/young-events/" + url.PathEscape(strings.TrimSpace(youngID))
}
