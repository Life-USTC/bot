package responses

import "strings"

// NewReminderCardImage builds the small single-row table card the notification
// poller sends for an upcoming class, an approaching deadline, or a todo.
//
// The shape lives here with the other card constructors rather than in the
// poller so the example gallery renders exactly what a user receives, and so a
// reminder card is laid out by the same rich-text path as every other card.
func NewReminderCardImage(kind, title string, headers, cells []string, altText string) *Image {
	separators := make([]string, len(headers))
	for i := range separators {
		separators[i] = "---"
	}
	richText := strings.Join([]string{
		"# " + title,
		"",
		reminderTableRow(headers),
		reminderTableRow(separators),
		reminderTableRow(cells),
	}, "\n")
	return NewRichTextImage(kind, richText, altText)
}

// reminderTableRow renders one Markdown row. A cell's own pipe would split the
// row into extra columns, so it becomes a full-width bar, and its internal
// whitespace collapses to keep the row on one line.
func reminderTableRow(cells []string) string {
	clean := make([]string, len(cells))
	for i, cell := range cells {
		cell = strings.ReplaceAll(cell, "|", "｜")
		clean[i] = strings.Join(strings.Fields(cell), " ")
	}
	return "| " + strings.Join(clean, " | ") + " |"
}
