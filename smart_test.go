package imaging

import (
	"image"
	"image/color"
	"testing"
)

// newSolidImage returns an opaque NRGBA image with a non-zero origin.
func newSolidImage(rect image.Rectangle, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := 0; y < rect.Dy(); y++ {
		for x := 0; x < rect.Dx(); x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	img.Rect = rect
	return img
}

// fillNRGBA fills the given rectangle (in image coordinates) with a color.
func fillNRGBA(img *image.NRGBA, rect image.Rectangle, c color.NRGBA) {
	r := rect.Intersect(img.Rect).Sub(img.Rect.Min)
	if r.Empty() {
		return
	}
	row := make([]uint8, r.Dx()*4)
	for i := 0; i < len(row); i += 4 {
		row[i+0] = c.R
		row[i+1] = c.G
		row[i+2] = c.B
		row[i+3] = c.A
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(img.Pix[y*img.Stride+r.Min.X*4:][:len(row)], row)
	}
}

// rectContains reports whether rect contains the given point.
func rectContains(rect image.Rectangle, pt image.Point) bool {
	return pt.In(rect)
}

var (
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	black = color.NRGBA{0x00, 0x00, 0x00, 0xff}
	red   = color.NRGBA{0xff, 0x00, 0x00, 0xff}
)

func TestSmartCropRectCorners(t *testing.T) {
	// The largest crop with the target aspect ratio always matches the
	// source in one dimension, so a corner subject pins down the window on
	// the free axis. Both orientations are covered below.
	const (
		srcW, srcH = 200, 160
		subject    = 30
	)

	corners := []struct {
		name    string
		x, y    int
		wantX   int // portrait window (50x160)
		wantY   int // landscape window (200x50)
		leftCol bool
		topRow  bool
	}{
		{"top-left", 0, 0, 0, 0, true, true},
		{"top-right", srcW - subject, 0, srcW - 50, 0, false, true},
		{"bottom-left", 0, srcH - subject, 0, srcH - 50, true, false},
		{"bottom-right", srcW - subject, srcH - subject, srcW - 50, srcH - 50, false, false},
	}

	for _, tc := range corners {
		t.Run(tc.name+" portrait", func(t *testing.T) {
			src := newSolidImage(image.Rect(0, 0, srcW, srcH), white)
			fillNRGBA(src, image.Rect(tc.x, tc.y, tc.x+subject, tc.y+subject), red)

			// The portrait window covers the full height and is 50 wide.
			rect := SmartCropRect(src, 50, 160)
			if want := image.Rect(tc.wantX, 0, tc.wantX+50, 160); rect != want {
				t.Fatalf("got crop %v want %v", rect, want)
			}

			// Same size crop and target: pixels are copied 1:1.
			dst := SmartThumbnail(src, 50, 160, Box)
			redX := tc.x - tc.wantX + 15
			redY := tc.y + 15
			if c := dst.NRGBAAt(redX, redY); c.R != 0xff || c.G != 0 || c.B != 0 {
				t.Fatalf("subject pixel at (%d,%d) is %v, want red", redX, redY, c)
			}
			whiteX := 45
			if !tc.leftCol {
				whiteX = 5
			}
			if c := dst.NRGBAAt(whiteX, redY); c != white {
				t.Fatalf("background pixel at (%d,%d) is %v, want white", whiteX, redY, c)
			}
		})

		t.Run(tc.name+" landscape", func(t *testing.T) {
			src := newSolidImage(image.Rect(0, 0, srcW, srcH), white)
			fillNRGBA(src, image.Rect(tc.x, tc.y, tc.x+subject, tc.y+subject), red)

			// The landscape window covers the full width and is 50 high.
			rect := SmartCropRect(src, 200, 50)
			if want := image.Rect(0, tc.wantY, 200, tc.wantY+50); rect != want {
				t.Fatalf("got crop %v want %v", rect, want)
			}

			dst := SmartThumbnail(src, 200, 50, Box)
			redX := tc.x + 15
			redY := tc.y - tc.wantY + 15
			if c := dst.NRGBAAt(redX, redY); c.R != 0xff || c.G != 0 || c.B != 0 {
				t.Fatalf("subject pixel at (%d,%d) is %v, want red", redX, redY, c)
			}
			whiteY := 45
			if !tc.topRow {
				whiteY = 5
			}
			if c := dst.NRGBAAt(redX, whiteY); c != white {
				t.Fatalf("background pixel at (%d,%d) is %v, want white", redX, whiteY, c)
			}
		})
	}
}

func TestSmartCropRectCenter(t *testing.T) {
	const (
		srcW, srcH = 200, 160
		subject    = 30
	)
	src := newSolidImage(image.Rect(0, 0, srcW, srcH), white)
	fillNRGBA(src, image.Rect(85, 65, 85+subject, 65+subject), red)

	// Every portrait window that contains the centered subject ties; the
	// tie is resolved in favor of the centered window.
	rect := SmartCropRect(src, 50, 160)
	if want := image.Rect((srcW-50)/2, 0, (srcW+50)/2, 160); rect != want {
		t.Fatalf("got crop %v want %v", rect, want)
	}

	dst := SmartThumbnail(src, 50, 160, Box)
	if c := dst.NRGBAAt(25, 80); c.R != 0xff || c.G != 0 || c.B != 0 {
		t.Fatalf("center pixel is %v, want red", c)
	}
	for _, p := range []image.Point{{2, 80}, {47, 80}} {
		if c := dst.NRGBAAt(p.X, p.Y); c != white {
			t.Fatalf("pixel %v is %v, want white", p, c)
		}
	}

	// The same holds vertically for a landscape window.
	rect = SmartCropRect(src, 200, 50)
	if want := image.Rect(0, (srcH-50)/2, 200, (srcH+50)/2); rect != want {
		t.Fatalf("got crop %v want %v", rect, want)
	}
	dst = SmartThumbnail(src, 200, 50, Box)
	if c := dst.NRGBAAt(100, 25); c.R != 0xff || c.G != 0 || c.B != 0 {
		t.Fatalf("center pixel is %v, want red", c)
	}
	for _, p := range []image.Point{{100, 2}, {100, 47}} {
		if c := dst.NRGBAAt(p.X, p.Y); c != white {
			t.Fatalf("pixel %v is %v, want white", p, c)
		}
	}
}

func TestSmartThumbnailDiffersFromCenter(t *testing.T) {
	src := newSolidImage(image.Rect(0, 0, 200, 100), white)
	fillNRGBA(src, image.Rect(0, 0, 60, 60), black)

	smart := SmartThumbnail(src, 50, 50, Box)
	center := Thumbnail(src, 50, 50, Box)
	if compareNRGBA(smart, center, 0) {
		t.Fatalf("smart thumbnail equals the centered thumbnail")
	}
	// The top-left of the smart output must be dark.
	if c := smart.NRGBAAt(5, 5); c.R > 0x20 {
		t.Fatalf("top-left output pixel is %v, want dark", c)
	}
	if c := smart.NRGBAAt(45, 45); c.R != 0xff {
		t.Fatalf("bottom-right output pixel is %v, want white", c)
	}
}

func TestSmartCropTransparentWeight(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	// Left half: fully transparent, but with a high-contrast checkerboard.
	for y := 0; y < 100; y++ {
		for x := 0; x < 50; x++ {
			if (x/4+y/4)%2 == 0 {
				src.SetNRGBA(x, y, color.NRGBA{0xff, 0xff, 0xff, 0x00})
			} else {
				src.SetNRGBA(x, y, color.NRGBA{0x00, 0x00, 0x00, 0x00})
			}
		}
	}
	// Right half: opaque white with a dark square near the bottom-right.
	fillNRGBA(src, image.Rect(50, 0, 100, 100), white)
	fillNRGBA(src, image.Rect(80, 70, 95, 90), black)

	rect := SmartCropRect(src, 50, 100)
	// Compare against a version of the same image where the transparent
	// checkerboard is replaced by transparent black.
	plain := Clone(src)
	fillNRGBA(plain, image.Rect(0, 0, 50, 100), color.NRGBA{0x00, 0x00, 0x00, 0x00})
	if got := SmartCropRect(plain, 50, 100); got != rect {
		t.Fatalf("transparent checkerboard influenced the crop: %v vs %v", rect, got)
	}
	if rect.Min.X <= 0 {
		t.Fatalf("crop %v placed against the transparent left edge", rect)
	}
	if !rectContains(rect, image.Pt(80, 70)) || !rectContains(rect, image.Pt(94, 89)) {
		t.Fatalf("crop %v does not contain the opaque subject", rect)
	}
}

func TestSmartThumbnailOpaqueSubjectOnTransparentCanvas(t *testing.T) {
	// A cover-style image: a transparent canvas with an opaque subject
	// near the right edge. The centered crop of Fill/Thumbnail would only
	// see transparent pixels; the smart crop must keep the subject.
	src := image.NewNRGBA(image.Rect(0, 0, 800, 200))
	fillNRGBA(src, image.Rect(650, 25, 790, 175), color.NRGBA{20, 120, 220, 255})

	rect := SmartCropRect(src, 100, 200)
	if rect.Dy() != 200 || rect.Dx() != 100 {
		t.Fatalf("unexpected crop %v", rect)
	}
	if rect.Max.X < 740 {
		t.Fatalf("crop %v does not follow the subject", rect)
	}

	opaque := func(img *image.NRGBA) float64 {
		n := 0
		for i := 3; i < len(img.Pix); i += 4 {
			if img.Pix[i] > 200 {
				n++
			}
		}
		return float64(n) / float64(len(img.Pix)/4)
	}

	smart := SmartThumbnail(src, 100, 200, Box)
	center := Thumbnail(src, 100, 200, Box)
	if got := opaque(center); got != 0 {
		t.Fatalf("centered thumbnail unexpectedly opaque: %.2f", got)
	}
	if got := opaque(smart); got < 0.6 {
		t.Fatalf("smart thumbnail is mostly transparent: %.2f", got)
	}
	if c := smart.NRGBAAt(50, 100); c.A != 0xff {
		t.Fatalf("center pixel is %v, want opaque subject", c)
	}
}

func TestSmartCropNonZeroBounds(t *testing.T) {
	bounds := image.Rect(-30, 20, 70, 120)
	src := newSolidImage(bounds, white)
	fillNRGBA(src, image.Rect(-30, 20, 10, 60), black)

	rect := SmartCropRect(src, 50, 50)
	if !rect.In(bounds) {
		t.Fatalf("crop %v is outside source bounds %v", rect, bounds)
	}
	if rect.Min.X >= 0 || rect.Min.Y != 20 {
		t.Fatalf("crop %v does not follow the non-zero bounds origin", rect)
	}
	if !rectContains(rect, image.Pt(-30, 20)) || !rectContains(rect, image.Pt(9, 59)) {
		t.Fatalf("crop %v does not contain the subject", rect)
	}

	dst := SmartThumbnail(src, 50, 50, Box)
	if dst.Rect != image.Rect(0, 0, 50, 50) {
		t.Fatalf("unexpected output rect: %v", dst.Rect)
	}
	if c := dst.NRGBAAt(5, 5); c.R > 0x20 {
		t.Fatalf("subject pixel is %v, want dark", c)
	}
}

func TestSmartCropAspectRatio(t *testing.T) {
	src := newSolidImage(image.Rect(0, 0, 200, 100), white)

	tests := []struct {
		name    string
		dstW    int
		dstH    int
		wantW   int
		wantH   int
		wantMin image.Point
	}{
		{"square target", 50, 50, 100, 100, image.Pt(50, 0)},
		{"very wide target", 100, 25, 200, 50, image.Pt(0, 25)},
		{"wide target", 50, 40, 125, 100, image.Pt(37, 0)},
		{"tall target", 20, 100, 20, 100, image.Pt(90, 0)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rect := SmartCropRect(src, tc.dstW, tc.dstH)
			if rect.Dx() != tc.wantW || rect.Dy() != tc.wantH {
				t.Fatalf("got crop size %dx%d want %dx%d", rect.Dx(), rect.Dy(), tc.wantW, tc.wantH)
			}
			if rect.Min != tc.wantMin {
				t.Fatalf("got crop origin %v want %v", rect.Min, tc.wantMin)
			}
			dst := SmartThumbnail(src, tc.dstW, tc.dstH, Box)
			if dst.Rect.Dx() != tc.dstW || dst.Rect.Dy() != tc.dstH {
				t.Fatalf("got output size %dx%d", dst.Rect.Dx(), dst.Rect.Dy())
			}
		})
	}
}

func TestSmartCropUniformFallback(t *testing.T) {
	tests := []struct {
		w, h int
	}{
		{100, 100},
		{101, 51},
		{51, 101},
		{100, 40},
		{40, 100},
	}
	for _, tc := range tests {
		src := newSolidImage(image.Rect(0, 0, tc.w, tc.h), color.NRGBA{0x80, 0x80, 0x80, 0xff})
		cropW, cropH := smartCropSize(tc.w, tc.h, 20, 20)
		rect := SmartCropRect(src, 20, 20)
		want := image.Rect(0, 0, cropW, cropH).Add(anchorPt(src.Bounds(), cropW, cropH, Center))
		if rect != want {
			t.Fatalf("%dx%d: got crop %v want centered %v", tc.w, tc.h, rect, want)
		}

		dst := SmartThumbnail(src, 20, 20, Box)
		if c := dst.NRGBAAt(10, 10); c.R != 0x80 || c.G != 0x80 || c.B != 0x80 || c.A != 0xff {
			t.Fatalf("%dx%d: got %v want uniform gray", tc.w, tc.h, c)
		}
	}
}

func TestSmartCropDeterministic(t *testing.T) {
	src := newSolidImage(image.Rect(0, 0, 160, 120), white)
	fillNRGBA(src, image.Rect(90, 10, 150, 70), black)
	fillNRGBA(src, image.Rect(20, 60, 80, 110), red)

	rect := SmartCropRect(src, 48, 40)
	first := SmartThumbnail(src, 48, 40, Lanczos)
	for i := 0; i < 5; i++ {
		if got := SmartCropRect(src, 48, 40); got != rect {
			t.Fatalf("crop rect is not deterministic: %v vs %v", got, rect)
		}
		if got := SmartThumbnail(src, 48, 40, Lanczos); !compareNRGBA(got, first, 0) {
			t.Fatalf("thumbnail is not deterministic")
		}
	}
}

func TestSmartThumbnailInvalidInput(t *testing.T) {
	src := newSolidImage(image.Rect(0, 0, 100, 80), white)
	emptyRect := image.Rectangle{}

	invalid := []struct {
		name string
		w, h int
	}{
		{"zero width", 0, 10},
		{"zero height", 10, 0},
		{"both zero", 0, 0},
		{"negative width", -4, 10},
		{"negative height", 10, -4},
		{"both negative", -1, -1},
		{"width too large", smartMaxTargetSide + 1, 10},
		{"height too large", 10, smartMaxTargetSide + 1},
		{"area too large", 1 << 13, 1 << 13},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if got := SmartCropRect(src, tc.w, tc.h); got != emptyRect {
				t.Fatalf("got crop %v want empty", got)
			}
			got := SmartThumbnail(src, tc.w, tc.h, Box)
			if got.Rect.Dx() != 0 || got.Rect.Dy() != 0 {
				t.Fatalf("got output %v want empty", got.Rect)
			}
		})
	}

	if got := SmartCropRect(nil, 10, 10); got != emptyRect {
		t.Fatalf("nil image: got crop %v want empty", got)
	}
	if got := SmartThumbnail(nil, 10, 10, Box); got.Rect.Dx() != 0 {
		t.Fatalf("nil image: got output %v want empty", got.Rect)
	}
}

func TestSmartThumbnailEmptyImage(t *testing.T) {
	images := []image.Image{
		&image.NRGBA{},
		image.NewNRGBA(image.Rect(0, 0, 0, 0)),
		image.NewNRGBA(image.Rect(0, 0, 10, 0)),
		image.NewNRGBA(image.Rect(0, 0, 0, 10)),
	}
	for _, src := range images {
		if got := SmartCropRect(src, 10, 10); got != (image.Rectangle{}) {
			t.Fatalf("got crop %v want empty", got)
		}
		got := SmartThumbnail(src, 10, 10, Box)
		if got.Rect.Dx() != 0 || got.Rect.Dy() != 0 {
			t.Fatalf("got output %v want empty", got.Rect)
		}
	}
}

func TestSmartThumbnailExtremeAspectRatio(t *testing.T) {
	tests := []struct {
		name string
		w, h int
	}{
		{"very wide", 4000, 3},
		{"very tall", 3, 4000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := newSolidImage(image.Rect(0, 0, tc.w, tc.h), white)
			if tc.w > tc.h {
				fillNRGBA(src, image.Rect(0, 0, 20, tc.h), red)
			} else {
				fillNRGBA(src, image.Rect(0, 0, tc.w, 20), red)
			}

			rect := SmartCropRect(src, 64, 64)
			if rect.Dx() != rect.Dy() || rect.Dx() < 1 {
				t.Fatalf("unexpected crop %v", rect)
			}
			if !rect.In(src.Bounds()) {
				t.Fatalf("crop %v outside bounds", rect)
			}

			dst := SmartThumbnail(src, 64, 64, Box)
			if dst.Rect.Dx() != 64 || dst.Rect.Dy() != 64 {
				t.Fatalf("unexpected output size %v", dst.Rect)
			}
			c := dst.NRGBAAt(2, 2)
			if c.R != 0xff || c.G != 0 || c.B != 0 {
				t.Fatalf("subject pixel is %v, want red", c)
			}
		})
	}
}

func TestSmartThumbnailLargeSource(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large image test in short mode")
	}

	const w, h = 4000, 3000
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	// Make the image cheap to build: top-left corner red, rest white.
	for y := 0; y < h; y++ {
		off := y * src.Stride
		for x := 0; x < w; x++ {
			i := off + x*4
			if x < 400 && y < 400 {
				src.Pix[i+0] = 0xff
				src.Pix[i+3] = 0xff
			} else {
				src.Pix[i+0] = 0xff
				src.Pix[i+1] = 0xff
				src.Pix[i+2] = 0xff
				src.Pix[i+3] = 0xff
			}
		}
	}

	// 1:3 target: the crop covers the full height and is 1000px wide,
	// so only the horizontal position depends on the subject.
	rect := SmartCropRect(src, 100, 300)
	if rect.Dx() != 1000 || rect.Dy() != 3000 {
		t.Fatalf("unexpected crop %v", rect)
	}
	if !rectContains(rect, image.Pt(399, 399)) {
		t.Fatalf("crop %v lost the subject", rect)
	}
	if rect.Min.X > 16 || rect.Min.Y > 0 {
		t.Fatalf("got crop origin %v, want near (0,0)", rect.Min)
	}

	// The intermediate work image must stay within the documented bounds.
	work := smartWorkImage(src, rect, 100, 300)
	if work.Rect.Dx() > smartMaxWorkSide || work.Rect.Dy() > smartMaxWorkSide {
		t.Fatalf("work image too large: %v", work.Rect)
	}
	if int64(work.Rect.Dx())*int64(work.Rect.Dy()) > smartMaxWorkPixels {
		t.Fatalf("work image area too large: %v", work.Rect)
	}

	dst := SmartThumbnail(src, 100, 300, Box)
	if dst.Rect.Dx() != 100 || dst.Rect.Dy() != 300 {
		t.Fatalf("unexpected output size %v", dst.Rect)
	}
	if c := dst.NRGBAAt(5, 5); c.R != 0xff || c.G != 0 || c.B != 0 {
		t.Fatalf("subject pixel is %v, want red", c)
	}
	if c := dst.NRGBAAt(5, 290); c.R != 0xff || c.G != 0xff || c.B != 0xff {
		t.Fatalf("background pixel is %v, want white", c)
	}
}

func TestSmartDownscaleRectExact(t *testing.T) {
	// Exactly a 2x2 average for a 2x factor, alpha weighted.
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.SetNRGBA(0, 0, color.NRGBA{0xff, 0x00, 0x00, 0xff})
	src.SetNRGBA(1, 0, color.NRGBA{0x00, 0xff, 0x00, 0xff})
	src.SetNRGBA(0, 1, color.NRGBA{0x00, 0x00, 0xff, 0xff})
	src.SetNRGBA(1, 1, color.NRGBA{0xff, 0xff, 0xff, 0xff})

	got := smartDownscaleRect(src, src.Bounds(), 2, 2)
	want := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	want.Pix = []uint8{0x80, 0x80, 0x80, 0xff}
	if !compareNRGBA(got, want, 1) {
		t.Fatalf("got %v want %v", got.Pix, want.Pix)
	}

	// Transparent neighbor must not darken the opaque pixel.
	src.SetNRGBA(1, 0, color.NRGBA{0x00, 0x00, 0x00, 0x00})
	src.SetNRGBA(0, 1, color.NRGBA{0x00, 0x00, 0x00, 0x00})
	src.SetNRGBA(1, 1, color.NRGBA{0x00, 0x00, 0x00, 0x00})
	got = smartDownscaleRect(src, src.Bounds(), 2, 2)
	if c := got.NRGBAAt(0, 0); c != (color.NRGBA{0xff, 0x00, 0x00, 0x40}) {
		t.Fatalf("got %v want {255 0 0 64}", c)
	}

	// Non-zero source bounds are honored.
	shifted := image.NewNRGBA(image.Rect(10, 20, 12, 22))
	fillNRGBA(shifted, image.Rect(10, 20, 12, 22), red)
	got = smartDownscaleRect(shifted, shifted.Bounds(), 2, 2)
	if c := got.NRGBAAt(0, 0); c != red {
		t.Fatalf("got %v want red", c)
	}

	// The per-pixel fallback used for extremely large factors must agree
	// with the strip implementation.
	for fx := 1; fx <= 12; fx++ {
		for fy := 1; fy <= 12; fy++ {
			strip := smartDownscaleRect(src, src.Bounds(), fx, fy)
			pixel := smartDownscaleRectPixel(src, src.Bounds(), fx, fy)
			if !compareNRGBA(strip, pixel, 0) {
				t.Fatalf("fx=%d fy=%d: pixel fallback differs", fx, fy)
			}
		}
	}
}

func TestSmartWorkImageBounds(t *testing.T) {
	src := newSolidImage(image.Rect(0, 0, 9000, 9000), white)
	rect := image.Rect(0, 0, 9000, 9000)

	work := smartWorkImage(src, rect, 64, 64)
	if int64(work.Rect.Dx())*int64(work.Rect.Dy()) > smartMaxWorkPixels {
		t.Fatalf("work image area %d exceeds limit", work.Rect.Dx()*work.Rect.Dy())
	}

	// Small targets keep at least the target resolution available, so a
	// normal thumbnail does not go through the downsample path.
	small := newSolidImage(image.Rect(0, 0, 100, 100), white)
	work = smartWorkImage(small, image.Rect(0, 0, 100, 100), 50, 50)
	if work.Rect.Dx() != 100 || work.Rect.Dy() != 100 {
		t.Fatalf("unexpected work size %v", work.Rect)
	}
}

func TestSmartThumbnailDoesNotModifyInput(t *testing.T) {
	bounds := image.Rect(5, -7, 205, 93)
	src := newSolidImage(bounds, white)
	fillNRGBA(src, image.Rect(5, -7, 65, 53), black)
	fillNRGBA(src, image.Rect(150, 40, 190, 80), red)
	before := Clone(src)

	_ = SmartCropRect(src, 40, 40)
	_ = SmartThumbnail(src, 40, 40, Lanczos)

	if !compareBytes(src.Pix, before.Pix, 0) {
		t.Fatalf("source image was modified")
	}
	if src.Rect != bounds {
		t.Fatalf("source bounds changed: %v", src.Rect)
	}
}

// syntheticImage is an image.Image with a possibly huge logical size that
// computes its pixels on the fly, so tests can exercise the large-image path
// without allocating the full backing store. The 500x6000 strip at the top
// left is dark; everything else is white.
type syntheticImage struct {
	rect image.Rectangle
}

func (s syntheticImage) ColorModel() color.Model { return color.NRGBAModel }

func (s syntheticImage) Bounds() image.Rectangle { return s.rect }

func (s syntheticImage) At(x, y int) color.Color {
	if x-s.rect.Min.X < 500 && y-s.rect.Min.Y < 6000 {
		return color.NRGBA{0, 0, 0, 0xff}
	}
	return color.NRGBA{0xff, 0xff, 0xff, 0xff}
}

// bandImage is an image.Image with a huge logical size whose subject is a
// dark vertical band in the right-most 5% of the image, computed on the fly.
type bandImage struct {
	rect image.Rectangle
}

func (b bandImage) ColorModel() color.Model { return color.NRGBAModel }

func (b bandImage) Bounds() image.Rectangle { return b.rect }

func (b bandImage) At(x, y int) color.Color {
	if x-b.rect.Min.X > b.rect.Dx()*19/20 &&
		y-b.rect.Min.Y > b.rect.Dy()/3 && y-b.rect.Min.Y < b.rect.Dy()*2/3 {
		return color.NRGBA{0, 0, 0, 0xff}
	}
	return color.NRGBA{0xff, 0xff, 0xff, 0xff}
}

func TestSmartCropVeryWideImage(t *testing.T) {
	// A 200000x1000 source cannot be scanned row by row into a single
	// w*4 buffer in a bounded amount of memory; strips are used instead.
	src := bandImage{rect: image.Rect(0, 0, 200000, 1000)}

	rect := SmartCropRect(src, 100, 1000)
	if rect.Dy() != 1000 || rect.Dx() != 100 {
		t.Fatalf("unexpected crop %v", rect)
	}
	if rect.Max.X < 190000 {
		t.Fatalf("crop %v missed the right-side subject", rect)
	}

	dst := SmartThumbnail(src, 100, 1000, Box)
	if dst.Rect.Dx() != 100 || dst.Rect.Dy() != 1000 {
		t.Fatalf("unexpected output size %v", dst.Rect)
	}
	if c := dst.NRGBAAt(50, 500); c.R > 0x20 {
		t.Fatalf("center pixel is %v, want dark", c)
	}
}

func TestSmartCropHugeImageBoundedWork(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping huge image test in short mode")
	}

	// The image is computed on the fly; only bounded intermediates are
	// ever allocated.
	const (
		side    = 50000
		subject = 500
	)
	src := syntheticImage{rect: image.Rect(0, 0, side, side)}

	// A 2:1 target produces a full-width 50000x25000 window with 25000px
	// of vertical slack. The dark strip is 6000px tall, so only the window
	// at y=0 contains all of it and the top edge follows the subject.
	rect := SmartCropRect(src, 2000, 1000)
	if rect.Dx() != side || rect.Dy() != side/2 || rect.Min.X != 0 {
		t.Fatalf("unexpected crop %v", rect)
	}
	// The grid cell is 196px; the crop may be shifted by up to half a cell
	// from the scored window origin.
	if rect.Min.Y < 0 || rect.Min.Y > 98 {
		t.Fatalf("crop %v is too far from the top", rect)
	}

	dst := SmartThumbnail(src, 2000, 1000, Box)
	if dst.Rect.Dx() != 2000 || dst.Rect.Dy() != 1000 {
		t.Fatalf("unexpected output size %v", dst.Rect)
	}
	// The 500x6000 dark strip is downscaled by exactly 25x on both axes,
	// so it maps to an aligned 20x240 block of output pixels.
	dark, light := 0, 0
	for y := 0; y < 1000; y++ {
		for x := 0; x < 2000; x++ {
			if dst.NRGBAAt(x, y).R < 0x80 {
				dark++
			} else {
				light++
			}
		}
	}
	if dark < 20*230 || dark > 20*241 {
		t.Fatalf("unexpected dark pixel count %d", dark)
	}
	if light == 0 {
		t.Fatalf("expected a white background")
	}
}

func TestSmartCropFullyTransparent(t *testing.T) {
	// A fully transparent image has no saliency at all; it must use the
	// centered fallback and produce a fully transparent thumbnail.
	src := image.NewNRGBA(image.Rect(0, 0, 120, 80))
	rect := SmartCropRect(src, 60, 80)
	if want := image.Rect(30, 0, 90, 80); rect != want {
		t.Fatalf("got crop %v want %v", rect, want)
	}

	dst := SmartThumbnail(src, 60, 80, Box)
	if dst.Rect.Dx() != 60 || dst.Rect.Dy() != 80 {
		t.Fatalf("unexpected output size %v", dst.Rect)
	}
	for i := 0; i < len(dst.Pix); i += 4 {
		if dst.Pix[i+3] != 0 {
			t.Fatalf("pixel %d is not transparent: %v", i/4, dst.Pix[i:i+4])
		}
	}
}

func TestSmartCropExactAspectRatio(t *testing.T) {
	src := newSolidImage(image.Rect(-10, -20, 190, 80), white)
	rect := SmartCropRect(src, 100, 50)
	if rect != src.Bounds() {
		t.Fatalf("got crop %v want full bounds %v", rect, src.Bounds())
	}
	dst := SmartThumbnail(src, 100, 50, Box)
	if dst.Rect.Dx() != 100 || dst.Rect.Dy() != 50 {
		t.Fatalf("unexpected output size %v", dst.Rect)
	}
}

func TestSmartCropExtremeAspectSizes(t *testing.T) {
	// Very lopsided sources must never produce a crop that is larger than
	// the source or has a zero dimension.
	cases := []struct{ w, h int }{
		{100000, 1},
		{1, 100000},
		{99999, 2},
		{2, 99999},
		{3000, 1},
	}
	targets := []struct{ w, h int }{
		{1, 1}, {2, 1}, {1, 2}, {100, 100}, {1000, 3}, {3, 1000},
	}
	for _, c := range cases {
		src := newSolidImage(image.Rect(0, 0, c.w, c.h), white)
		for _, tgt := range targets {
			rect := SmartCropRect(src, tgt.w, tgt.h)
			if rect.Dx() < 1 || rect.Dy() < 1 {
				t.Fatalf("%dx%d -> %d: degenerate crop %v", c.w, c.h, tgt.w, rect)
			}
			if !rect.In(src.Bounds()) {
				t.Fatalf("%dx%d -> %d: crop %v outside bounds", c.w, c.h, tgt.w, rect)
			}
			dst := SmartThumbnail(src, tgt.w, tgt.h, Box)
			if dst.Rect.Dx() != tgt.w || dst.Rect.Dy() != tgt.h {
				t.Fatalf("%dx%d -> %d: output %v", c.w, c.h, tgt.w, dst.Rect)
			}
		}
	}
}

func TestSmartCropOnePixelImage(t *testing.T) {
	src := newSolidImage(image.Rect(0, 0, 1, 1), red)
	rect := SmartCropRect(src, 10, 10)
	if rect.Dx() != 1 || rect.Dy() != 1 {
		t.Fatalf("unexpected crop %v", rect)
	}
	dst := SmartThumbnail(src, 10, 10, Box)
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if c := dst.NRGBAAt(x, y); c != red {
				t.Fatalf("got %v at (%d,%d) want red", c, x, y)
			}
		}
	}
}

func TestSmartCropScoreIsMaximal(t *testing.T) {
	// Two subjects at opposite corners; only one 160x160 window can
	// contain either whole subject, so the larger one must win.
	src := newSolidImage(image.Rect(0, 0, 200, 160), white)
	fillNRGBA(src, image.Rect(0, 0, 10, 10), black)
	fillNRGBA(src, image.Rect(150, 110, 190, 150), red)

	rect := SmartCropRect(src, 200, 160)
	if !rectContains(rect, image.Pt(170, 130)) {
		t.Fatalf("crop %v does not include the larger subject", rect)
	}
}
