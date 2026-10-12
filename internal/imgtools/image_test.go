package imgtools

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createTestPNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	p := filepath.Join(dir, "test.png")
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

func TestProcessPNG_DimensionsAndScale(t *testing.T) {
	tmp := t.TempDir()
	p := createTestPNG(t, tmp, 1920, 867)

	res, err := Process(context.Background(), p, Options{
		MaxDimension: 1568,
		Quality:      80,
		Format:       "jpeg",
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.OrigW != 1920 || res.OrigH != 867 {
		t.Errorf("expected orig 1920x867, got %dx%d", res.OrigW, res.OrigH)
	}
	if res.OutW != 1568 {
		t.Errorf("expected out width 1568, got %d", res.OutW)
	}
	calcH := 867.0 * 1568.0 / 1920.0
	expectedH := int(calcH) // approx 708
	if res.OutH < expectedH-2 || res.OutH > expectedH+2 {
		t.Errorf("expected out height ~%d, got %d", expectedH, res.OutH)
	}
	if len(res.Base64) > MaxSingleImageBase64 {
		t.Errorf("base64 exceeds 1MB: %d bytes", len(res.Base64))
	}
	if res.MIMEType != "image/jpeg" {
		t.Errorf("expected mime image/jpeg, got %s", res.MIMEType)
	}
	if !strings.Contains(res.TextInfo, "original: 1920x867") {
		t.Errorf("text info missing original dims: %s", res.TextInfo)
	}
	if !strings.Contains(res.TextInfo, "output: 1568x") {
		t.Errorf("text info missing output dims: %s", res.TextInfo)
	}
}

func TestProcessCrop(t *testing.T) {
	tmp := t.TempDir()
	p := createTestPNG(t, tmp, 1000, 800)

	res, err := Process(context.Background(), p, Options{
		MaxDimension: 1568,
		Crop: &Crop{
			X:      100,
			Y:      200,
			Width:  400,
			Height: 300,
		},
		Format: "jpeg",
	})
	if err != nil {
		t.Fatalf("crop process: %v", err)
	}
	if res.OutW != 400 || res.OutH != 300 {
		t.Errorf("expected cropped out 400x300, got %dx%d", res.OutW, res.OutH)
	}
	if res.CropStr != "x=100,y=200,w=400,h=300" {
		t.Errorf("cropStr: %s", res.CropStr)
	}
	if res.Scale != 1.0 {
		t.Errorf("scale should be 1.0 for unscaled crop, got %f", res.Scale)
	}
}

func TestProcessCrop_OutOfBounds(t *testing.T) {
	tmp := t.TempDir()
	p := createTestPNG(t, tmp, 500, 500)

	_, err := Process(context.Background(), p, Options{
		Crop: &Crop{
			X:      300,
			Y:      300,
			Width:  300,
			Height: 300,
		},
	})
	if err == nil {
		t.Fatal("expected out of bounds crop error")
	}
	if !strings.Contains(err.Error(), "exceeds image bounds") {
		t.Errorf("expected bounds error, got: %v", err)
	}
}

func TestProcess_FileNotFound(t *testing.T) {
	_, err := Process(context.Background(), "/non/existent/image.png", Options{})
	if err == nil {
		t.Fatal("expected file not found error")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("expected 'file not found', got: %v", err)
	}
}

func TestProcess_SmallImageNotUpscaled(t *testing.T) {
	tmp := t.TempDir()
	p := createTestPNG(t, tmp, 300, 200)

	res, err := Process(context.Background(), p, Options{
		MaxDimension: 1568,
		Format:       "jpeg",
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.OutW != 300 || res.OutH != 200 {
		t.Errorf("small image was resized: %dx%d", res.OutW, res.OutH)
	}
	if res.Scale != 1.0 {
		t.Errorf("scale should be 1.0, got %f", res.Scale)
	}
}

func TestProcess_RealHEICIfAvailable(t *testing.T) {
	heicPath := "/System/Library/Desktop Pictures/Hello Metallic Blue.heic"
	if _, err := os.Stat(heicPath); err != nil {
		t.Skip("system HEIC image not available")
	}

	res, err := Process(context.Background(), heicPath, Options{
		MaxDimension: 1568,
		Format:       "jpeg",
		Quality:      80,
	})
	if err != nil {
		t.Fatalf("process HEIC: %v", err)
	}
	if res.OrigW != 6016 || res.OrigH != 6016 {
		t.Errorf("orig HEIC dimensions: %dx%d", res.OrigW, res.OrigH)
	}
	if res.OutW != 1568 || res.OutH != 1568 {
		t.Errorf("out HEIC dimensions: %dx%d", res.OutW, res.OutH)
	}
	if len(res.Base64) > MaxSingleImageBase64 {
		t.Errorf("HEIC result base64 exceeds 1MB: %d", len(res.Base64))
	}
}
