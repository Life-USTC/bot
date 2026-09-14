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
	action  string
	page    int
	search  string
	youngID string
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
		if len(remaining) != 0 {
			return youngEventQuery{}, errors.New("第二课堂列表用法：第二课堂 列表 [页码]")
		}
		query.page = page
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

	page, err := h.Life.ListYoungEvents(ctx, query.page, youngEventPageSize, query.search)
	if err != nil {
		return h.commandError("第二课堂查不到：", err)
	}
	if len(page.Data) == 0 {
		if query.search != "" {
			return h.notFound("没找到相关第二课堂活动：" + query.search)
		}
		return h.notFound("没有第二课堂活动。")
	}
	h.markData(map[string]any{
		"operation": "list",
		"page":      query.page,
		"search":    query.search,
		"result":    page,
	})
	return formatYoungEventPage(page, query, h.Life.YoungEventURL)
}

func youngEventIsNotFound(err error) bool {
	var httpErr life.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

func formatYoungEventPage(page life.YoungEventPage, query youngEventQuery, eventURL func(string) string) string {
	lines := []string{"第二课堂："}
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
		command := "第二课堂 列表"
		if query.search != "" {
			command = "第二课堂 搜索 " + query.search
		}
		navigation := []string{fmt.Sprintf("第 %d/%d 页", pageNumber, totalPages)}
		if pageNumber > 1 {
			navigation = append(navigation, "上一页：发送「"+command+" 第"+strconv.Itoa(pageNumber-1)+"页」")
		}
		if pageNumber < totalPages {
			navigation = append(navigation, "下一页：发送「"+command+" 第"+strconv.Itoa(pageNumber+1)+"页」")
		}
		lines = append(lines, textutil.MonospaceDigits(strings.Join(navigation, " · ")))
	}
	return strings.Join(lines, "\n")
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
	}
	if signupTime := youngEventTimeRange(event.ApplyStartAt, event.ApplyEndAt); signupTime != "" {
		lines = append(lines, "报名时间："+signupTime)
	}
	if link = strings.TrimSpace(link); link != "" {
		lines = append(lines, "链接："+link)
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
