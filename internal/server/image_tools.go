package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/noovertime7/mcp-shell/internal/imgtools"
	"github.com/noovertime7/mcp-shell/internal/mcp"
)

type readImageIn struct {
	Path         string         `json:"path" jsonschema:"image file path; supports jpg, jpeg, png, webp, gif, bmp, tiff, heic"`
	MaxDimension int            `json:"max_dimension,omitempty" jsonschema:"max dimension (width or height) to scale down to; default 1568. Smaller images are not upscaled"`
	Quality      int            `json:"quality,omitempty" jsonschema:"JPEG compression quality (30-95, default 80)"`
	Format       string         `json:"format,omitempty" jsonschema:"output format: 'jpeg' or 'png', default 'jpeg'"`
	Crop         *imgtools.Crop `json:"crop,omitempty" jsonschema:"crop rectangle in original image pixel coordinates (x, y, width, height), crop applied before scaling"`
	Rotate       int            `json:"rotate,omitempty" jsonschema:"rotation clockwise in degrees before crop: 0, 90, 180, or 270"`
}

type readImagesIn struct {
	Paths        []string       `json:"paths" jsonschema:"list of image paths (maximum 10 images)"`
	MaxDimension int            `json:"max_dimension,omitempty" jsonschema:"max dimension to scale down to; default 1568. Smaller images are not upscaled"`
	Quality      int            `json:"quality,omitempty" jsonschema:"JPEG compression quality (30-95, default 80)"`
	Format       string         `json:"format,omitempty" jsonschema:"output format: 'jpeg' or 'png', default 'jpeg'"`
	Crop         *imgtools.Crop `json:"crop,omitempty" jsonschema:"crop rectangle in original image pixel coordinates (applied to each image)"`
	Rotate       int            `json:"rotate,omitempty" jsonschema:"rotation clockwise in degrees before crop: 0, 90, 180, or 270"`
}

func (s *Server) readImage(ctx context.Context, in readImageIn) (*mcp.CallResult, string, error) {
	target := strings.TrimSpace(in.Path)
	if target == "" {
		return nil, "", fmt.Errorf("path is required")
	}
	resolved, err := s.guard.Resolve(target)
	if err != nil {
		return nil, "", err
	}

	res, err := imgtools.Process(ctx, resolved, imgtools.Options{
		MaxDimension: in.MaxDimension,
		Quality:      in.Quality,
		Format:       in.Format,
		Crop:         in.Crop,
		Rotate:       in.Rotate,
		ScratchDir:   s.scratch,
	})
	if err != nil {
		return nil, "", err
	}

	callRes := &mcp.CallResult{
		Content: []mcp.Content{
			{Type: "text", Text: res.TextInfo},
			{Type: "image", Data: res.Base64, MIMEType: res.MIMEType},
		},
	}
	summary := fmt.Sprintf("path=%s out=%dx%d(%s)", target, res.OutW, res.OutH, res.Format)
	return callRes, summary, nil
}

func (s *Server) readImages(ctx context.Context, in readImagesIn) (*mcp.CallResult, string, error) {
	if len(in.Paths) == 0 {
		return nil, "", fmt.Errorf("paths cannot be empty")
	}
	if len(in.Paths) > 10 {
		return nil, "", fmt.Errorf("too many images (%d): maximum 10 images allowed per request", len(in.Paths))
	}

	var contents []mcp.Content
	var totalBase64 int
	var summaries []string

	for idx, p := range in.Paths {
		target := strings.TrimSpace(p)
		if target == "" {
			return nil, "", fmt.Errorf("paths[%d]: empty path", idx)
		}
		resolved, err := s.guard.Resolve(target)
		if err != nil {
			return nil, "", fmt.Errorf("paths[%d] (%s): %w", idx, target, err)
		}

		res, err := imgtools.Process(ctx, resolved, imgtools.Options{
			MaxDimension: in.MaxDimension,
			Quality:      in.Quality,
			Format:       in.Format,
			Crop:         in.Crop,
			Rotate:       in.Rotate,
			ScratchDir:   s.scratch,
		})
		if err != nil {
			return nil, "", fmt.Errorf("paths[%d] (%s): %w", idx, target, err)
		}

		totalBase64 += len(res.Base64)
		contents = append(contents,
			mcp.Content{Type: "text", Text: res.TextInfo},
			mcp.Content{Type: "image", Data: res.Base64, MIMEType: res.MIMEType},
		)
		summaries = append(summaries, fmt.Sprintf("%s(%dx%d)", filepath.Base(target), res.OutW, res.OutH))
	}

	if totalBase64 > imgtools.MaxTotalImagesBase64 {
		return nil, "", fmt.Errorf("total images payload (%.2f MB) exceeds 4 MB limit; please reduce the number of images or specify a smaller max_dimension", float64(totalBase64)/(1024*1024))
	}

	callRes := &mcp.CallResult{
		Content: contents,
	}
	summary := fmt.Sprintf("%d images: %s", len(in.Paths), strings.Join(summaries, ", "))
	return callRes, summary, nil
}
