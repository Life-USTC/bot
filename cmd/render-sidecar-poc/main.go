// Command render-sidecar-poc verifies the typst sidecar renderer
// (RemoteRenderer against renderd). It renders the bus timetable card via
// HTTP, writes the PNG to disk, and measures end-to-end latency.
//
// Usage:
//
//	go run ./cmd/render-sidecar-poc -addr http://127.0.0.1:9123/render -out /tmp/bus.png
//	go run ./cmd/render-sidecar-poc -concurrency 20 -requests 100
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Life-USTC/Bot/internal/responses"
)

// pocBusImage mirrors testBusImage in internal/responses/render_test.go.
func pocBusImage() *responses.Image {
	return responses.NewTextImage("bus", "校车 东区 → 西区", strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"14:30\t14:40\t14:52\t15:05",
		"16:00\t16:10\t16:22\t16:35",
		"",
		"东区\t北区\t西区",
		"15:30\t15:35\t15:40\t✨",
		"15:50\t15:55\t16:00",
	}, "\n"))
}

// pocBusAllImage mirrors testBusAllImage in internal/responses/render_test.go.
func pocBusAllImage() *responses.Image {
	return responses.NewTextImage("bus", "校车", strings.Join([]string{
		"东区\t西区\t先研院\t高新区",
		"14:30\t14:40\t14:52\t15:05",
		"16:00\t16:10\t16:22\t16:35",
		"",
		"东区\t北区\t西区",
		"15:30\t15:35\t15:40\t✨",
		"15:50\t15:55\t16:00",
		"",
		"西区\t北区\t东区",
		"15:40\t15:45\t16:00",
		"16:10\t16:15\t16:30",
	}, "\n"))
}

func main() {
	addr := flag.String("addr", envOr("RENDERD_ADDR", "http://127.0.0.1:9123/render"), "render endpoint URL")
	out := flag.String("out", "", "write single-render PNG to this path")
	warm := flag.Int("warm", 20, "number of warm renders after the first")
	concurrency := flag.Int("concurrency", 0, "if >0, run stress mode with this many workers")
	requests := flag.Int("requests", 100, "stress mode: total requests")
	flag.Parse()

	// Fixed clock so highlight/departed marking is deterministic.
	fixedNow := func() time.Time {
		return time.Date(2026, 7, 9, 15, 28, 0, 0, time.FixedZone("CST", 8*60*60))
	}
	renderer := responses.RemoteRenderer{Endpoint: *addr, Now: fixedNow}

	if *concurrency > 0 {
		runStress(renderer, *concurrency, *requests)
		return
	}
	runSingle(renderer, *out, *warm)
}

func runSingle(renderer responses.RemoteRenderer, out string, warm int) {
	images := []struct {
		name string
		img  *responses.Image
	}{
		{"route", pocBusImage()},
		{"all", pocBusAllImage()},
	}
	for _, item := range images {
		start := time.Now()
		png, width, height, err := renderer.RenderPNG(item.img)
		cold := time.Since(start)
		if err != nil {
			fmt.Fprintf(os.Stderr, "render %s failed: %v\n", item.name, err)
			os.Exit(1)
		}
		fmt.Printf("[%s] cold render: %v (%dx%d, %d bytes)\n", item.name, cold, width, height, len(png))

		durations := make([]time.Duration, 0, warm)
		for i := 0; i < warm; i++ {
			start := time.Now()
			if _, _, _, err := renderer.RenderPNG(item.img); err != nil {
				fmt.Fprintf(os.Stderr, "warm render %s failed: %v\n", item.name, err)
				os.Exit(1)
			}
			durations = append(durations, time.Since(start))
		}
		fmt.Printf("[%s] warm renders (n=%d): min=%v median=%v max=%v\n",
			item.name, len(durations), minDur(durations), medianDur(durations), maxDur(durations))

		if out != "" {
			path := out
			if item.name == "all" {
				path = strings.TrimSuffix(out, ".png") + "-all.png"
			}
			if err := os.WriteFile(path, png, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
				os.Exit(1)
			}
			fmt.Printf("[%s] wrote %s\n", item.name, path)
		}
	}
}

func runStress(renderer responses.RemoteRenderer, concurrency, total int) {
	jobs := make(chan struct{}, total)
	durations := make([]time.Duration, total)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	idx := 0

	start := time.Now()
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				reqStart := time.Now()
				_, _, _, err := renderer.RenderPNG(pocBusImage())
				elapsed := time.Since(reqStart)
				mu.Lock()
				if err != nil {
					failures++
					fmt.Fprintf(os.Stderr, "stress render failed: %v\n", err)
				} else {
					durations[idx] = elapsed
					idx++
				}
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < total; i++ {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()
	wall := time.Since(start)

	durations = durations[:idx]
	fmt.Printf("stress: concurrency=%d requests=%d failures=%d wall=%v throughput=%.1f req/s\n",
		concurrency, total, failures, wall, float64(idx)/wall.Seconds())
	if idx > 0 {
		fmt.Printf("stress latency: min=%v median=%v p95=%v max=%v\n",
			minDur(durations), medianDur(durations), percentileDur(durations, 0.95), maxDur(durations))
	}
	if failures > 0 {
		os.Exit(1)
	}
}

func minDur(d []time.Duration) time.Duration {
	m := d[0]
	for _, v := range d[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxDur(d []time.Duration) time.Duration {
	m := d[0]
	for _, v := range d[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func medianDur(d []time.Duration) time.Duration { return percentileDur(d, 0.5) }

func percentileDur(d []time.Duration, p float64) time.Duration {
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[int(p*float64(len(sorted)-1))]
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
