package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/life"
)

type Handler struct {
	Life   *life.Client
	Prefix string
}

func (h Handler) Handle(ctx context.Context, text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || fields[0] != h.Prefix {
		return "", false
	}
	if len(fields) == 1 || fields[1] == "help" {
		return h.help(), true
	}

	switch fields[1] {
	case "ping":
		if err := h.Life.Health(ctx); err != nil {
			return "Life @ USTC API unavailable: " + err.Error(), true
		}
		return "Life @ USTC API is reachable.", true
	case "semester":
		return h.currentSemester(ctx), true
	case "course":
		return h.searchCourses(ctx, strings.Join(fields[2:], " ")), true
	case "section":
		return h.searchSections(ctx, strings.Join(fields[2:], " ")), true
	case "bus":
		return h.bus(ctx), true
	default:
		return h.help(), true
	}
}

func (h Handler) help() string {
	return strings.Join([]string{
		"Life @ USTC commands:",
		h.Prefix + " ping",
		h.Prefix + " semester",
		h.Prefix + " course <keyword>",
		h.Prefix + " section <keyword>",
		h.Prefix + " bus",
	}, "\n")
}

func (h Handler) currentSemester(ctx context.Context) string {
	semester, err := h.Life.CurrentSemester(ctx)
	if err != nil {
		return "Failed to load current semester: " + err.Error()
	}
	name := firstString(semester, "name", "nameCn", "namePrimary")
	if name == "" {
		name = fmt.Sprint(semester["id"])
	}
	return "Current semester: " + name
}

func (h Handler) searchCourses(ctx context.Context, keyword string) string {
	if keyword == "" {
		return "Usage: " + h.Prefix + " course <keyword>"
	}
	courses, err := h.Life.SearchCourses(ctx, keyword, 5)
	if err != nil {
		return "Failed to search courses: " + err.Error()
	}
	if len(courses) == 0 {
		return "No courses found."
	}
	lines := []string{"Courses:"}
	for _, course := range courses {
		lines = append(lines, formatCourse(course))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) searchSections(ctx context.Context, keyword string) string {
	if keyword == "" {
		return "Usage: " + h.Prefix + " section <keyword>"
	}
	sections, err := h.Life.SearchSections(ctx, keyword, 5)
	if err != nil {
		return "Failed to search sections: " + err.Error()
	}
	if len(sections) == 0 {
		return "No sections found."
	}
	lines := []string{"Sections:"}
	for _, section := range sections {
		lines = append(lines, formatSection(section))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) bus(ctx context.Context) string {
	data, err := h.Life.Bus(ctx)
	if err != nil {
		return "Failed to load bus data: " + err.Error()
	}
	trips, _ := data["trips"].([]any)
	routes, _ := data["routes"].([]any)
	return fmt.Sprintf("Bus data loaded: %d routes, %d trips.", len(routes), len(trips))
}

func formatCourse(course map[string]any) string {
	code := firstString(course, "code")
	name := firstString(course, "namePrimary", "nameCn", "name")
	if code == "" {
		return "- " + name
	}
	return "- " + code + " " + name
}

func formatSection(section map[string]any) string {
	code := firstString(section, "code")
	course := nestedString(section, "course", "namePrimary", "nameCn", "name")
	semester := nestedString(section, "semester", "name")
	parts := []string{code, course, semester}
	return "- " + strings.Join(nonEmpty(parts), " ")
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func nestedString(m map[string]any, key string, nestedKeys ...string) string {
	child, ok := m[key].(map[string]any)
	if !ok {
		return ""
	}
	return firstString(child, nestedKeys...)
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
