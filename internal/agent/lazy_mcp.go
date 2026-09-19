package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/commands"
	"github.com/Life-USTC/Bot/internal/life"
	botmcp "github.com/Life-USTC/Bot/internal/mcp"
	"github.com/Life-USTC/Bot/internal/retry"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const maxCampusToolSearchResults = 5

type campusToolEffect string

const (
	campusEffectRead        campusToolEffect = "read"
	campusEffectWrite       campusToolEffect = "write"
	campusEffectDestructive campusToolEffect = "destructive"
)

// campusCatalogTTL bounds how stale a cached tools/list result may be. Tool
// definitions are deployment configuration rather than user data, so caching
// them process-wide means a tools/list round trip is only paid again once the
// catalog has expired, not on every lazyMCPSession. The OAuth token itself
// already carries its own expiry-based cache in internal/auth; this cache
// targets the separate tools/list RPC that a fresh lazyMCPSession otherwise
// repeats on every turn.
const campusCatalogTTL = 10 * time.Minute

type campusCatalog struct {
	tools     []mcpgo.Tool
	fetchedAt time.Time
}

// campusCatalogCache is shared process-wide on the Service, so every
// lazyMCPSession created for any turn or inventory lookup consults the same
// entries. Anonymous and authenticated listings are cached separately because
// the server filters the catalog by the caller's scopes: a shared conversation
// has no user token and legitimately sees only the public tools, so its
// catalog must never be served to, or from, an authenticated caller.
type campusCatalogCache struct {
	mu      sync.Mutex
	entries map[string]campusCatalog
	now     func() time.Time
}

func newCampusCatalogCache() *campusCatalogCache {
	return &campusCatalogCache{entries: make(map[string]campusCatalog), now: time.Now}
}

func (c *campusCatalogCache) get(key string) ([]mcpgo.Tool, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	if !found || c.now().Sub(entry.fetchedAt) > campusCatalogTTL {
		return nil, false
	}
	return entry.tools, true
}

func (c *campusCatalogCache) put(key string, tools []mcpgo.Tool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = campusCatalog{tools: append([]mcpgo.Tool(nil), tools...), fetchedAt: c.now()}
}

// campusCatalogCacheKey partitions the cache by authorization class. This Bot
// requests one fixed scope set, so every caller holding a token sees the same
// authenticated catalog and every tokenless caller sees the same public one.
//
// The key is derived from the token actually obtained, never from the shape of
// the identity: a caller can carry a full Platform/UserID identity and still be
// logged out, in which case MCPAccessToken returns ErrNotLoggedIn, the session
// opens anonymously, and the server returns only the public catalog. Keying on
// the identity would file that public catalog under "authenticated" — starving
// real logged-in callers of their tools, and in the other direction serving a
// logged-out caller the authenticated catalog a logged-in caller had warmed.
func campusCatalogCacheKey(token string) string {
	if token != "" {
		return "authenticated"
	}
	return "anonymous"
}

type campusToolSearchInput struct {
	Query string `json:"query" jsonschema_description:"Words describing the campus data lookup you need"`
}

type campusToolCallInput struct {
	Name      string         `json:"name" jsonschema_description:"Exact MCP tool name returned by search_campus_tools"`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema_description:"Arguments matching that tool's inputSchema exactly"`
}

type campusResourceReadInput struct {
	URI string `json:"uri" jsonschema_description:"Exact resource URI returned by list_campus_resources"`
}

type campusPromptGetInput struct {
	Name      string            `json:"name" jsonschema_description:"Exact prompt name returned by list_campus_prompts"`
	Arguments map[string]string `json:"arguments,omitempty" jsonschema_description:"Prompt arguments matching the listed prompt's contract"`
}

type campusToolDocumentation struct {
	Name         string               `json:"name"`
	Title        string               `json:"title,omitempty"`
	Description  string               `json:"description,omitempty"`
	InputSchema  json.RawMessage      `json:"inputSchema"`
	OutputSchema json.RawMessage      `json:"outputSchema,omitempty"`
	Annotations  mcpgo.ToolAnnotation `json:"annotations"`
	Effect       campusToolEffect     `json:"effect"`
}

// lazyMCPSession owns one authenticated MCP session for a private agent run.
// Discovery exposes exact server schemas while the
// stable meta-tools remain available for discovery and GraphQL context.
type lazyMCPSession struct {
	service      *Service
	identity     store.Identity
	jobID        int64
	sendResponse func(context.Context, store.Identity, commands.Response) error

	once    sync.Once
	session *botmcp.Session
	tools   map[string]mcpgo.Tool
	err     error
}

func newLazyMCPSession(service *Service, identity store.Identity, jobID int64) *lazyMCPSession {
	return &lazyMCPSession{service: service, identity: identity, jobID: jobID}
}

func (s *lazyMCPSession) appendTools(tools []tool.BaseTool) ([]tool.BaseTool, error) {
	var err error
	tools, err = appendInferredTool(tools, campusSearchToolName, "Search all MCP tools by names, descriptions, aliases, parameter fields and examples. Returns multiple candidates with the complete original schema and effect. Public tools can be discovered without login. Use query * for the full catalog.", s.search)
	if err != nil {
		return nil, err
	}
	tools, err = appendInferredTool(tools, campusCallToolName, "Call one MCP tool by the exact name returned by search_campus_tools. Returns the shared JSON result envelope with original domain data. Ordinary writes execute directly; dangerous and unknown-risk operations require user confirmation. All writes are durable and never replayed after an unknown outcome.", s.call)
	if err != nil {
		return nil, err
	}
	tools, err = appendInferredTool(tools, "list_campus_resources", "List MCP resources and URI templates. Use this to find the GraphQL schema or other server context before constructing a query.", func(ctx context.Context, _ emptyInput) (string, error) {
		if err := s.ensure(ctx); err != nil {
			return "", normalizeCampusSetupError(ctx, "list_campus_resources", err)
		}
		result, err := s.session.Resources(ctx)
		if err != nil {
			return "", normalizeCampusReadCallError(ctx, "list_campus_resources", err)
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendInferredTool(tools, "read_campus_resource", "Read one exact MCP resource URI returned by list_campus_resources, such as life-ustc://graphql/schema. Return the resource's literal contents.", func(ctx context.Context, input campusResourceReadInput) (string, error) {
		uri := strings.TrimSpace(input.URI)
		if uri == "" {
			return "", botmcp.NewRecoverableToolError("read_campus_resource", "uri is required")
		}
		if err := s.ensure(ctx); err != nil {
			return "", normalizeCampusSetupError(ctx, "read_campus_resource", err)
		}
		result, err := s.session.ReadResource(ctx, uri)
		if err != nil {
			return "", normalizeCampusReadCallError(ctx, "read_campus_resource", err)
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	tools, err = appendInferredTool(tools, "list_campus_prompts", "List MCP prompts available for planning campus operations. Use plan_graphql_operation when constructing GraphQL arguments.", func(ctx context.Context, _ emptyInput) (string, error) {
		if err := s.ensure(ctx); err != nil {
			return "", normalizeCampusSetupError(ctx, "list_campus_prompts", err)
		}
		result, err := s.session.Prompts(ctx)
		if err != nil {
			return "", normalizeCampusReadCallError(ctx, "list_campus_prompts", err)
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return appendInferredTool(tools, "get_campus_prompt", "Render one exact MCP prompt returned by list_campus_prompts. Use it to construct a valid GraphQL operation before calling graphql_operation_run.", func(ctx context.Context, input campusPromptGetInput) (string, error) {
		name := strings.TrimSpace(input.Name)
		if name == "" {
			return "", botmcp.NewRecoverableToolError("get_campus_prompt", "name is required")
		}
		if err := s.ensure(ctx); err != nil {
			return "", normalizeCampusSetupError(ctx, "get_campus_prompt", err)
		}
		result, err := s.session.GetPrompt(ctx, name, input.Arguments)
		if err != nil {
			return "", normalizeCampusReadCallError(ctx, "get_campus_prompt", err)
		}
		return result, nil
	})
}

func (s *lazyMCPSession) ensure(ctx context.Context) error {
	s.once.Do(func() {
		for attempt := 0; attempt < 3; attempt++ {
			s.err = s.initialize(ctx)
			if s.err == nil || ctx.Err() != nil || isMCPAuthorizationError(s.err) {
				return
			}
			if attempt < 2 && !retry.Wait(ctx, time.Duration(attempt+1)*200*time.Millisecond) {
				s.err = ctx.Err()
				return
			}
		}
	})
	return s.err
}

func (s *lazyMCPSession) initialize(ctx context.Context) error {
	if s.service == nil || s.service.mcpClient == nil || s.service.auth == nil {
		return errors.New("campus tool service is unavailable")
	}
	token, err := s.service.auth.MCPAccessToken(ctx, s.identity)
	if errors.Is(err, auth.ErrNotLoggedIn) {
		token, err = "", nil
	}
	if err != nil {
		return fmt.Errorf("get MCP access token: %w", err)
	}
	session, err := s.service.mcpClient.OpenSession(ctx, token)
	if err != nil {
		return err
	}
	cacheKey := campusCatalogCacheKey(token)
	if cached, found := s.service.campusCatalog.get(cacheKey); found {
		s.session = session
		s.tools = campusToolsByName(cached)
		return nil
	}
	listed, err := session.Tools(ctx)
	if err != nil {
		_ = session.Close()
		return err
	}
	available, err := campusValidatedTools(listed)
	if err != nil {
		_ = session.Close()
		return err
	}
	s.service.campusCatalog.put(cacheKey, listed)
	s.session = session
	s.tools = available
	return nil
}

// campusValidatedTools rebuilds the tools/list response into a lookup map,
// rejecting a catalog the server should never send: an empty or duplicate name
// would make search_campus_tools and call_campus_tool ambiguous about which
// tool they mean.
func campusValidatedTools(listed []mcpgo.Tool) (map[string]mcpgo.Tool, error) {
	if len(listed) == 0 {
		return nil, errors.New("MCP tools/list returned no tools")
	}
	available := make(map[string]mcpgo.Tool, len(listed))
	for _, candidate := range listed {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			return nil, errors.New("MCP tools/list returned a tool with an empty name")
		}
		candidate.Name = name
		if _, duplicate := available[name]; duplicate {
			return nil, fmt.Errorf("MCP tools/list returned duplicate tool name %q", name)
		}
		available[name] = candidate
	}
	return available, nil
}

// campusToolsByName rebuilds the lookup map from a cached catalog. The catalog
// was already validated (non-empty, unique names) the first time it was
// fetched, so this cannot fail.
func campusToolsByName(cached []mcpgo.Tool) map[string]mcpgo.Tool {
	available := make(map[string]mcpgo.Tool, len(cached))
	for _, candidate := range cached {
		available[candidate.Name] = candidate
	}
	return available
}

func (s *lazyMCPSession) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
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

func campusToolOutputSchema(candidate mcpgo.Tool) (json.RawMessage, error) {
	if len(candidate.RawOutputSchema) > 0 {
		if !json.Valid(candidate.RawOutputSchema) {
			return nil, fmt.Errorf("campus tool %s has an invalid output schema", candidate.Name)
		}
		return append(json.RawMessage(nil), candidate.RawOutputSchema...), nil
	}
	if strings.TrimSpace(candidate.OutputSchema.Type) == "" {
		return nil, nil
	}
	data, err := json.Marshal(candidate.OutputSchema)
	if err != nil {
		return nil, fmt.Errorf("encode campus tool %s output schema: %w", candidate.Name, err)
	}
	return data, nil
}

func campusEffectOf(candidate mcpgo.Tool) campusToolEffect {
	if candidate.Annotations.ReadOnlyHint != nil && *candidate.Annotations.ReadOnlyHint {
		return campusEffectRead
	}
	if candidate.Annotations.DestructiveHint != nil && *candidate.Annotations.DestructiveHint {
		return campusEffectDestructive
	}
	if candidate.Annotations.ReadOnlyHint != nil && !*candidate.Annotations.ReadOnlyHint &&
		candidate.Annotations.DestructiveHint != nil && !*candidate.Annotations.DestructiveHint {
		return campusEffectWrite
	}
	return campusEffectDestructive
}

func (s *lazyMCPSession) search(ctx context.Context, input campusToolSearchInput) (string, error) {
	query := strings.ToLower(strings.TrimSpace(input.Query))
	if query == "" {
		return "", botmcp.NewRecoverableToolError("search_campus_tools", "query is required")
	}
	if err := s.ensure(ctx); err != nil {
		return "", normalizeCampusSetupError(ctx, "search_campus_tools", err)
	}
	tokens := strings.Fields(query)
	listAll := query == "*"
	type match struct {
		name  string
		score int
	}
	matches := make([]match, 0, len(s.tools))
	for name, candidate := range s.tools {
		schemaData, _ := json.Marshal(candidate)
		haystack := strings.ToLower(name + " " + candidate.Title + " " + candidate.Description + " " + string(schemaData) + " " + campusToolSearchAliases(name))
		score := 0
		if listAll {
			score = 1
		} else {
			for _, token := range tokens {
				score += textutil.MeaningfulSearchTokenMatches(token, haystack)
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
	if !listAll && len(matches) > maxCampusToolSearchResults {
		matches = matches[:maxCampusToolSearchResults]
	}
	docs := make([]campusToolDocumentation, 0, len(matches))
	for _, matched := range matches {
		candidate := s.tools[matched.name]
		schemaJSON, err := campusToolInputSchema(candidate)
		if err != nil {
			return "", err
		}
		outputSchema, err := campusToolOutputSchema(candidate)
		if err != nil {
			return "", err
		}
		docs = append(docs, campusToolDocumentation{
			Name: candidate.Name, Title: candidate.Title, Description: candidate.Description,
			InputSchema: schemaJSON, OutputSchema: outputSchema,
			Annotations: candidate.Annotations, Effect: campusEffectOf(candidate),
		})
	}
	data, err := json.Marshal(docs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func campusToolSearchAliases(name string) string {
	lower := strings.ToLower(name)
	aliases := make([]string, 0, 8)
	add := func(values ...string) { aliases = append(aliases, values...) }
	if strings.Contains(lower, "young") || strings.Contains(lower, "event") {
		add("第二课堂", "二课", "活动", "报名", "项目")
	}
	if strings.Contains(lower, "room") || strings.Contains(lower, "map") || strings.Contains(lower, "building") {
		add("教室", "房间", "地图", "位置", "楼层", "room map")
	}
	if strings.Contains(lower, "location") || strings.Contains(lower, "place") || strings.Contains(lower, "geo") {
		add("地点", "位置", "在哪里", "校区")
	}
	if strings.Contains(lower, "weather") || strings.Contains(lower, "forecast") {
		add("天气", "气温", "温度", "降雨", "预报", "下雨", "本部", "主校区", "高新", "高新区", "高新校区")
	}
	if strings.Contains(lower, "semester") || strings.Contains(lower, "term") {
		add("学期", "当前学期", "学年")
	}
	if strings.Contains(lower, "homework") || strings.Contains(lower, "assignment") {
		add("作业", "作业截止", "提交作业")
	}
	if strings.Contains(lower, "course") || strings.Contains(lower, "section") || strings.Contains(lower, "class") {
		add("课程", "课", "选课", "班级", "节次", "教务")
	}
	if strings.Contains(lower, "academic") || strings.Contains(lower, "curriculum") || strings.Contains(lower, "major") || strings.Contains(lower, "department") {
		add("教务", "培养方案", "专业", "学院")
	}
	if strings.Contains(lower, "enroll") || strings.Contains(lower, "registration") || strings.Contains(lower, "selection") {
		add("选课", "选课结果", "教学班", "报名")
	}
	if strings.Contains(lower, "teacher") || strings.Contains(lower, "instructor") {
		add("教师", "老师", "任课老师")
	}
	if strings.Contains(lower, "exam") || strings.Contains(lower, "test") {
		add("考试", "考场", "成绩")
	}
	if strings.Contains(lower, "grade") || strings.Contains(lower, "score") || strings.Contains(lower, "transcript") {
		add("成绩", "分数", "绩点", "成绩单")
	}
	if strings.Contains(lower, "deadline") || strings.Contains(lower, "due") {
		add("截止", "截止时间", "ddl")
	}
	if strings.Contains(lower, "schedule") || strings.Contains(lower, "calendar") {
		add("课表", "日程", "日历", "上课")
	}
	if strings.Contains(lower, "bus") || strings.Contains(lower, "route") {
		add("校车", "班车", "公交", "路线", "发车")
	}
	if strings.Contains(lower, "transport") || strings.Contains(lower, "shuttle") {
		add("交通", "校车", "班车", "路线")
	}
	if strings.Contains(lower, "library") || strings.Contains(lower, "book") {
		add("图书馆", "图书", "借阅", "馆藏")
	}
	if strings.Contains(lower, "canteen") || strings.Contains(lower, "restaurant") || strings.Contains(lower, "food") || strings.Contains(lower, "menu") {
		add("食堂", "餐厅", "吃饭", "菜单")
	}
	if strings.Contains(lower, "holiday") || strings.Contains(lower, "vacation") {
		add("假期", "放假", "节假日")
	}
	if strings.Contains(lower, "announcement") || strings.Contains(lower, "notice") {
		add("公告", "通知", "消息")
	}
	if strings.Contains(lower, "subscription") || strings.Contains(lower, "subscribe") {
		add("订阅", "关注", "提醒")
	}
	if strings.Contains(lower, "todo") || strings.Contains(lower, "task") {
		add("待办", "任务", "提醒")
	}
	if strings.Contains(lower, "account") || strings.Contains(lower, "profile") || strings.Contains(lower, "user") {
		add("账户", "账号", "个人信息")
	}
	if strings.Contains(lower, "student") || strings.Contains(lower, "personal") || strings.Contains(lower, "me_") {
		add("学生", "个人", "我的")
	}
	if strings.Contains(lower, "graphql") || strings.Contains(lower, "operation") {
		add("GraphQL", "数据", "教务", "查询", "操作")
	}
	if strings.Contains(lower, "upload") || strings.Contains(lower, "file") {
		add("上传", "文件", "附件")
	}
	if strings.Contains(lower, "comment") || strings.Contains(lower, "feedback") {
		add("评论", "反馈", "建议")
	}
	if strings.Contains(lower, "setting") || strings.Contains(lower, "preference") {
		add("设置", "偏好", "配置")
	}
	if strings.Contains(lower, "notification") || strings.Contains(lower, "remind") {
		add("通知", "提醒", "消息")
	}
	return strings.Join(aliases, " ")
}

func (s *lazyMCPSession) deliverRoomMapResponse(ctx context.Context, result string) error {
	if s == nil || s.sendResponse == nil {
		return nil
	}
	var room life.RoomMap
	if err := json.Unmarshal([]byte(result), &room); err != nil {
		return fmt.Errorf("decode catalog_rooms_map result: %w", err)
	}
	response := commands.RoomMapResponse(room)
	return s.sendResponse(ctx, s.identity, response)
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
