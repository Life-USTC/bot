package life

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Life-USTC/Bot/internal/openapi"
)

// Candidate searches consume every page: uniqueness cannot be decided from
// just the first page of a catalog response.
func (c *Client) SectionCandidates(ctx context.Context, query string, semesterID int64) ([]map[string]any, error) {
	return catalogCandidates(func(page int64) (*http.Response, error) {
		return c.Typed(ctx, "").ListSections(ctx, &openapi.ListSectionsParams{
			Search: &query, SemesterId: &semesterID, Page: &page, PageSize: int64Ptr(100),
		})
	})
}

func (c *Client) CourseCandidates(ctx context.Context, query string) ([]map[string]any, error) {
	return catalogCandidates(func(page int64) (*http.Response, error) {
		return c.Typed(ctx, "").ListCourses(ctx, &openapi.ListCoursesParams{
			Search: &query, Page: &page, PageSize: int64Ptr(100),
		})
	})
}

func catalogCandidates(fetch func(int64) (*http.Response, error)) ([]map[string]any, error) {
	var result []map[string]any
	for page := int64(1); ; page++ {
		var body struct {
			Data       []map[string]any
			Pagination struct {
				Page       int64
				TotalPages int64
			}
		}
		response, err := fetch(page)
		if err := typedJSON(response, err, "catalog candidates", &body); err != nil {
			return nil, err
		}
		if body.Pagination.Page != page || body.Pagination.TotalPages < page {
			return nil, fmt.Errorf("catalog returned invalid pagination for page %d", page)
		}
		result = append(result, body.Data...)
		if page == body.Pagination.TotalPages {
			return result, nil
		}
		if len(body.Data) == 0 {
			return nil, fmt.Errorf("catalog page %d is unexpectedly empty", page)
		}
	}
}
