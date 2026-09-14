package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	maxImageBytes         = 10 << 20
	maxImageDownloadBytes = 25 << 20
	maxImagePixels        = 40_000_000
	maxVisionImageSide    = 3072
)

var supportedImageTypes = map[string]struct{}{
	"image/gif":  {},
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

// imageInputError is safe to show to users. Keep transport, decoding internals,
// and upstream response details as ordinary errors so they are only recorded in
// logs and agent_runs.
type imageInputError struct {
	message string
}

func (e *imageInputError) Error() string { return e.message }

func newImageInputError(message string) error {
	return &imageInputError{message: message}
}

func (s *Service) prepareInputImages(ctx context.Context, input *Input) error {
	if input == nil || len(input.ImageURLs) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Input is normally a fresh value for each run, but clearing these slices
	// makes repeated preparation deterministic for focused callers too.
	input.skippedImages = 0
	input.skippedImageErrors = nil
	loadedURLs := make([]string, 0, len(input.ImageURLs))
	dataURLs := make([]string, 0, len(input.ImageURLs))
	var lastErr error
	for _, rawURL := range input.ImageURLs {
		dataURL, err := s.loadImageDataURL(ctx, rawURL)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return err
			}
			var inputErr *imageInputError
			if isAgentBudgetError(err) || errors.As(err, &inputErr) {
				return err
			}
			lastErr = err
			input.skippedImages++
			input.skippedImageErrors = append(input.skippedImageErrors, safeImageSkipReason(err))
			s.logf("input image skipped: reason=%s", safeImageSkipReason(err))
			continue
		}
		loadedURLs = append(loadedURLs, rawURL)
		dataURLs = append(dataURLs, dataURL)
	}
	// Nothing usable is left and there is no text to answer: report the real
	// image failure instead of pretending the turn had no content.
	if len(dataURLs) == 0 && strings.TrimSpace(input.Text) == "" && lastErr != nil {
		return lastErr
	}
	input.ImageURLs = loadedURLs
	input.imageDataURLs = dataURLs
	return nil
}

// safeImageSkipReason keeps transport and provider details, including signed
// image URLs, out of model-visible metadata and logs. HTTP status values are
// useful to the model and contain no response payload.
func safeImageSkipReason(err error) string {
	if err == nil {
		return "图片下载失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "图片下载超时"
	}
	if message := err.Error(); strings.HasPrefix(message, "图片下载失败：HTTP ") {
		return message
	}
	if strings.HasPrefix(err.Error(), "图片读取失败") {
		return "图片读取失败"
	}
	return "图片下载失败"
}

func (s *Service) loadImageDataURL(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "data:image/") {
		data, contentType, err := decodeImageDataURL(rawURL)
		if err != nil {
			return "", err
		}
		return normalizeImageDataURL(data, contentType)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", newImageInputError("不支持的图片地址")
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
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("图片下载超时")
		}
		return "", errors.New("图片下载失败")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("图片下载失败：HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxImageDownloadBytes {
		return "", newImageInputError("图片超过 25 MiB 安全上限")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageDownloadBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", errors.New("图片读取失败")
	}
	if len(data) > maxImageDownloadBytes {
		return "", newImageInputError("图片超过 25 MiB 安全上限")
	}
	contentType := normalizedImageContentType(resp.Header.Get("Content-Type"), data)
	return normalizeImageDataURL(data, contentType)
}

func decodeImageDataURL(rawURL string) ([]byte, string, error) {
	header, encoded, found := strings.Cut(rawURL, ",")
	if !found || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return nil, "", newImageInputError("不支持的图片数据")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxImageDownloadBytes)+4 {
		return nil, "", newImageInputError("图片超过 25 MiB 安全上限")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", newImageInputError("图片数据损坏")
	}
	contentType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	return data, normalizedImageContentType(contentType, data), nil
}

func normalizedImageContentType(contentType string, data []byte) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mediaType
	}
	if _, ok := supportedImageTypes[contentType]; !ok {
		contentType = http.DetectContentType(data)
	}
	return contentType
}

func normalizeImageDataURL(data []byte, contentType string) (string, error) {
	if _, ok := supportedImageTypes[contentType]; !ok {
		return "", newImageInputError(fmt.Sprintf("不支持的图片格式 %q", contentType))
	}
	if len(data) <= maxImageBytes {
		return imageDataURL(data, contentType), nil
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return "", newImageInputError("图片数据损坏，无法压缩")
	}
	if int64(config.Width)*int64(config.Height) > maxImagePixels {
		return "", newImageInputError("图片像素尺寸过大")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", newImageInputError("图片数据损坏，无法压缩")
	}
	compressed, err := compressVisionImage(source)
	if err != nil {
		return "", err
	}
	return imageDataURL(compressed, "image/jpeg"), nil
}

func compressVisionImage(source image.Image) ([]byte, error) {
	for _, maxSide := range []int{maxVisionImageSide, 2560, 2048, 1536} {
		resized := resizeImage(source, maxSide)
		for _, quality := range []int{85, 72, 60} {
			var output bytes.Buffer
			if err := jpeg.Encode(&output, resized, &jpeg.Options{Quality: quality}); err != nil {
				return nil, fmt.Errorf("图片压缩失败：%w", err)
			}
			if output.Len() <= maxImageBytes {
				return output.Bytes(), nil
			}
		}
	}
	return nil, newImageInputError("图片压缩后仍超过 10 MiB")
}

func resizeImage(source image.Image, maxSide int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width > maxSide || height > maxSide {
		if width >= height {
			height = max(1, height*maxSide/width)
			width = maxSide
		} else {
			width = max(1, width*maxSide/height)
			height = maxSide
		}
	}
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	stddraw.Draw(destination, destination.Bounds(), &image.Uniform{C: color.White}, image.Point{}, stddraw.Src)
	xdraw.CatmullRom.Scale(destination, destination.Bounds(), source, bounds, xdraw.Over, nil)
	return destination
}

func imageDataURL(data []byte, contentType string) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
