package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Life-USTC/Bot/internal/lifedata"
	"github.com/Life-USTC/Bot/internal/store"
)

func (p *Poller) notifyTodos(ctx context.Context, ident store.Identity, todos []map[string]any, now time.Time) error {
	var errs []error
	for _, todo := range todos {
		id := lifedata.FirstString(todo, "id")
		completed, _ := todo["completed"].(bool)
		due, ok := lifedata.ParseAPITime(lifedata.FirstString(todo, "dueAt"))
		if id == "" || completed || !ok || !due.After(now) || due.After(now.Add(24*time.Hour)) {
			continue
		}
		title := lifedata.FirstString(todo, "title")
		text := fmt.Sprintf("待办提醒：\n%s\n截止 %s", title, lifedata.FormatAPITime(lifedata.FirstString(todo, "dueAt")))
		card := reminderTableImage("todo_reminder", "待办提醒", []string{"截止", "待办"}, []string{lifedata.FormatAPITime(lifedata.FirstString(todo, "dueAt")), title}, text)
		key := notificationKey(todoKind, id+"|"+due.UTC().Format(time.RFC3339))
		if _, err := p.enqueueNotification(ctx, ident, todoKind, key, text, card, due); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
