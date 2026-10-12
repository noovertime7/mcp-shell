package server

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/noovertime7/mcp-shell/internal/config"
	"github.com/noovertime7/mcp-shell/internal/imgtools"
)

func createSamplePNG(t *testing.T, dir, filename string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 100, A: 255})
		}
	}
	p := filepath.Join(dir, filename)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestImageServer(t *testing.T, dir string) *Server {
	t.Helper()
	t.Setenv("MCP_SHELL_TOKEN", "")
	cfgMap := map[string]any{
		"listen": "127.0.0.1:0", "token": "test-token-12345678", "roots": []string{dir},
		"unrestricted": true, "scratch_dir": t.TempDir(),
	}
	data, _ := json.Marshal(cfgMap)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestReadImage_PNG(t *testing.T) {
	dir := t.TempDir()
	srv := newTestImageServer(t, dir)
	defer srv.Close()

	imgFile := createSamplePNG(t, dir, "a.png", 1920, 867)

	res, summary, err := srv.readImage(context.Background(), readImageIn{
		Path:         imgFile,
		MaxDimension: 1568,
		Quality:      80,
		Format:       "jpeg",
	})
	if err != nil {
		t.Fatalf("readImage failed: %v", err)
	}

	if len(res.Content) != 2 {
		t.Fatalf("expected 2 contents (text + image), got %d", len(res.Content))
	}
	textContent := res.Content[0]
	imageContent := res.Content[1]

	if textContent.Type != "text" {
		t.Errorf("expected content[0] to be text, got %s", textContent.Type)
	}
	if imageContent.Type != "image" {
		t.Errorf("expected content[1] to be image, got %s", imageContent.Type)
	}
	if imageContent.MIMEType != "image/jpeg" {
		t.Errorf("expected mimeType image/jpeg, got %s", imageContent.MIMEType)
	}
	if len(imageContent.Data) == 0 {
		t.Error("expected non-empty base64 data")
	}
	if len(imageContent.Data) > imgtools.MaxSingleImageBase64 {
		t.Errorf("image base64 exceeded 1MB: %d bytes", len(imageContent.Data))
	}

	// Verify text content formatting
	text := textContent.Text
	if !strings.Contains(text, filepath.Base(imgFile)) {
		t.Errorf("text missing path: %s", text)
	}
	if !strings.Contains(text, "original: 1920x867, png") {
		t.Errorf("text missing original info: %s", text)
	}
	if !strings.Contains(text, "output: 1568x") || !strings.Contains(text, "jpeg q80") {
		t.Errorf("text missing output info: %s", text)
	}
	if !strings.Contains(text, "crop: none") {
		t.Errorf("text missing crop info: %s", text)
	}
	if !strings.Contains(text, "scale: 0.8167") {
		t.Errorf("text missing scale 0.8167: %s", text)
	}
	if !strings.Contains(summary, "out=1568x") {
		t.Errorf("summary: %s", summary)
	}
}

func TestReadImage_Crop(t *testing.T) {
	dir := t.TempDir()
	srv := newTestImageServer(t, dir)
	defer srv.Close()

	imgFile := createSamplePNG(t, dir, "form.png", 1000, 800)

	res, _, err := srv.readImage(context.Background(), readImageIn{
		Path: imgFile,
		Crop: &imgtools.Crop{
			X:      100,
			Y:      200,
			Width:  400,
			Height: 300,
		},
		Format: "jpeg",
	})
	if err != nil {
		t.Fatalf("crop readImage failed: %v", err)
	}

	if len(res.Content) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(res.Content))
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "crop: x=100,y=200,w=400,h=300") {
		t.Errorf("text missing crop string: %s", text)
	}
	if !strings.Contains(text, "output: 400x300") {
		t.Errorf("text missing output 400x300: %s", text)
	}
	if !strings.Contains(text, "scale: 1.0000") {
		t.Errorf("scale: %s", text)
	}
}

func TestReadImages_Batch5(t *testing.T) {
	dir := t.TempDir()
	srv := newTestImageServer(t, dir)
	defer srv.Close()

	paths := make([]string, 5)
	for i := 0; i < 5; i++ {
		filename := filepath.Join(dir, strings.Repeat("img", i+1)+".png")
		paths[i] = createSamplePNG(t, dir, filepath.Base(filename), 400+i*50, 300+i*50)
	}

	res, summary, err := srv.readImages(context.Background(), readImagesIn{
		Paths:  paths,
		Format: "jpeg",
	})
	if err != nil {
		t.Fatalf("readImages batch failed: %v", err)
	}

	// 5 images -> 10 contents (text + image alternating)
	if len(res.Content) != 10 {
		t.Fatalf("expected 10 contents, got %d", len(res.Content))
	}

	for i := 0; i < 5; i++ {
		textItem := res.Content[i*2]
		imgItem := res.Content[i*2+1]
		if textItem.Type != "text" {
			t.Errorf("[%d] expected text, got %s", i, textItem.Type)
		}
		if !strings.Contains(textItem.Text, paths[i]) {
			t.Errorf("[%d] text missing path %s: %s", i, paths[i], textItem.Text)
		}
		if imgItem.Type != "image" {
			t.Errorf("[%d] expected image, got %s", i, imgItem.Type)
		}
		if len(imgItem.Data) == 0 {
			t.Errorf("[%d] empty image data", i)
		}
	}

	if !strings.Contains(summary, "5 images:") {
		t.Errorf("summary: %s", summary)
	}
}

func TestCaptureScreen_ReturnImage(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("capture_screen only on macOS")
	}

	dir := t.TempDir()
	srv := newTestImageServer(t, dir)
	defer srv.Close()

	dest := filepath.Join(dir, "my_screenshot.png")
	res, summary, err := srv.captureScreen(context.Background(), captureScreenIn{
		Path:        dest,
		ReturnImage: true,
	})
	if err != nil {
		t.Fatalf("captureScreen: %v", err)
	}
	if len(res.Content) != 2 {
		t.Fatalf("expected 2 content items, got %d", len(res.Content))
	}
	if res.Content[0].Type != "text" || !strings.Contains(res.Content[0].Text, "screenshot saved") {
		t.Errorf("text: %s", res.Content[0].Text)
	}
	if res.Content[1].Type != "image" || len(res.Content[1].Data) == 0 {
		t.Errorf("image item invalid: %v", res.Content[1])
	}
	if !strings.Contains(summary, "screenshot saved") {
		t.Errorf("summary: %s", summary)
	}
}
