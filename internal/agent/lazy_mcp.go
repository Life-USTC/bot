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
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/retry"
	"github.com/Life-USTC/Bot/internal/store"
)

type campusToolCallInput struct {
	Name      string         `json:"name" jsonschema_description:"Exact read-only tool name returned by search_campus_tools"`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema_description:"Arguments matching that tool's inputSchema exactly"`
}

// lazyMCPSession registers the server's own tools with the model, with the
// server's own JSON Schemas. Nothing is re-declared here: the enums,
// descriptions and defaults the server publishes are the constraints a
// hand-written host copy cannot reproduce and, as the weather location names
// showed, will eventually contradict.
//
// The catalog is cached process-wide, so a turn pays OAuth and tools/list only
// when the cache has expired rather than on every request.
type lazyMCPSession struct {
	service  *Service
	identity store.Identity
	jobID    int64

	once    sync.Once
	session *botmcp.Session
	listed  []mcpgo.Tool
	tools   map[string]mcpgo.Tool
	err     error
}

func newLazyMCPSession(service *Service, identity store.Identity, jobID int64) *lazyMCPSession {
	return &lazyMCPSession{service: service, identity: identity, jobID: jobID}
}

func (s *lazyMCPSession) appendTools(ctx context.Context, tools []tool.BaseTool) ([]tool.BaseTool, error) {
	catalog, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	for _, remote := range catalog {
		name, description, rawSchema, err := campusToolInfo(remote)
		if err != nil {
			return nil, err
		}
		parsed := &jsonschema.Schema{}
		if err := json.Unmarshal(rawSchema, parsed); err != nil {
			return nil, fmt.Errorf("decode campus tool %s input schema: %w", name, err)
		}
		tools = append(tools, &campusTool{session: s, name: name, info: &schema.ToolInfo{
			Name: name, Desc: description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parsed),
		}})
	}
	return tools, nil
}

// catalog lists the remote tools, preferring the process-wide cache. Anonymous
// and authenticated listings are cached apart because the server filters the
// catalog by the caller's scopes, and this Bot requests one fixed scope set, so
// every logged-in caller sees the same catalog.
func (s *lazyMCPSession) catalog(ctx context.Context) ([]mcpgo.Tool, error) {
	key := "anonymous"
	if store.HasUserIdentity(s.identity) {
		key = "authenticated"
	}
	if cached, found := s.service.campusCatalog.get(key); found {
		return cached, nil
	}
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	s.service.campusCatalog.put(key, s.listed)
	return s.listed, nil
}

// campusTool is one remote tool presented to the model under its own name and
// schema.
type campusTool struct {
	session *lazyMCPSession
	name    string
	info    *schema.ToolInfo
}

func (t *campusTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

func (t *campusTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	arguments := map[string]any{}
	if trimmed := strings.TrimSpace(argumentsInJSON); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal([]byte(trimmed), &arguments); err != nil {
			return "", botmcp.NewRecoverableToolError(t.name, "arguments must be a JSON object matching this tool's inputSchema")
		}
	}
	return t.session.call(ctx, campusToolCallInput{Name: t.name, Arguments: arguments})
}

// campusCatalogAttempts bounds how hard the host tries to read the catalog
// before giving up. Registering tools natively makes the catalog a
// precondition of the turn rather than something fetched on demand, so a
// transient failure must be retried instead of quietly shrinking what the
// model can do.
const campusCatalogAttempts = 3

// accessToken obtains the caller's token, or reports that the campus service is
// unusable this turn.
//
// Only one outcome is not a failure: ErrNotLoggedIn means the store holds no
// grant at all, so there is nothing to retry and the public half of the catalog
// — which needs no token — is exactly what this caller is entitled to.
//
// Everything else is a failure and is treated as one. A refused grant is
// reported immediately so the user is told to log in again; a transient error
// is retried, because a token endpoint hiccup is precisely what retries are
// for. A logged-in caller is never quietly reduced to public tools: their
// personal tools missing would look to them like the Bot forgot they exist.
func (s *lazyMCPSession) accessToken(ctx context.Context) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= campusCatalogAttempts; attempt++ {
		token, err := s.service.auth.MCPAccessToken(ctx, s.identity)
		if err == nil {
			return token, nil
		}
		lastErr = err
		if errors.Is(err, auth.ErrNotLoggedIn) {
			return "", nil
		}
		if isMCPAuthorizationError(err) {
			return "", err
		}
		if attempt < campusCatalogAttempts {
			s.service.logf("campus token attempt %d failed, retrying: error=%v", attempt, err)
			if !retry.Wait(ctx, time.Duration(attempt)*250*time.Millisecond) {
				break
			}
		}
	}
	return "", fmt.Errorf("get MCP access token: %w", lastErr)
}

func (s *lazyMCPSession) openCatalog(ctx context.Context, token string) (*botmcp.Session, []mcpgo.Tool, error) {
	var lastErr error
	for attempt := 1; attempt <= campusCatalogAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		session, err := s.service.mcpClient.OpenSession(ctx, token)
		if err == nil {
			var listed []mcpgo.Tool
			listed, err = session.Tools(ctx)
			if err == nil {
				return session, listed, nil
			}
			_ = session.Close()
		}
		lastErr = err
		// An authorization failure will not fix itself by trying again.
		if isMCPAuthorizationError(err) {
			break
		}
		if attempt < campusCatalogAttempts {
			s.service.logf("campus catalog attempt %d failed, retrying: error=%v", attempt, err)
			if !retry.Wait(ctx, time.Duration(attempt)*250*time.Millisecond) {
				return nil, nil, ctx.Err()
			}
		}
	}
	return nil, nil, lastErr
}

func (s *lazyMCPSession) ensure(ctx context.Context) error {
	s.once.Do(func() {
		if s.service == nil || s.service.mcpClient == nil || s.service.auth == nil {
			s.err = errors.New("campus tool service is unavailable")
			return
		}
		token, err := s.accessToken(ctx)
		if err != nil {
			s.err = err
			return
		}
		session, listed, err := s.openCatalog(ctx, token)
		if err != nil {
			s.err = err
			return
		}
		available := make(map[string]mcpgo.Tool, len(listed))
		for _, candidate := range listed {
			available[candidate.Name] = candidate
		}
		s.session = session
		s.listed = listed
		s.tools = available
	})
	return s.err
}

func (s *lazyMCPSession) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
}

func (s *lazyMCPSession) call(ctx context.Context, input campusToolCallInput) (string, error) {
	// A confirmed or denied destructive call re-enters here after the interrupt.
	if wasInterrupted, hasState, state := tool.GetInterruptState[capabilityInterruptState](ctx); wasInterrupted {
		if !hasState || len(state.ExecutionIDs) != 1 {
			return "", errors.New("confirmed campus call checkpoint has no operation state")
		}
		return s.resolveCampusCall(ctx, state)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return "", botmcp.NewRecoverableToolError("call_campus_tool", "name is required")
	}
	if err := s.ensure(ctx); err != nil {
		return "", err
	}
	remote, found := s.tools[name]
	if !found {
		return "", botmcp.NewRecoverableToolError(name, "no campus tool by that name is available in this turn")
	}
	effect := campusEffectOf(remote)
	if effect == campusEffectDestructive {
		// Same consent rule as a destructive Bot command: the server decides
		// whether the user may do this, the host asks whether they want to.
		return s.confirmCampusCall(ctx, name, input.Arguments)
	}

	execution, tracked, execute, err := s.prepareExecution(ctx, name, input.Arguments, effect)
	if err != nil {
		return "", err
	}
	if tracked {
		currentLease := store.ConversationJobLeaseFromContext(ctx, execution.JobID)
		if execution.State == store.CapabilityExecutionRunning && execution.LeaseToken != currentLease {
			claimed, claimedForExecution, claimErr := s.service.handler.Store.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID, currentLease)
			if claimErr != nil {
				return "", markDurableAgentStateError("recover campus read", claimErr)
			}
			if !claimedForExecution {
				if capabilityExecutionTerminal(claimed.State) {
					return existingCampusToolResult(claimed), nil
				}
				return "", errors.New("campus read ownership changed before recovery")
			}
			execution = claimed
			execute = true
		}
		if execution.State != store.CapabilityExecutionRunning {
			return existingCampusToolResult(execution), nil
		}
		if !execute {
			return "", errors.New("campus read is already running under the current job lease")
		}
	}
	result, callErr := s.session.Call(ctx, name, input.Arguments)
	if tracked {
		receipt := execution.Receipt
		receipt.Subject = campusReceiptSubject(name, input.Arguments, result)
		if receipt.Subject != execution.Receipt.Subject {
			if err := s.service.handler.Store.UpdateCapabilityExecutionReceipt(ctx, execution.ID, execution.LeaseToken, receipt); err != nil {
				return "", markDurableAgentStateError("update campus read receipt", err)
			}
		}
		storedErr := callErr
		if safe, ok := botmcp.ModelToolErrorResult(callErr); ok {
			storedErr = errors.New(safe)
		}
		if _, err := s.service.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, result, storedErr); err != nil {
			return "", markDurableAgentStateError("finish campus read", err)
		}
	}
	// A tool-reported failure is evidence the model can act on and is returned
	// as a result. A transport or authorization failure is the host's problem
	// and still aborts the run.
	if callErr != nil {
		detail, recoverable := botmcp.ModelToolErrorResult(callErr)
		if !recoverable {
			return "", callErr
		}
		return campusCallToolResult(name, input.Arguments, result, errors.New(detail)), nil
	}
	return campusCallToolResult(name, input.Arguments, result, nil), nil
}

func (s *lazyMCPSession) prepareExecution(ctx context.Context, name string, arguments map[string]any, effect campusToolEffect) (store.CapabilityExecution, bool, bool, error) {
	if s.service == nil || s.service.handler.Store == nil || s.jobID <= 0 {
		if effect == campusEffectDestructive {
			return store.CapabilityExecution{}, false, false, errors.New("destructive campus call requires a persisted conversation job")
		}
		return store.CapabilityExecution{}, false, true, nil
	}
	leaseCtx, err := ensureCapabilityJobLease(ctx, s.service.handler.Store, s.jobID)
	if err != nil {
		return store.CapabilityExecution{}, false, false, err
	}
	ctx = leaseCtx
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return store.CapabilityExecution{}, false, false, err
	}
	callID := strings.TrimSpace(compose.GetToolCallID(ctx))
	if callID == "" {
		digest := sha256.Sum256(append([]byte(name+"\x00"), encoded...))
		callID = fmt.Sprintf("%x", digest[:12])
	}
	execution, created, err := s.service.handler.Store.PrepareCapabilityExecution(ctx, store.CapabilityExecutionPrepare{
		Identity: s.identity, JobID: s.jobID, LeaseToken: store.ConversationJobLeaseFromContext(ctx, s.jobID),
		DedupeKey:  "conversation-job:" + fmt.Sprint(s.jobID) + ":mcp:" + callID,
		ToolCallID: callID, Capability: campusExecutionCapability(name), Arguments: []string{string(encoded)},
		Effect:               campusExecutionEffect(effect),
		RequiresConfirmation: effect == campusEffectDestructive,
		Receipt:              store.CapabilityReceipt{Action: campusReceiptAction(effect), Resource: campusReceiptResource(name), Subject: campusReceiptSubject(name, arguments, "")},
	})
	return execution, true, created, markDurableAgentStateError("prepare campus call", err)
}

func campusReceiptResource(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "young_event"):
		return "第二课堂活动"
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

func campusExecutionCapability(name string) string { return "mcp:" + name }

func campusExecutionToolName(capability string) string {
	return strings.TrimPrefix(strings.TrimSpace(capability), "mcp:")
}

func campusExecutionEffect(effect campusToolEffect) string {
	switch effect {
	case campusEffectDestructive:
		return string(commands.EffectDestructive)
	case campusEffectWrite:
		return string(commands.EffectWrite)
	default:
		return string(commands.EffectRead)
	}
}

func campusReceiptAction(effect campusToolEffect) string {
	switch effect {
	case campusEffectDestructive:
		return "删除"
	case campusEffectWrite:
		return "修改"
	default:
		return "查询"
	}
}

// confirmCampusCall records the pending destructive call and hands control back
// to the coordinator, which shows the user exactly one operation at a time.
func (s *lazyMCPSession) confirmCampusCall(ctx context.Context, name string, arguments map[string]any) (string, error) {
	execution, tracked, _, err := s.prepareExecution(ctx, name, arguments, campusEffectDestructive)
	if err != nil {
		return "", err
	}
	if !tracked {
		return "", errors.New("destructive campus call requires a persisted conversation job")
	}
	return s.resolveCampusExecution(ctx, execution.ID, false)
}

func (s *lazyMCPSession) resolveCampusCall(ctx context.Context, state capabilityInterruptState) (string, error) {
	return s.resolveCampusExecution(ctx, state.ExecutionIDs[0], true)
}

// resolveCampusExecution mirrors the Bot capability resume path for a remote
// call: the durable row is the only record of what the user decided, so the
// outcome is read from it rather than from anything the model reports.
func (s *lazyMCPSession) resolveCampusExecution(ctx context.Context, executionID string, persistResult bool) (string, error) {
	db := s.service.handler.Store
	execution, found, err := db.CapabilityExecution(ctx, executionID)
	if err != nil {
		return "", markDurableAgentStateError("read campus execution", err)
	}
	if !found {
		return "", fmt.Errorf("campus execution %s is missing", executionID)
	}
	if execution.Identity != s.identity {
		return "", fmt.Errorf("campus execution %s belongs to another conversation", executionID)
	}
	leaseCtx, err := ensureCapabilityJobLease(ctx, db, execution.JobID)
	if err != nil {
		return "", err
	}
	ctx = leaseCtx

	if execution.State == store.CapabilityExecutionAwaitingConfirmation {
		return "", tool.StatefulInterrupt(ctx, capabilityInterruptInfo{
			Kind: capabilityInterruptConfirmation, ExecutionIDs: []string{execution.ID},
		}, capabilityInterruptState{ExecutionIDs: []string{execution.ID}, ToolCallID: execution.ToolCallID})
	}
	if execution.State == store.CapabilityExecutionApproved {
		claimed, execute, claimErr := db.ClaimCapabilityExecutionForJob(ctx, execution.ID, execution.JobID,
			store.ConversationJobLeaseFromContext(ctx, execution.JobID))
		if claimErr != nil {
			return "", markDurableAgentStateError("claim approved campus call", claimErr)
		}
		if execute {
			execution, err = s.executeApprovedCampusCall(ctx, claimed)
			if err != nil {
				return "", err
			}
		} else {
			execution = claimed
		}
	}
	if !capabilityExecutionTerminal(execution.State) {
		return "", fmt.Errorf("campus execution %s is not terminal: %s", execution.ID, execution.State)
	}
	if execution.State != store.CapabilityExecutionSucceeded {
		toolOutcomesFromContext(ctx).markError(execution.ToolCallID)
	}
	result := existingCampusToolResult(execution)
	if persistResult {
		if err := s.service.persistResumedToolResult(ctx, s.identity, execution.JobID,
			execution.ToolCallID, campusExecutionToolName(execution.Capability), result); err != nil {
			return "", err
		}
	}
	return result, nil
}

// executeApprovedCampusCall runs a call the user actually approved. A mutation
// that was interrupted mid-flight is never replayed: the row is finalized as
// unknown by the same stale-lease rules that cover Bot mutations.
func (s *lazyMCPSession) executeApprovedCampusCall(ctx context.Context, execution store.CapabilityExecution) (store.CapabilityExecution, error) {
	name := campusExecutionToolName(execution.Capability)
	arguments := map[string]any{}
	if len(execution.Arguments) == 1 && strings.TrimSpace(execution.Arguments[0]) != "" {
		if err := json.Unmarshal([]byte(execution.Arguments[0]), &arguments); err != nil {
			finished, finishErr := s.service.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, "",
				errors.New("宿主无法恢复已确认的校园操作参数"))
			return finished, markDurableAgentStateError("record unrestorable campus call", finishErr)
		}
	}
	if err := s.ensure(ctx); err != nil {
		return execution, err
	}
	result, callErr := s.session.Call(ctx, name, arguments)
	storedErr := callErr
	if safe, ok := botmcp.ModelToolErrorResult(callErr); ok {
		storedErr = errors.New(safe)
	}
	if callErr != nil && storedErr == callErr {
		// A transport failure on a destructive call may or may not have taken
		// effect, so it is recorded as unknown and never retried.
		finished, finishErr := s.service.handler.Store.FinishCapabilityExecutionUnknown(ctx, execution.ID, execution.LeaseToken, "",
			"destructive campus call did not report an outcome")
		return finished, markDurableAgentStateError("record unknown campus outcome", finishErr)
	}
	finished, err := s.service.handler.Store.FinishCapabilityExecution(ctx, execution.ID, execution.LeaseToken, result, storedErr)
	return finished, markDurableAgentStateError("finish approved campus call", err)
}
