package imgtools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	MaxSingleImageBase64 = 1 * 1024 * 1024     // 1 MB base64 target
	MaxTotalImagesBase64 = 4 * 1024 * 1024     // 4 MB total base64 limit
)

// Crop defines a pixel rectangle in original image coordinates.
type Crop struct {
	X      int `json:"x" jsonschema:"crop left X in original image pixels"`
	Y      int `json:"y" jsonschema:"crop top Y in original image pixels"`
	Width  int `json:"width" jsonschema:"crop width in original image pixels"`
	Height int `json:"height" jsonschema:"crop height in original image pixels"`
}

// Options configure the image transformation.
type Options struct {
	MaxDimension int   `json:"max_dimension,omitempty"`
	Quality      int   `json:"quality,omitempty"`
	Format       string `json:"format,omitempty"` // "jpeg" or "png"
	Crop         *Crop `json:"crop,omitempty"`
	Rotate       int   `json:"rotate,omitempty"`  // 0, 90, 180, 270
	ScratchDir   string `json:"-"`
}

// Result contains both base64 payload and detailed image metrics.
type Result struct {
	Base64   string  `json:"base64"`
	MIMEType string  `json:"mimeType"`
	OrigW    int     `json:"orig_w"`
	OrigH    int     `json:"orig_h"`
	OrigSize int64   `json:"orig_size"`
	OrigExt  string  `json:"orig_format"`
	OutW     int     `json:"out_w"`
	OutH     int     `json:"out_h"`
	OutSize  int64   `json:"out_size"`
	Format   string  `json:"format"`
	Quality  int     `json:"quality"`
	Scale    float64 `json:"scale"`
	CropStr  string  `json:"crop_str"`
	TextInfo string  `json:"-"`
}

// SupportedExtensions lists all image extensions handled by the processor.
var SupportedExtensions = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "webp": true,
	"gif": true, "bmp": true, "tiff": true, "tif": true,
	"heic": true, "heif": true,
}

// Process reads and transforms an image file according to Options.
func Process(ctx context.Context, path string, opts Options) (*Result, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("path is a directory: %s", path)
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if !SupportedExtensions[ext] {
		return nil, fmt.Errorf("unsupported image format %q (supported: jpg, jpeg, png, webp, gif, bmp, tiff, heic)", ext)
	}

	if opts.MaxDimension <= 0 {
		opts.MaxDimension = 1568
	}
	if opts.Quality <= 0 {
		opts.Quality = 80
	}
	if opts.Quality < 30 {
		opts.Quality = 30
	} else if opts.Quality > 95 {
		opts.Quality = 95
	}

	opts.Format = strings.ToLower(strings.TrimSpace(opts.Format))
	if opts.Format == "" {
		opts.Format = "jpeg"
	}
	if opts.Format != "jpeg" && opts.Format != "png" {
		return nil, fmt.Errorf("unsupported output format %q (must be 'jpeg' or 'png')", opts.Format)
	}

	if opts.Rotate != 0 && opts.Rotate != 90 && opts.Rotate != 180 && opts.Rotate != 270 {
		return nil, fmt.Errorf("rotate must be 0, 90, 180, or 270 degrees (got %d)", opts.Rotate)
	}

	scratch := opts.ScratchDir
	if scratch == "" {
		scratch = os.TempDir()
	}

	// For HEIC on macOS, convert to PNG if Python PIL does not support HEIC directly
	actualPath := path
	var cleanupFiles []string
	defer func() {
		for _, f := range cleanupFiles {
			_ = os.Remove(f)
		}
	}()

	if ext == "heic" || ext == "heif" {
		if _, err := exec.LookPath("sips"); err == nil {
			tmpPng := filepath.Join(scratch, fmt.Sprintf("heic_conv_%d.png", os.Getpid()))
			cmd := exec.CommandContext(ctx, "sips", "-s", "format", "png", path, "--out", tmpPng)
			if out, err := cmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("sips convert heic: %v (%s)", err, strings.TrimSpace(string(out)))
			}
			actualPath = tmpPng
			cleanupFiles = append(cleanupFiles, tmpPng)
		}
	}

	// Try Python + PIL pipeline first
	if res, err := processWithPython(ctx, path, actualPath, opts); err == nil {
		res.OrigSize = st.Size()
		if ext == "heic" || ext == "heif" {
			res.OrigExt = ext
		}
		res.TextInfo = FormatTextInfo(path, res)
		return res, nil
	}

	// Fallback to pure Go pipeline for standard formats (JPEG, PNG, GIF)
	res, err := processWithGo(path, actualPath, opts)
	if err != nil {
		return nil, err
	}
	res.OrigSize = st.Size()
	res.TextInfo = FormatTextInfo(path, res)
	return res, nil
}

// FormatTextInfo builds the user-specified TextContent block.
func FormatTextInfo(path string, res *Result) string {
	cropDesc := "none"
	if res.CropStr != "" && res.CropStr != "none" {
		cropDesc = res.CropStr
	}
	origExt := res.OrigExt
	if origExt == "" {
		origExt = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	outDesc := fmt.Sprintf("%dx%d, %s q%d, %s", res.OutW, res.OutH, res.Format, res.Quality, formatSize(res.OutSize))
	if res.Format == "png" {
		outDesc = fmt.Sprintf("%dx%d, png, %s", res.OutW, res.OutH, formatSize(res.OutSize))
	}
	return fmt.Sprintf("path: %s\noriginal: %dx%d, %s, %s\noutput: %s\ncrop: %s\nscale: %.4f",
		path, res.OrigW, res.OrigH, origExt, formatSize(res.OrigSize),
		outDesc, cropDesc, res.Scale)
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024.0)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024.0*1024.0))
}

const pyWorkerScript = `
import sys, json, os, io, base64
from PIL import Image, ImageOps

def run():
    req = json.load(sys.stdin)
    path = req['path']
    max_dim = req.get('max_dimension', 1568)
    q = req.get('quality', 80)
    fmt = req.get('format', 'jpeg').lower()
    rotate = req.get('rotate', 0)
    crop = req.get('crop')

    orig_size = os.path.getsize(path)
    im = Image.open(path)
    orig_fmt = (im.format or 'jpeg').lower()

    # EXIF auto-orientation
    im = ImageOps.exif_transpose(im)
    orig_w, orig_h = im.size

    # Rotate before crop
    if rotate in (90, 180, 270):
        im = im.rotate(-rotate, expand=True)
    cur_w, cur_h = im.size

    crop_str = "none"
    if crop:
        cx, cy, cw, ch = crop['x'], crop['y'], crop['width'], crop['height']
        if cx < 0 or cy < 0 or cw <= 0 or ch <= 0:
            raise ValueError(f"crop parameters must be positive (got x={cx}, y={cy}, w={cw}, h={ch})")
        if cx + cw > cur_w or cy + ch > cur_h:
            raise ValueError(f"crop region (x={cx}, y={cy}, w={cw}, h={ch}) exceeds image bounds ({cur_w}x{cur_h})")
        im = im.crop((cx, cy, cx + cw, cy + ch))
        crop_str = f"x={cx},y={cy},w={cw},h={ch}"

    post_crop_w, post_crop_h = im.size

    cur_q = q
    cur_dim = max_dim
    b64 = ""
    out_bytes = b""
    out_w, out_h = post_crop_w, post_crop_h

    # Iterative compression to satisfy single-image <= 1 MB base64
    for attempt in range(20):
        target = im.copy()
        if max(target.size) > cur_dim:
            target.thumbnail((cur_dim, cur_dim), Image.Resampling.LANCZOS)
        out_w, out_h = target.size

        buf = io.BytesIO()
        if fmt == 'jpeg':
            if target.mode in ('RGBA', 'P', 'LA'):
                target = target.convert('RGB')
            target.save(buf, format='JPEG', quality=cur_q)
        else:
            target.save(buf, format='PNG')
        out_bytes = buf.getvalue()
        b64 = base64.b64encode(out_bytes).decode('ascii')

        if len(b64) <= 1048576:
            break

        if fmt == 'jpeg' and cur_q > 40:
            cur_q = max(40, cur_q - 15)
        else:
            cur_dim = int(cur_dim * 0.8)
            if cur_dim < 64:
                break

    src_w = post_crop_w if crop else orig_w
    scale = float(out_w) / float(src_w) if src_w > 0 else 1.0

    mime = 'image/jpeg' if fmt == 'jpeg' else 'image/png'
    out = {
        'base64': b64,
        'mimeType': mime,
        'orig_w': orig_w,
        'orig_h': orig_h,
        'orig_size': orig_size,
        'orig_format': orig_fmt,
        'out_w': out_w,
        'out_h': out_h,
        'out_size': len(out_bytes),
        'format': fmt,
        'quality': cur_q,
        'scale': scale,
        'crop_str': crop_str,
    }
    json.dump(out, sys.stdout)

if __name__ == '__main__':
    try:
        run()
    except Exception as e:
        sys.stderr.write(str(e))
        sys.exit(1)
`

func processWithPython(ctx context.Context, displayPath, actualPath string, opts Options) (*Result, error) {
	pythonExe := "python3"
	if p, err := exec.LookPath("python3"); err == nil {
		pythonExe = p
	} else if p, err := exec.LookPath("python"); err == nil {
		pythonExe = p
	} else {
		return nil, errors.New("python not found")
	}

	reqData := map[string]any{
		"path":          actualPath,
		"max_dimension": opts.MaxDimension,
		"quality":       opts.Quality,
		"format":        opts.Format,
		"rotate":        opts.Rotate,
	}
	if opts.Crop != nil {
		reqData["crop"] = map[string]int{
			"x":      opts.Crop.X,
			"y":      opts.Crop.Y,
			"width":  opts.Crop.Width,
			"height": opts.Crop.Height,
		}
	}
	reqBytes, err := json.Marshal(reqData)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, pythonExe, "-c", pyWorkerScript)
	cmd.Stdin = bytes.NewReader(reqBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errStr := strings.TrimSpace(stderr.String())
		if errStr == "" {
			errStr = err.Error()
		}
		return nil, fmt.Errorf("%s", errStr)
	}

	var res Result
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("decode python result: %w (%s)", err, stdout.String())
	}
	return &res, nil
}

// processWithGo is standard library Go fallback for JPEG, PNG, GIF
func processWithGo(displayPath, actualPath string, opts Options) (*Result, error) {
	f, err := os.Open(actualPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	origW := img.Bounds().Dx()
	origH := img.Bounds().Dy()

	curImg := img
	curW := origW
	curH := origH

	cropStr := "none"
	if opts.Crop != nil {
		cx := opts.Crop.X
		cy := opts.Crop.Y
		cw := opts.Crop.Width
		ch := opts.Crop.Height
		if cx < 0 || cy < 0 || cw <= 0 || ch <= 0 {
			return nil, fmt.Errorf("crop parameters must be positive (got x=%d, y=%d, w=%d, h=%d)", cx, cy, cw, ch)
		}
		if cx+cw > curW || cy+ch > curH {
			return nil, fmt.Errorf("crop region (x=%d, y=%d, w=%d, h=%d) exceeds image bounds (%dx%d)", cx, cy, cw, ch, curW, curH)
		}
		type subImager interface {
			SubImage(r image.Rectangle) image.Image
		}
		if si, ok := curImg.(subImager); ok {
			curImg = si.SubImage(image.Rect(cx, cy, cx+cw, cy+ch))
		} else {
			// fallback manual crop
			dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
			for y := 0; y < ch; y++ {
				for x := 0; x < cw; x++ {
					dst.Set(x, y, curImg.At(cx+x, cy+y))
				}
			}
			curImg = dst
		}
		curW = cw
		curH = ch
		cropStr = fmt.Sprintf("x=%d,y=%d,w=%d,h=%d", cx, cy, cw, ch)
	}

	curQ := opts.Quality
	curDim := opts.MaxDimension
	var b64 string
	var outBytes []byte
	outW := curW
	outH := curH

	for attempt := 0; attempt < 15; attempt++ {
		target := curImg
		targetW := curW
		targetH := curH
		if maxInt(targetW, targetH) > curDim {
			if targetW >= targetH {
				targetW = curDim
				targetH = maxInt(1, int(float64(curH)*float64(curDim)/float64(curW)))
			} else {
				targetH = curDim
				targetW = maxInt(1, int(float64(curW)*float64(curDim)/float64(curH)))
			}
			target = simpleResize(curImg, targetW, targetH)
		}
		outW = targetW
		outH = targetH

		var buf bytes.Buffer
		if opts.Format == "jpeg" {
			err = jpeg.Encode(&buf, target, &jpeg.Options{Quality: curQ})
		} else {
			err = png.Encode(&buf, target)
		}
		if err != nil {
			return nil, fmt.Errorf("encode image: %w", err)
		}
		outBytes = buf.Bytes()
		b64 = base64.StdEncoding.EncodeToString(outBytes)

		if len(b64) <= MaxSingleImageBase64 {
			break
		}

		if opts.Format == "jpeg" && curQ > 40 {
			curQ -= 15
			if curQ < 40 {
				curQ = 40
			}
		} else {
			curDim = int(float64(curDim) * 0.8)
			if curDim < 64 {
				break
			}
		}
	}

	srcW := curW
	if opts.Crop == nil {
		srcW = origW
	}
	scale := 1.0
	if srcW > 0 {
		scale = float64(outW) / float64(srcW)
	}

	mime := "image/jpeg"
	if opts.Format == "png" {
		mime = "image/png"
	}

	return &Result{
		Base64:   b64,
		MIMEType: mime,
		OrigW:    origW,
		OrigH:    origH,
		OrigExt:  format,
		OutW:     outW,
		OutH:     outH,
		OutSize:  int64(len(outBytes)),
		Format:   opts.Format,
		Quality:  curQ,
		Scale:    scale,
		CropStr:  cropStr,
	}, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func simpleResize(src image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	srcBounds := src.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()
	for y := 0; y < h; y++ {
		sy := srcBounds.Min.Y + (y * srcH / h)
		for x := 0; x < w; x++ {
			sx := srcBounds.Min.X + (x * srcW / w)
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
