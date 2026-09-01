package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/Life-USTC/Bot/internal/commands"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/store"
)

const maxCampusToolSearchResults = 5

type campusToolSearchInput struct {
	Query string `json:"query" jsonschema_description:"Words describing the campus data lookup you need"`
}

type campusToolCallInput struct {
	Name      string         `json:"name" jsonschema_description:"Exact read-only tool name returned by search_campus_tools"`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema_description:"Arguments matching that tool's inputSchema exactly"`
}

type campusToolDocumentation struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// lazyMCPSession exposes only two stable meta-tools to the model. OAuth,
// MCP initialization, and tools/list happen only if the model actually asks
// for campus-tool documentation or invokes a campus read.
type lazyMCPSession struct {
	service  *Service
	identity store.Identity
	jobID    int64
	trace    *toolTraceNotifier

	once    sync.Once
	session *botmcp.Session
	tools   map[string]mcpgo.Tool
	err     error
}

func newLazyMCPSession(service *Service, identity store.Identity, jobID int64, trace *toolTraceNotifier) *lazyMCPSession {
	return &lazyMCPSession{service: service, identity: identity, jobID: jobID, trace: trace}
}

func (s *lazyMCPSession) appendTools(tools []tool.BaseTool) ([]tool.BaseTool, error) {
	var err error
	tools, err = appendInferredTool(tools, "search_campus_tools", "Search documentation for supplementary read-only campus tools. Search first, then pass the returned exact name and inputSchema to call_campus_tool. Bot commands should be searched and preferred first.", s.trace, s.search)
	if err != nil {
		return nil, err
	}
	return appendInferredTool(tools, "call_campus_tool", "Call one read-only campus tool previously returned by search_campus_tools. The result is the campus tool's actual result, without a status wrapper.", s.trace, s.call)
}

func (s *lazyMCPSession) ensure(ctx context.Context) error {
	s.once.Do(func() {
		if s.service == nil || s.service.mcpClient == nil || s.service.auth == nil {
			s.err = errors.New("campus tool service is unavailable")
			return
		}
		token, err := s.service.auth.MCPAccessToken(ctx, s.identity)
		if err != nil {
			s.err = fmt.Errorf("get MCP access token: %w", err)
			return
		}
		session, err := s.service.mcpClient.OpenSession(ctx, token)
		if err != nil {
			s.err = err
			return
		}
		listed, err := session.Tools(ctx)
		if err != nil {
			_ = session.Close()
			s.err = err
			return
		}
		readOnly := make(map[string]mcpgo.Tool)
		for _, candidate := range listed {
			if candidate.Annotations.ReadOnlyHint == nil || !*candidate.Annotations.ReadOnlyHint {
				s.service.logf("MCP mutation tool hidden from agent: name=%s", candidate.Name)
				continue
			}
			readOnly[candidate.Name] = candidate
		}
		s.session = session
		s.tools = readOnly
	})
	return s.err
}

func (s *lazyMCPSession) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
}

func (s *lazyMCPSession) search(ctx context.Context, input campusToolSearchInput) (string, error) {
	query := strings.ToLower(strings.TrimSpace(input.Query))
	if query == "" {
		return "", botmcp.NewRecoverableToolError("search_campus_tools", "query is required")
	}
	if err := s.ensure(ctx); err != nil {
		return "", err
	}
	tokens := strings.Fields(query)
	type match struct {
		name  string
		score int
	}
	matches := make([]match, 0, len(s.tools))
	for name, candidate := range s.tools {
		haystack := strings.ToLower(name + " " + candidate.Description)
		score := 0
		for _, token := range tokens {
			if strings.Contains(haystack, token) {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, match{name: name, score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].name < matches[j].name
	})
	if len(matches) > maxCampusToolSearchResults {
		matches = matches[:maxCampusToolSearchResults]
	}
	docs := make([]campusToolDocumentation, 0, len(matches))
	for _, matched := range matches {
		candidate := s.tools[matched.name]
		schemaJSON, err := campusToolInputSchema(candidate)
		if err != nil {
			return "", err
		}
		docs = append(docs, campusToolDocumentation{
			Name: candidate.Name, Description: candidate.Description, InputSchema: schemaJSON,
		})
	}
	data, err := json.Marshal(docs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func campusToolInputSchema(candidate mcpgo.Tool) (json.RawMessage, error) {
	if len(candidate.RawInputSchema) > 0 {
		if !json.Valid(candidate.RawInputSchema) {
			return nil, fmt.Errorf("campus tool %s has an invalid input schema", candidate.Name)
		}
		return append(json.RawMessage(nil), candidate.RawInputSchema...), nil
	}
	data, err := json.Marshal(candidate.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encode campus tool %s input schema: %w", candidate.Name, err)
	}
	return data, nil
}

func (s *lazyMCPSession) call(ctx context.Context, input campusToolCallInput) (string, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return "", botmcp.NewRecoverableToolError("call_campus_tool", "name is required")
	}
	if err := s.ensure(ctx); err != nil {
		return "", err
	}
	if _, found := s.tools[name]; !found {
		return "", botmcp.NewRecoverableToolError(name, "the requested read-only campus tool was not found")
	}

	execution, tracked, err := s.prepareExecution(ctx, name, input.Arguments)
	if err != nil {
		return "", err
	}
	if tracked && execution.State != store.CapabilityExecutionRunning {
		return existingCampusToolResult(execution), nil
	}
	result, callErr := s.session.Call(ctx, name, input.Arguments)
	if s.trace != nil {
		s.trace.Notify(ctx, name, input.Arguments, result, callErr)
	}
	if tracked {
		receipt := execution.Receipt
		receipt.Subject = campusReceiptSubject(name, input.Arguments, result)
		if receipt.Subject != execution.Receipt.Subject {
			if err := s.service.handler.Store.UpdateCapabilityExecutionReceipt(ctx, execution.ID, receipt); err != nil {
				return "", err
			}
		}
		storedErr := callErr
		if safe, ok := botmcp.ModelToolErrorResult(callErr); ok {
			storedErr = errors.New(safe)
		}
		if _, err := s.service.handler.Store.FinishCapabilityExecution(ctx, execution.ID, result, storedErr); err != nil {
			return "", err
		}
	}
	return result, callErr
}

func (s *lazyMCPSession) prepareExecution(ctx context.Context, name string, arguments map[string]any) (store.CapabilityExecution, bool, error) {
	if s.service == nil || s.service.handler.Store == nil || s.jobID <= 0 {
		return store.CapabilityExecution{}, false, nil
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return store.CapabilityExecution{}, false, err
	}
	callID := strings.TrimSpace(compose.GetToolCallID(ctx))
	if callID == "" {
		digest := sha256.Sum256(append([]byte(name+"\x00"), encoded...))
		callID = fmt.Sprintf("%x", digest[:12])
	}
	execution, _, err := s.service.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: s.identity, JobID: s.jobID,
		DedupeKey:  "conversation-job:" + fmt.Sprint(s.jobID) + ":mcp:" + callID,
		ToolCallID: callID, Capability: "mcp:" + name, Arguments: []string{string(encoded)}, Effect: string(commands.EffectRead),
		Receipt: store.CapabilityReceipt{Action: "查询", Resource: campusReceiptResource(name), Subject: campusReceiptSubject(name, arguments, "")},
	})
	return execution, true, err
}

func existingCampusToolResult(execution store.CapabilityExecution) string {
	switch execution.State {
	case store.CapabilityExecutionSucceeded:
		return execution.Result
	case store.CapabilityExecutionFailed, store.CapabilityExecutionUnknown:
		return execution.Error
	default:
		return "the campus query has not completed"
	}
}

func campusReceiptResource(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "course"), strings.Contains(lower, "section"), strings.Contains(lower, "class"):
		return "课程"
	case strings.Contains(lower, "teacher"):
		return "教师"
	case strings.Contains(lower, "semester"):
		return "学期"
	case strings.Contains(lower, "homework"):
		return "作业"
	case strings.Contains(lower, "exam"):
		return "考试"
	case strings.Contains(lower, "bus"):
		return "校车"
	default:
		return "校园数据"
	}
}

func campusReceiptSubject(name string, arguments map[string]any, result string) string {
	if subject := campusResultSubject(result); subject != "" {
		return subject
	}
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(arguments[key]))
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) > 0 {
		return strings.Join(values, " ")
	}
	return name
}

func campusResultSubject(result string) string {
	var value any
	if json.Unmarshal([]byte(result), &value) != nil {
		return ""
	}
	item := firstResultObject(value)
	if item == nil {
		return ""
	}
	course := nestedResultString(item, "course", "namePrimary", "nameCn", "name")
	if course == "" {
		course = firstResultString(item, "courseName", "namePrimary", "nameCn", "name", "code")
	}
	teacher := nestedResultString(item, "teacher", "namePrimary", "nameCn", "name")
	if teacher == "" {
		teacher = firstResultString(item, "teacherName", "teacherNamePrimary", "teacherNameCn")
	}
	semester := nestedResultString(item, "semester", "namePrimary", "nameCn", "name")
	if semester == "" {
		semester = firstResultString(item, "semesterName", "semesterNamePrimary", "semesterNameCn")
	}
	if course == "" {
		return ""
	}
	qualifiers := make([]string, 0, 2)
	for _, value := range []string{teacher, semester} {
		if value != "" {
			qualifiers = append(qualifiers, value)
		}
	}
	if len(qualifiers) == 0 {
		return course
	}
	return course + "（" + strings.Join(qualifiers, "，") + "）"
}

func firstResultObject(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"data", "items", "results"} {
			if nested, found := typed[key]; found {
				if item := firstResultObject(nested); item != nil {
					return item
				}
			}
		}
		return typed
	case []any:
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				return object
			}
		}
	}
	return nil
}

func nestedResultString(item map[string]any, key string, names ...string) string {
	nested, _ := item[key].(map[string]any)
	return firstResultString(nested, names...)
}

func firstResultString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
