// Package imageprep normalises uploaded images before they go to a vision
// model: bounded size (so prompt tokens and holds stay predictable) and no
// metadata (EXIF location and the like are dropped by re-encoding).
package imageprep

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// MaxSide bounds the longer edge sent to the model. Qwen2.5-VL uses one token
// per 28x28 patch, so 1024px is at most ~1400 tokens.
const MaxSide = 1024

// maxPixels guards against decompression bombs before decoding.
const maxPixels = 50_000_000

var ErrNotImage = errors.New("not a supported image (JPEG, PNG, GIF or WebP)")
var ErrTooLarge = errors.New("image dimensions are too large")

// Detect reports whether data is an image this package can prepare.
func Detect(data []byte) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && cfg.Width > 0 && cfg.Height > 0
}

// Prepare decodes, downsizes to MaxSide, flattens transparency onto white
// and re-encodes as JPEG.
func Prepare(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, ErrTooLarge
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}

	w, h := cfg.Width, cfg.Height
	if w > MaxSide || h > MaxSide {
		if w >= h {
			w, h = MaxSide, max(1, h*MaxSide/w)
		} else {
			w, h = max(1, w*MaxSide/h), MaxSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DataURL prepares an image and returns it as a base64 JPEG data URL.
func DataURL(data []byte) (string, error) {
	out, err := Prepare(data)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out), nil
}
