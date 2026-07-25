package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const maxImageBytes = 10 << 20

var supportedImageTypes = map[string]struct{}{
	"image/gif":  {},
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

func (s *Service) loadImageDataURL(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "data:image/") {
		return rawURL, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("unsupported image URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download image: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	if len(data) > maxImageBytes {
		return "", errors.New("image exceeds 10 MiB")
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mediaType
	}
	if _, ok := supportedImageTypes[contentType]; !ok {
		contentType = http.DetectContentType(data)
	}
	if _, ok := supportedImageTypes[contentType]; !ok {
		return "", fmt.Errorf("unsupported image type %q", contentType)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
