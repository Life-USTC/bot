package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
	"golang.org/x/text/unicode/norm"
)

var academicSectionActions = map[string]string{"课表": "section_schedules", "考试": "section_exams", "作业": "section_homeworks", "查看": "section_by_jw_id", "详情": "section_by_jw_id"}

type academicQuery struct {
	Target   string
	Code     bool
	Semester string
	From     string
	To       string
}

func parseAcademicQuery(args []string, dates bool) (academicQuery, bool) {
	q := academicQuery{}
	var target []string
	for i := 0; i < len(args); i++ {
		value := args[i]
		if value == "学期" || value == "semester" {
			i++
			if i >= len(args) || q.Semester != "" {
				return q, false
			}
			q.Semester = args[i]
		} else if strings.HasPrefix(value, "semester=") || strings.HasPrefix(value, "学期=") {
			if q.Semester != "" {
				return q, false
			}
			q.Semester = strings.SplitN(value, "=", 2)[1]
			if q.Semester == "" {
				return q, false
			}
		} else {
			target = append(target, value)
		}
	}
	if dates && len(target) >= 3 {
		from, fromOK := parseScheduleDateToken(target[len(target)-2], time.Now())
		to, toOK := parseScheduleDateToken(target[len(target)-1], time.Now())
		if fromOK || toOK {
			if !fromOK || !toOK || to.Before(from) {
				return q, false
			}
			q.From, _ = lifedata.DayRFC3339Range(from)
			_, q.To = lifedata.DayRFC3339Range(to)
			target = target[:len(target)-2]
		}
	}
	q.Target = strings.Trim(strings.TrimSpace(strings.Join(target, " ")), "<>〈〉")
	for _, prefix := range []string{"code:", "课程编号:", "教学班编号:"} {
		if strings.HasPrefix(q.Target, prefix) {
			q.Code = true
			q.Target = strings.TrimSpace(strings.TrimPrefix(q.Target, prefix))
			break
		}
	}
	return q, q.Target != ""
}

func academicTargetArgs(args []string) bool {
	_, ok := parseAcademicQuery(args, false)
	return ok
}

// Leading-zero identifiers are course codes, not decimal JW IDs.
func academicJWID(target string) (int64, bool) {
	if strings.HasPrefix(target, "jw:") {
		return parseIntArg(strings.TrimPrefix(target, "jw:"))
	}
	if strings.HasPrefix(target, "0") {
		return 0, false
	}
	return parseIntArg(target)
}

func academicKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(norm.NFKC.String(textutil.PlainMonospace(value))), ""))
}

func academicMatch(item map[string]any, target string, course bool) int {
	key := academicKey(target)
	object := item
	if !course {
		if academicKey(lifedata.FirstString(item, "code")) == key {
			return 2
		}
		object, _ = item["course"].(map[string]any)
	}
	if object == nil {
		return 0
	}
	if academicKey(lifedata.FirstString(object, "code")) == key {
		return 2
	}
	for _, field := range []string{"namePrimary", "nameCn", "nameEn", "name"} {
		name := academicKey(lifedata.FirstString(object, field))
		if name != "" && name == key {
			return 2
		}
	}
	for _, field := range []string{"namePrimary", "nameCn", "nameEn", "name"} {
		name := academicKey(lifedata.FirstString(object, field))
		if name != "" && strings.Contains(name, key) {
			return 1
		}
	}
	return 0
}

func rankedAcademicMatches(items []map[string]any, target string, course bool) []map[string]any {
	best := 0
	var matches []map[string]any
	seen := map[string]bool{}
	for _, item := range items {
		rank := academicMatch(item, target, course)
		if rank == 0 || rank < best {
			continue
		}
		if rank > best {
			matches = nil
			seen = map[string]bool{}
			best = rank
		}
		key := lifedata.FirstString(item, "jwId")
		if key == "" {
			key = lifedata.FirstString(item, "id", "code")
		}
		if !seen[key] {
			seen[key] = true
			matches = append(matches, item)
		}
	}
	return matches
}

func (h Handler) academicSemester(ctx context.Context, raw string) (map[string]any, string) {
	if raw == "" {
		semester, err := h.Life.CurrentSemester(ctx)
		if err != nil {
			return nil, h.commandError("当前学期查不到：", err)
		}
		if lifedata.FirstInt(semester, "id") <= 0 {
			return nil, h.failed("当前学期信息不完整，请指定学期。")
		}
		return semester, ""
	}
	if id, ok := parseIntArg(raw); ok {
		return map[string]any{"id": id}, ""
	}
	normalized, ok := normalizeScheduleSemesterTarget(raw)
	if !ok {
		return nil, h.invalidInput("学期无效，例如：学期 2026秋。")
	}
	semester, err := h.matchScheduleSemester(ctx, normalized)
	if err != nil {
		return nil, h.commandError("学期查不到：", err)
	}
	if semester == nil {
		return nil, h.notFound("没找到指定学期。")
	}
	return semester, ""
}

// resolveAcademicTarget is shared by human commands and model capabilities.
// Personalized selection never goes through a shared public response cache.
func (h Handler) resolveAcademicTarget(ctx context.Context, ident store.Identity, q academicQuery, course bool) (int64, string) {
	if id, ok := academicJWID(q.Target); ok && !q.Code && (q.Semester == "" || course) {
		return id, ""
	}

	if course && strings.Contains(q.Target, ".") && sectionCodeListAcceptable(q.Target) {
		sectionID, reply := h.resolveAcademicTarget(ctx, ident, q, false)
		if reply != "" {
			return 0, reply
		}
		section, err := h.Life.GetSectionByJwID(ctx, sectionID)
		if err != nil {
			return 0, h.commandError("教学班查不到：", err)
		}
		object, _ := section["course"].(map[string]any)
		return h.selectAcademicTarget([]map[string]any{object}, q.Target, true)
	}
	var token string
	var personalized bool
	if !store.IsSharedConversation(ident) {
		token, personalized = h.accessToken(ctx, ident)
	}
	var semesterID int64
	if !course || personalized || q.Semester != "" {
		semester, reply := h.academicSemester(ctx, q.Semester)
		if reply != "" {
			return 0, reply
		}
		semesterID = int64(lifedata.FirstInt(semester, "id"))
	}

	if id, ok := academicJWID(q.Target); ok && !q.Code && !course {
		section, err := h.Life.GetSectionByJwID(ctx, id)
		if err != nil {
			return 0, h.commandError("教学班查不到：", err)
		}
		if !academicSectionInSemester(section, semesterID) {
			return 0, h.notFound("该教学班不属于指定学期。")
		}
		return id, ""
	}
	if personalized {
		sections, err := auth.WithRefresh(ctx, h.Auth, ident, token, func(token string) ([]map[string]any, error) {
			return h.Life.ListSubscribedSections(ctx, token)
		})
		if err != nil {
			return 0, h.commandError("个人订阅查不到：", err)
		}
		var eligible []map[string]any
		for _, section := range sections {
			if !academicSectionInSemester(section, semesterID) {
				continue
			}
			if course {
				if entry, ok := section["course"].(map[string]any); ok {
					eligible = append(eligible, entry)
				}
			} else {
				eligible = append(eligible, section)
			}
		}
		matches := rankedAcademicMatches(eligible, q.Target, course)
		if len(matches) > 0 {
			return h.selectAcademicTarget(matches, q.Target, course)
		}
	}
	var candidates []map[string]any
	var err error
	if course {
		candidates, err = h.Life.CourseCandidates(ctx, q.Target)
	} else {
		candidates, err = h.Life.SectionCandidates(ctx, q.Target, semesterID)
	}
	if err != nil {
		return 0, h.commandError("课程查询失败：", err)
	}
	// Never select a fuzzy public match automatically. Return even one fuzzy
	// candidate for an explicit user choice; exact names/codes may resolve.
	matches := rankedAcademicMatches(candidates, q.Target, course)
	if len(matches) == 1 && academicMatch(matches[0], q.Target, course) == 1 {
		return 0, h.academicCandidates(matches, q.Target, course)
	}
	return h.selectAcademicTarget(matches, q.Target, course)
}

func (h Handler) selectAcademicTarget(matches []map[string]any, target string, course bool) (int64, string) {
	if len(matches) == 0 {
		return 0, h.notFound("没找到“" + target + "”对应的课程或教学班。")
	}
	if len(matches) > 1 {
		return 0, h.academicCandidates(matches, target, course)
	}
	id := int64(lifedata.FirstInt(matches[0], "jwId"))
	if id <= 0 {
		return 0, h.failed("查询结果缺少 JW ID，无法继续查询。")
	}
	return id, ""
}

func (h Handler) academicCandidates(matches []map[string]any, target string, course bool) string {
	h.markData(map[string]any{"operation": "match_candidates", "query": target, "items": matches, "requires_selection": true})
	kind := "教学班"
	if course {
		kind = "课程"
	}
	lines := []string{fmt.Sprintf("请明确选择%s（%d 个候选）：", kind, len(matches))}
	for _, item := range matches {
		label := formatSection(item)
		if course {
			label = formatCourse(item)
		}
		details := []string{label}
		for _, teacher := range lifedata.MapSlice(item["teachers"]) {
			if nested, ok := teacher["teacher"].(map[string]any); ok {
				teacher = nested
			}
			if name := lifedata.FirstString(teacher, "namePrimary", "nameCn", "name"); name != "" {
				details = append(details, name)
			}
		}
		if campus := lifedata.NestedString(item, "campus", "namePrimary", "nameCn", "name"); campus != "" {
			details = append(details, campus)
		}
		details = append(details, "JW ID："+lifedata.FirstString(item, "jwId"))
		lines = append(lines, strings.Join(details, " · "))
	}
	lines = append(lines, "请用候选的 JW ID 重发原查询，例如："+kind+" 查看 "+strconv.Itoa(lifedata.FirstInt(matches[0], "jwId")))
	return strings.Join(lines, "\n")
}

func academicSectionInSemester(section map[string]any, semesterID int64) bool {
	id := int64(lifedata.FirstInt(section, "semesterId"))
	if nested, ok := section["semester"].(map[string]any); ok {
		nestedID := int64(lifedata.FirstInt(nested, "id"))
		if id > 0 && nestedID > 0 && id != nestedID {
			return false
		}
		if nestedID > 0 {
			id = nestedID
		}
	}
	return id > 0 && id == semesterID
}
