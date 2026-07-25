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

func (s *Service) prepareInputImages(ctx context.Context, input *Input) error {
	if input == nil || len(input.ImageURLs) == 0 {
		return nil
	}
	input.imageDataURLs = make([]string, 0, len(input.ImageURLs))
	for _, rawURL := range input.ImageURLs {
		dataURL, err := s.loadImageDataURL(ctx, rawURL)
		if err != nil {
			return err
		}
		input.imageDataURLs = append(input.imageDataURLs, dataURL)
	}
	return nil
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
		return "", errors.New("不支持的图片地址")
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
		return "", fmt.Errorf("图片下载失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("图片下载失败：HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxImageDownloadBytes {
		return "", errors.New("图片超过 25 MiB 安全上限")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageDownloadBytes+1))
	if err != nil {
		return "", fmt.Errorf("图片读取失败：%w", err)
	}
	if len(data) > maxImageDownloadBytes {
		return "", errors.New("图片超过 25 MiB 安全上限")
	}
	contentType := normalizedImageContentType(resp.Header.Get("Content-Type"), data)
	return normalizeImageDataURL(data, contentType)
}

func decodeImageDataURL(rawURL string) ([]byte, string, error) {
	header, encoded, found := strings.Cut(rawURL, ",")
	if !found || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return nil, "", errors.New("不支持的图片数据")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxImageDownloadBytes)+4 {
		return nil, "", errors.New("图片超过 25 MiB 安全上限")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", errors.New("图片数据损坏")
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
		return "", fmt.Errorf("不支持的图片格式 %q", contentType)
	}
	if len(data) <= maxImageBytes {
		return imageDataURL(data, contentType), nil
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return "", errors.New("图片数据损坏，无法压缩")
	}
	if int64(config.Width)*int64(config.Height) > maxImagePixels {
		return "", errors.New("图片像素尺寸过大")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("图片数据损坏，无法压缩")
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
	return nil, errors.New("图片压缩后仍超过 10 MiB")
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
