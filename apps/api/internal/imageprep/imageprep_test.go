package imageprep

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func pngOf(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 128})
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestPrepareDownscales(t *testing.T) {
	out, err := Prepare(pngOf(3000, 1500))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != MaxSide || cfg.Height != MaxSide/2 {
		t.Fatalf("got %dx%d", cfg.Width, cfg.Height)
	}
}

func TestPrepareKeepsSmall(t *testing.T) {
	out, err := Prepare(pngOf(40, 60))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := jpeg.DecodeConfig(bytes.NewReader(out))
	if cfg.Width != 40 || cfg.Height != 60 {
		t.Fatalf("got %dx%d", cfg.Width, cfg.Height)
	}
}

func TestRejectsNonImages(t *testing.T) {
	if Detect([]byte("hello")) {
		t.Fatal("text detected as image")
	}
	if _, err := Prepare([]byte("hello")); err != ErrNotImage {
		t.Fatalf("err = %v", err)
	}
}

func TestDataURL(t *testing.T) {
	u, err := DataURL(pngOf(10, 10))
	if err != nil || !strings.HasPrefix(u, "data:image/jpeg;base64,") {
		t.Fatalf("%v %q", err, u[:min(len(u), 30)])
	}
}
