package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

type Handler struct {
	Life   *life.Client
	Auth   *auth.Manager
	Store  *store.Store
	Prefix string
}

type Input struct {
	Text     string
	Identity store.Identity
}

func (h Handler) Handle(ctx context.Context, input Input) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(input.Text))
	if len(fields) == 0 || fields[0] != h.Prefix {
		return "", false
	}
	h.recordState(ctx, input.Identity, fields)
	if len(fields) == 1 || fields[1] == "help" {
		return h.help(), true
	}

	switch fields[1] {
	case "login":
		return h.login(ctx, input.Identity, fields[2:]), true
	case "logout":
		return h.logout(ctx, input.Identity), true
	case "me":
		return h.me(ctx, input.Identity), true
	case "todo":
		return h.todo(ctx, input.Identity, fields[2:]), true
	case "sub", "subs", "subscription":
		return h.subscription(ctx, input.Identity), true
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
		h.Prefix + " login",
		h.Prefix + " login status",
		h.Prefix + " logout",
		h.Prefix + " me",
		h.Prefix + " todo",
		h.Prefix + " todo add <title>",
		h.Prefix + " sub",
		h.Prefix + " semester",
		h.Prefix + " course <keyword>",
		h.Prefix + " section <keyword>",
		h.Prefix + " bus",
	}, "\n")
}

func (h Handler) login(ctx context.Context, ident store.Identity, args []string) string {
	if h.Auth == nil {
		return "Login is not configured."
	}
	if len(args) > 0 && args[0] == "status" {
		result, err := h.Auth.PollDeviceLogin(ctx, ident)
		if err != nil {
			return "Login status failed: " + err.Error()
		}
		return result.Message
	}
	session, err := h.Auth.BeginDeviceLogin(ctx, ident)
	if err != nil {
		return "Login failed: " + err.Error()
	}
	link := session.VerificationURIComplete
	if link == "" {
		link = session.VerificationURI
	}
	return strings.Join([]string{
		"Open this link to sign in to Life @ USTC:",
		link,
		"Code: " + session.UserCode,
		fmt.Sprintf("Then send: %s login status", h.Prefix),
	}, "\n")
}

func (h Handler) logout(ctx context.Context, ident store.Identity) string {
	if h.Auth == nil {
		return "Login is not configured."
	}
	if err := h.Auth.Logout(ctx, ident); err != nil {
		return "Logout failed: " + err.Error()
	}
	return "Logged out."
}

func (h Handler) me(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	me, err := h.Life.Me(ctx, token)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			me, err = h.Life.Me(ctx, token)
		}
	}
	if err != nil {
		return "Failed to load profile: " + err.Error()
	}
	name := firstString(me, "name", "username", "preferred_username", "email")
	if name == "" {
		name = firstString(me, "id", "sub")
	}
	return "Signed in as: " + name
}

func (h Handler) todo(ctx context.Context, ident store.Identity, args []string) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	if len(args) > 0 && args[0] == "add" {
		title := strings.TrimSpace(strings.Join(args[1:], " "))
		if title == "" {
			return "Usage: " + h.Prefix + " todo add <title>"
		}
		created, err := h.Life.CreateTodo(ctx, token, title)
		if err != nil && strings.Contains(err.Error(), " returned 401:") {
			token, refreshErr := h.Auth.Refresh(ctx, ident)
			if refreshErr == nil {
				created, err = h.Life.CreateTodo(ctx, token, title)
			}
		}
		if err != nil {
			return "Failed to create todo: " + err.Error()
		}
		id := firstString(created, "id")
		if id == "" {
			id = fmt.Sprint(created["id"])
		}
		return "Created todo: " + id
	}
	todos, err := h.Life.Todos(ctx, token, "false")
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			todos, err = h.Life.Todos(ctx, token, "false")
		}
	}
	if err != nil {
		return "Failed to load todos: " + err.Error()
	}
	if len(todos) == 0 {
		return "No pending todos."
	}
	lines := []string{"Pending todos:"}
	for i, todo := range todos {
		if i >= 8 {
			lines = append(lines, fmt.Sprintf("...and %d more", len(todos)-i))
			break
		}
		lines = append(lines, "- "+firstString(todo, "title"))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) subscription(ctx context.Context, ident store.Identity) string {
	token, ok := h.accessToken(ctx, ident)
	if !ok {
		return h.loginRequired()
	}
	data, err := h.Life.CurrentSubscription(ctx, token)
	if err != nil && strings.Contains(err.Error(), " returned 401:") {
		token, refreshErr := h.Auth.Refresh(ctx, ident)
		if refreshErr == nil {
			data, err = h.Life.CurrentSubscription(ctx, token)
		}
	}
	if err != nil {
		return "Failed to load subscriptions: " + err.Error()
	}
	sub, _ := data["subscription"].(map[string]any)
	sections, _ := sub["sections"].([]any)
	if len(sections) == 0 {
		return "No subscribed sections."
	}
	lines := []string{"Subscribed sections:"}
	for i, item := range sections {
		if i >= 8 {
			lines = append(lines, fmt.Sprintf("...and %d more", len(sections)-i))
			break
		}
		section, _ := item.(map[string]any)
		lines = append(lines, formatSection(section))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) accessToken(ctx context.Context, ident store.Identity) (string, bool) {
	if h.Auth == nil {
		return "", false
	}
	token, err := h.Auth.AccessToken(ctx, ident)
	if err == nil {
		return token, true
	}
	return "", !errors.Is(err, auth.ErrNotLoggedIn) && false
}

func (h Handler) loginRequired() string {
	return "This command requires login. Send: " + h.Prefix + " login"
}

func (h Handler) recordState(ctx context.Context, ident store.Identity, fields []string) {
	if h.Store == nil || ident.Platform == "" || ident.UserID == "" {
		return
	}
	command := ""
	if len(fields) > 1 {
		command = fields[1]
	}
	_ = h.Store.RecordConversationState(ctx, ident, command, strconv.Quote(strings.Join(fields, " ")))
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
