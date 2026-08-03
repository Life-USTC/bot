package commands

import (
	"strings"
	"testing"
)

func TestResolveImageCommandGluedBusRoute(t *testing.T) {
	got := ResolveImageCommand("校车东西区")
	if got.Directive != "![](校车 查询 东区 西区)" {
		t.Fatalf("directive = %#v", got)
	}
}

func TestResolveImageCommandSpacedBusRoute(t *testing.T) {
	got := ResolveImageCommand("校车 东区 西区")
	if !strings.HasPrefix(got.Directive, "![](校车") {
		t.Fatalf("directive = %#v", got)
	}
}

func TestResolveImageCommandTodaySchedule(t *testing.T) {
	got := ResolveImageCommand("今日课表")
	if got.Directive != "![](今日课表)" && got.Directive != "![](课表 单日 今天)" && got.Directive != "![](今天课表)" {
		t.Fatalf("directive = %#v", got)
	}
}

func TestLookupBotHelpBusTopic(t *testing.T) {
	help := LookupBotHelp("校车")
	if !strings.Contains(help, "校车 查询 东区 西区") {
		t.Fatalf("help = %q", help)
	}
}

func TestNormalizeBotCommandTextExpandsCampuses(t *testing.T) {
	if got := normalizeBotCommandText("校车东西区"); got != "校车 东区 西区" {
		t.Fatalf("got %q", got)
	}
}
