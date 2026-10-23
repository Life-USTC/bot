package store

import (
	"errors"
	"strings"
	"time"
	_ "time/tzdata"
)

// RecurringReminderSpec is a local-clock rule, independent of campus data and
// OAuth notification settings. EndDate includes that date in Timezone.
type RecurringReminderSpec struct {
	Text      string       `json:"text"`
	Frequency string       `json:"frequency"`
	Weekday   time.Weekday `json:"weekday"`
	TimeOfDay string       `json:"timeOfDay"`
	Timezone  string       `json:"timezone"`
	EndDate   string       `json:"endDate"`
}

func (spec RecurringReminderSpec) Validate() error {
	_, err := normalizeRecurringReminderSpec(spec)
	return err
}

func normalizeRecurringReminderSpec(spec RecurringReminderSpec) (RecurringReminderSpec, error) {
	spec.Text = strings.TrimSpace(spec.Text)
	spec.Timezone = strings.TrimSpace(spec.Timezone)
	if spec.Timezone == "" {
		spec.Timezone = "Asia/Shanghai"
	}
	if spec.Text == "" {
		return spec, errors.New("提醒内容不能为空")
	}
	if spec.Frequency != "daily" && spec.Frequency != "weekly" {
		return spec, errors.New("提醒频率只能是每天或每周")
	}
	if spec.Frequency == "weekly" && (spec.Weekday < time.Sunday || spec.Weekday > time.Saturday) {
		return spec, errors.New("每周提醒需要有效的星期几")
	}
	if spec.Frequency == "daily" {
		spec.Weekday = time.Sunday
	}
	if _, err := time.Parse("15:04", spec.TimeOfDay); err != nil || len(spec.TimeOfDay) != 5 {
		return spec, errors.New("提醒时间请使用 HH:MM，例如 09:00")
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil || spec.Timezone == "Local" {
		return spec, errors.New("请使用有效的 IANA 时区，例如 Asia/Shanghai")
	}
	if spec.EndDate != "" {
		if _, err := time.Parse(time.DateOnly, spec.EndDate); err != nil {
			return spec, errors.New("截止日期请使用有效的 YYYY-MM-DD 日期")
		}
	}
	return spec, nil
}

// nextRecurringReminderAt skips nonexistent local times and selects the first
// instance of a repeated clock time. Advancing after that instance cannot send
// the same local occurrence again during a daylight-saving fold.
func nextRecurringReminderAt(spec RecurringReminderSpec, after time.Time) (*time.Time, error) {
	location, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return nil, err
	}
	clock, err := time.Parse("15:04", spec.TimeOfDay)
	if err != nil {
		return nil, err
	}
	local := after.In(location)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for ; ; date = date.AddDate(0, 0, 1) {
		if spec.EndDate != "" && date.Format(time.DateOnly) > spec.EndDate {
			return nil, nil
		}
		if spec.Frequency == "weekly" && date.Weekday() != spec.Weekday {
			continue
		}
		candidate := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
		matches := func(value time.Time) bool {
			value = value.In(location)
			return value.Year() == date.Year() && value.Month() == date.Month() && value.Day() == date.Day() && value.Hour() == clock.Hour() && value.Minute() == clock.Minute()
		}
		if !matches(candidate) {
			continue
		}
		wall := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, time.UTC)
		for _, nearby := range []time.Time{candidate.Add(-24 * time.Hour), candidate.Add(24 * time.Hour)} {
			_, offset := nearby.Zone()
			alternative := wall.Add(-time.Duration(offset) * time.Second)
			if alternative.Before(candidate) && matches(alternative) {
				candidate = alternative
			}
		}
		if candidate.After(after) {
			utc := candidate.UTC()
			return &utc, nil
		}
	}
}
