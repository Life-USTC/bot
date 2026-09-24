package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const youngEventPageSize = 10

type youngEventQuery struct {
	action      string
	page        int
	search      string
	youngID     string
	organizerID string
	dateFrom    string
	dateTo      string
	timeBasis   string
	dateUnknown *bool
}

func youngEventArgsAcceptable(args []string) bool {
	if !hasArgs(args) || firstArgIsHelp(args) {
		return len(args) <= 1
	}
	_, err := parseYoungEventQuery(args)
	return err == nil
}

func parseYoungEventQuery(args []string) (youngEventQuery, error) {
	query := youngEventQuery{action: "list", page: 1}
	if len(args) == 0 {
		return query, nil
	}

	action := normToken(args[0])
	rest := args[1:]
	switch action {
	case "列表", "list":
		query.action = "list"
		if len(rest) == 0 {
			return query, nil
		}
		if len(rest) == 1 {
			if page, ok := youngEventPageNumber(rest[0]); ok {
				query.page = page
				return query, nil
			}
		}
		remaining, page, err := extractListPage(rest)
		if err != nil {
			return youngEventQuery{}, err
		}
		query.page = page
		if err := parseYoungEventFilters(&query, remaining); err != nil {
			return youngEventQuery{}, err
		}
		return query, nil
	case "搜索", "search":
		query.action = "search"
		remaining, page, err := extractListPage(rest)
		if err != nil {
			return youngEventQuery{}, err
		}
		query.search = strings.TrimSpace(joinedArgs(remaining))
		if query.search == "" {
			return youngEventQuery{}, errors.New("想搜索什么活动？例如：第二课堂 搜索 志愿")
		}
		query.page = page
		return query, nil
	case "查看", "详情", "view":
		query.action = "detail"
		if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
			return youngEventQuery{}, errors.New("需要提供第二课堂活动 youngId，例如：第二课堂 查看 event-1")
		}
		query.youngID = strings.TrimSpace(rest[0])
		return query, nil
	default:
		return youngEventQuery{}, errors.New("第二课堂命令用法：第二课堂、第二课堂 列表 [页码]、第二课堂 搜索 <关键词>、第二课堂 查看 <youngId>")
	}
}

func youngEventPageNumber(value string) (int, bool) {
	parsed, err := strconv.Atoi(textutil.PlainDigits(strings.TrimSpace(value)))
	return parsed, err == nil && parsed > 0
}

func (h Handler) youngEvents(ctx context.Context, args []string) string {
	query, err := parseYoungEventQuery(args)
	if err != nil {
		return h.invalidInput(err.Error())
	}

	if query.action == "detail" {
		event, err := h.Life.GetYoungEvent(ctx, query.youngID)
		if err != nil {
			if youngEventIsNotFound(err) {
				return h.notFound("没找到第二课堂活动：" + query.youngID)
			}
			return h.commandError("第二课堂活动查不到：", err)
		}
		if strings.TrimSpace(event.Name) == "" && strings.TrimSpace(event.YoungID) == "" {
			return h.notFound("没找到第二课堂活动：" + query.youngID)
		}
		h.markData(map[string]any{
			"operation": "detail",
			"event":     event,
		})
		return strings.Join(youngEventLines(event, h.Life.YoungEventURL(event.YoungID), ""), "\n")
	}

	page, err := h.Life.ListYoungEventsWithQuery(ctx, "", life.YoungEventQuery{
		Page:        query.page,
		PageSize:    youngEventPageSize,
		Search:      query.search,
		OrganizerID: query.organizerID,
		DateFrom:    query.dateFrom,
		DateTo:      query.dateTo,
		TimeBasis:   query.timeBasis,
		DateUnknown: query.dateUnknown,
	})
	if err != nil {
		return h.commandError("第二课堂查不到：", err)
	}
	h.markData(map[string]any{
		"operation": "list",
		"page":      query.page,
		"search":    query.search,
		"result":    page,
	})
	if len(page.Data) == 0 && page.UnknownDateCount == 0 {
		if query.search != "" {
			return h.notFound("没找到相关第二课堂活动：" + query.search)
		}
		return h.notFound("没有第二课堂活动。")
	}
	return formatYoungEventPage(page, query, h.Life.YoungEventURL)
}

func youngEventIsNotFound(err error) bool {
	var httpErr life.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

func formatYoungEventPage(page life.YoungEventPage, query youngEventQuery, eventURL func(string) string) string {
	lines := []string{"第二课堂："}
	if source := formatYoungSource(page.Source); source != "" {
		lines = append(lines, source)
	}
	for i, event := range page.Data {
		link := ""
		if eventURL != nil {
			link = eventURL(event.YoungID)
		}
		lines = append(lines, youngEventLines(event, link, strconv.Itoa(i+1))...)
	}

	pageNumber := page.Pagination.Page
	if pageNumber < 1 {
		pageNumber = query.page
	}
	totalPages := page.Pagination.TotalPages
	if totalPages < 1 && page.Pagination.Total > 0 {
		totalPages = (page.Pagination.Total + youngEventPageSize - 1) / youngEventPageSize
	}
	if totalPages > 1 {
		command := youngEventListCommand(query)
		navigation := []string{fmt.Sprintf("第 %d/%d 页", pageNumber, totalPages)}
		if pageNumber > 1 {
			navigation = append(navigation, "上一页：发送「"+command+" 第"+strconv.Itoa(pageNumber-1)+"页」")
		}
		if pageNumber < totalPages {
			navigation = append(navigation, "下一页：发送「"+command+" 第"+strconv.Itoa(pageNumber+1)+"页」")
		}
		lines = append(lines, textutil.MonospaceDigits(strings.Join(navigation, " · ")))
	}
	if page.UnknownDateCount > 0 {
		lines = append(lines, fmt.Sprintf("日期未知总数：%d · 发送「第二课堂 列表 日期未知」查看。", page.UnknownDateCount))
	}
	return strings.Join(lines, "\n")
}

func parseYoungEventFilters(query *youngEventQuery, args []string) error {
	for i := 0; i < len(args); {
		raw := strings.TrimSpace(args[i])
		if raw == "" {
			i++
			continue
		}
		field, value, hasValue := strings.Cut(raw, "=")
		if !hasValue {
			field = raw
			if normToken(field) == "日期未知" || normToken(field) == "dateunknown" {
				unknown := true
				query.dateUnknown = &unknown
				i++
				continue
			}
			switch normToken(field) {
			case "活动", "活动时间", "activity":
				query.timeBasis = "activity"
				i++
				continue
			case "报名", "报名时间", "registration":
				query.timeBasis = "registration"
				i++
				continue
			}
			if i+1 >= len(args) {
				return errors.New("第二课堂筛选用法：主办方 <organizerId>、日期从 <YYYY-MM-DD>、日期到 <YYYY-MM-DD>、活动时间|报名时间、日期未知")
			}
			value = strings.TrimSpace(args[i+1])
			i += 2
		} else {
			field, value = strings.TrimSpace(field), strings.TrimSpace(value)
			i++
		}
		if value == "" {
			return errors.New("第二课堂筛选值不能为空")
		}
		switch normToken(field) {
		case "主办方", "organizer", "organizerid", "组织":
			query.organizerID = value
		case "日期从", "从", "datefrom":
			if !youngEventDateFilter(value) {
				return errors.New("日期从必须是 YYYY-MM-DD")
			}
			query.dateFrom = value
		case "日期到", "到", "dateto":
			if !youngEventDateFilter(value) {
				return errors.New("日期到必须是 YYYY-MM-DD")
			}
			query.dateTo = value
		case "timebasis", "时间基准", "时间依据":
			basis := normToken(value)
			switch basis {
			case "活动", "活动时间", "activity":
				query.timeBasis = "activity"
			case "报名", "报名时间", "registration":
				query.timeBasis = "registration"
			default:
				return errors.New("时间依据请使用活动时间或报名时间")
			}
		case "dateunknown", "日期未知":
			unknown, ok := parseYoungSubscriptionBool(value)
			if !ok {
				return errors.New("日期未知请使用 true 或 false")
			}
			query.dateUnknown = &unknown
		default:
			return errors.New("未知第二课堂筛选项：" + field)
		}
	}
	if query.dateFrom != "" && query.dateTo != "" && query.dateTo < query.dateFrom {
		return errors.New("日期到不能早于日期从")
	}
	if query.dateUnknown != nil && *query.dateUnknown && (query.dateFrom != "" || query.dateTo != "") {
		return errors.New("日期未知不能同时使用日期范围")
	}
	return nil
}

func youngEventDateFilter(value string) bool {
	_, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	return err == nil
}

func youngEventListCommand(query youngEventQuery) string {
	command := "第二课堂 列表"
	if query.search != "" {
		command = "第二课堂 搜索 " + query.search
	}
	filters := make([]string, 0, 5)
	if query.organizerID != "" {
		filters = append(filters, "organizerId="+query.organizerID)
	}
	if query.dateFrom != "" {
		filters = append(filters, "dateFrom="+query.dateFrom)
	}
	if query.dateTo != "" {
		filters = append(filters, "dateTo="+query.dateTo)
	}
	if query.timeBasis != "" {
		filters = append(filters, "timeBasis="+query.timeBasis)
	}
	if query.dateUnknown != nil && *query.dateUnknown {
		filters = append(filters, "dateUnknown=true")
	}
	if len(filters) > 0 {
		command += " " + strings.Join(filters, " ")
	}
	return command
}
func youngEventLines(event life.YoungEvent, link, prefix string) []string {
	name := strings.TrimSpace(event.Name)
	if name == "" {
		name = "未命名活动"
	}
	if prefix != "" {
		name = prefix + ". " + name
	} else {
		name = "第二课堂活动：" + name
	}
	lines := []string{name}
	if youngID := strings.TrimSpace(event.YoungID); youngID != "" {
		lines = append(lines, "youngId："+youngID)
	}
	if location := strings.TrimSpace(stringValue(event.Location)); location != "" {
		lines = append(lines, "地点："+location)
	}
	if eventTime := youngEventTimeRange(event.StartAt, event.EndAt); eventTime != "" {
		lines = append(lines, "活动时间："+eventTime)
	} else if event.DateUnknown || (event.StartAt == nil && event.EndAt == nil) {
		lines = append(lines, "活动时间：待核实")
	}
	if signupTime := youngEventTimeRange(event.ApplyStartAt, event.ApplyEndAt); signupTime != "" {
		lines = append(lines, "报名时间："+signupTime)
	}
	if event.RequiresSignup != nil {
		if *event.RequiresSignup {
			lines = append(lines, "报名要求：需要报名")
		} else {
			lines = append(lines, "报名要求：无需报名")
		}
	}
	if event.IsOnline != nil && *event.IsOnline {
		lines = append(lines, "参与方式：线上活动")
	}
	if prefix == "" {
		if event.RequiresSignupInfo != nil && *event.RequiresSignupInfo {
			lines = append(lines, "报名时需填写补充信息")
		}
		if event.Hours != nil {
			lines = append(lines, fmt.Sprintf("学时：%g", *event.Hours))
		}
		if event.AppliedCount != nil {
			lines = append(lines, fmt.Sprintf("已报名：%d", *event.AppliedCount))
		}
		if event.Capacity != nil {
			lines = append(lines, fmt.Sprintf("名额：%d", *event.Capacity))
		}
		for _, field := range []struct{ label, value string }{
			{"活动级别", event.ActivityLevel}, {"模块", event.Module}, {"参与形式", event.Form},
			{"面向年级", event.Grades}, {"主办单位", event.Sponsor}, {"主办方", event.Organizer},
			{"校外主办方", event.ExternalSponsor}, {"联系人", event.ContactName}, {"联系电话", event.ContactTel},
		} {
			if value := strings.TrimSpace(field.value); value != "" {
				lines = append(lines, field.label+"："+value)
			}
		}
		if event.IsOnline == nil || *event.IsOnline {
			if event.OnlineMeetingInfo != "" {
				lines = append(lines, "线上会议信息："+event.OnlineMeetingInfo)
			}
		}
		if len(event.AllowedAttachmentTypes) > 0 {
			lines = append(lines, "附件格式："+strings.ToUpper(strings.Join(event.AllowedAttachmentTypes, ", ")))
		}
		if event.SignupScopeCode != "" || len(event.SignupDepartmentIds) > 0 {
			lines = append(lines, "报名资格与面向范围请以第二课堂平台为准。")
		}
	}
	if link = strings.TrimSpace(link); link != "" {
		lines = append(lines, "链接："+link)
	}
	if event.SourceMissing {
		lines = append(lines, "数据源：暂缺，活动状态待核实")
	}
	return lines
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func youngEventTimeRange(start, end *time.Time) string {
	startText := youngEventTime(start)
	endText := youngEventTime(end)
	switch {
	case startText != "" && endText != "":
		return startText + " ~ " + endText
	case startText != "":
		return startText
	default:
		return endText
	}
}

func youngEventTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.In(lifedata.ChinaLocation()).Format("2006-01-02 15:04")
}
