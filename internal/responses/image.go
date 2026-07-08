package responses

import "strings"

type Image struct {
	Kind    string
	Title   string
	Lines   []string
	AltText string
	URL     string
}

func NewTextImage(kind, title, text string) *Image {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return &Image{
		Kind:    strings.TrimSpace(kind),
		Title:   strings.TrimSpace(title),
		Lines:   lines,
		AltText: text,
	}
}

func (img *Image) WithURL(url string) *Image {
	if img == nil {
		return nil
	}
	next := *img
	next.URL = strings.TrimSpace(url)
	return &next
}
