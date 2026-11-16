package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

// makeQuantTestImage builds a zero-origin image from row-major colors.
func makeQuantTestImage(w, h int, colors []color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, colors[y*w+x])
		}
	}
	return img
}

// quantPair is a test-friendly representation of one palette entry.
type quantPair struct{ r, g, b, a uint8 }

func quantPairs(p color.Palette) []quantPair {
	out := make([]quantPair, len(p))
	for i, c := range p {
		n := color.NRGBAModel.Convert(c).(color.NRGBA)
		out[i] = quantPair{n.R, n.G, n.B, n.A}
	}
	return out
}

// assertValidPalette checks the invariants every palette must satisfy:
// no nil entries, no duplicates, and at most maxLen colors.
func assertValidPalette(t *testing.T, p color.Palette, maxLen int) {
	t.Helper()
	if len(p) > maxLen {
		t.Fatalf("palette has %d colors, want at most %d", len(p), maxLen)
	}
	seen := make(map[quantPair]bool, len(p))
	for i, c := range p {
		if c == nil {
			t.Fatalf("palette entry %d is nil", i)
		}
		q := quantPairs(color.Palette{c})[0]
		if seen[q] {
			t.Fatalf("duplicate palette entry %v at index %d: %v", q, i, quantPairs(p))
		}
		seen[q] = true
	}
}

func TestAdaptivePaletteFourColors(t *testing.T) {
	red := color.NRGBA{0xff, 0x00, 0x00, 0xff}
	green := color.NRGBA{0x00, 0xff, 0x00, 0xff}
	blue := color.NRGBA{0x00, 0x00, 0xff, 0xff}
	yellow := color.NRGBA{0xff, 0xff, 0x00, 0xff}

	colors := make([]color.NRGBA, 0, 8*8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			switch (x/2 + y/2) % 4 {
			case 0:
				colors = append(colors, red)
			case 1:
				colors = append(colors, green)
			case 2:
				colors = append(colors, blue)
			default:
				colors = append(colors, yellow)
			}
		}
	}
	img := makeQuantTestImage(8, 8, colors)

	got := AdaptivePalette(img, 4)
	assertValidPalette(t, got, 4)
	if len(got) != 4 {
		t.Fatalf("got %d colors, want 4: %v", len(got), quantPairs(got))
	}

	want := map[quantPair]bool{
		{0xff, 0x00, 0x00, 0xff}: true,
		{0x00, 0xff, 0x00, 0xff}: true,
		{0x00, 0x00, 0xff, 0xff}: true,
		{0xff, 0xff, 0x00, 0xff}: true,
	}
	for _, q := range quantPairs(got) {
		if !want[q] {
			t.Fatalf("unexpected palette color %v, want %v", q, want)
		}
		delete(want, q)
	}
	if len(want) != 0 {
		t.Fatalf("missing palette colors %v", want)
	}
}

func TestAdaptivePaletteFewerColorsThanBudget(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.SetNRGBA(0, 0, color.NRGBA{10, 20, 30, 0xff})
	img.SetNRGBA(1, 0, color.NRGBA{10, 20, 30, 0xff})
	img.SetNRGBA(2, 0, color.NRGBA{10, 20, 30, 0xff})
	img.SetNRGBA(0, 1, color.NRGBA{40, 50, 60, 0xff})
	img.SetNRGBA(1, 1, color.NRGBA{40, 50, 60, 0xff})
	img.SetNRGBA(2, 1, color.NRGBA{40, 50, 60, 0xff})

	got := AdaptivePalette(img, 16)
	assertValidPalette(t, got, 16)
	if len(got) != 2 {
		t.Fatalf("got %d colors, want 2: %v", len(got), quantPairs(got))
	}
	// The palette is ordered by descending pixel count; both colors are
	// equally frequent, so the tie is broken by the RGB value.
	want := []quantPair{{10, 20, 30, 0xff}, {40, 50, 60, 0xff}}
	gotPairs := quantPairs(got)
	for i := range want {
		if gotPairs[i] != want[i] {
			t.Fatalf("palette[%d] = %v, want %v", i, gotPairs[i], want[i])
		}
	}
}

func TestAdaptivePaletteDominantColorPreserved(t *testing.T) {
	// 75% of the pixels are one color; the remaining 25% form a gradient.
	// The dominant color must survive exactly even with a tiny budget.
	w, h := 40, 40
	dominant := color.NRGBA{0x12, 0x34, 0x56, 0xff}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < 30 {
				img.SetNRGBA(x, y, dominant)
			} else {
				img.SetNRGBA(x, y, color.NRGBA{uint8(200 + x), uint8(y * 6), uint8(x * 6), 0xff})
			}
		}
	}

	for _, n := range []int{2, 4, 8} {
		got := AdaptivePalette(img, n)
		assertValidPalette(t, got, n)
		if len(got) != n {
			t.Fatalf("n=%d: got %d colors, want %d: %v", n, len(got), n, quantPairs(got))
		}
		if len(got) == 0 || quantPairs(got)[0] != (quantPair{0x12, 0x34, 0x56, 0xff}) {
			t.Fatalf("n=%d: dominant color not first: %v", n, quantPairs(got))
		}
	}
}

func TestAdaptivePaletteTransparentIsolation(t *testing.T) {
	// Transparent pixels have RGB values that must not appear in the palette.
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if x == 0 {
				img.SetNRGBA(x, y, color.NRGBA{0xfa, 0x07, 0x09, 0x00})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{0x00, 0x00, 0xff, 0xff})
			}
		}
	}

	got := AdaptivePalette(img, 8)
	assertValidPalette(t, got, 8)
	if len(got) != 2 {
		t.Fatalf("got %d colors, want 2: %v", len(got), quantPairs(got))
	}
	if quantPairs(got)[0] != (quantPair{0, 0, 0, 0}) {
		t.Fatalf("palette[0] = %v, want transparent", quantPairs(got)[0])
	}
	for _, q := range quantPairs(got) {
		if q.r == 0xfa && q.g == 0x07 && q.b == 0x09 {
			t.Fatalf("transparent pixel RGB leaked into the palette: %v", quantPairs(got))
		}
	}

	// A caller-supplied transparent entry is reused instead of duplicated.
	pre := make(color.Palette, 1, 8)
	pre[0] = color.NRGBA{0, 0, 0, 0}
	got = PaletteQuantizer{NumColors: 8}.Quantize(pre, img)
	assertValidPalette(t, got, 8)
	if len(got) != 2 {
		t.Fatalf("got %d colors, want 2: %v", len(got), quantPairs(got))
	}
}

func TestAdaptivePaletteTransparentSmallBudget(t *testing.T) {
	// With a budget of one color, visible pixels win over transparency so
	// the image does not become entirely transparent.
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 0})
	img.SetNRGBA(1, 0, color.NRGBA{1, 2, 3, 0xff})

	got := AdaptivePalette(img, 1)
	assertValidPalette(t, got, 1)
	if len(got) != 1 || quantPairs(got)[0] != (quantPair{1, 2, 3, 0xff}) {
		t.Fatalf("got %v, want the visible color", quantPairs(got))
	}
}

func TestAdaptivePaletteAllTransparent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 100), uint8(y * 100), 0x55, 0x00})
		}
	}
	got := AdaptivePalette(img, 16)
	assertValidPalette(t, got, 16)
	if len(got) != 1 || quantPairs(got)[0] != (quantPair{0, 0, 0, 0}) {
		t.Fatalf("got %v, want a single transparent color", quantPairs(got))
	}
}

func TestAdaptivePaletteEmptyImage(t *testing.T) {
	for _, img := range []image.Image{
		image.NewNRGBA(image.Rect(0, 0, 0, 0)),
		image.NewNRGBA(image.Rect(5, 7, 5, 7)),
	} {
		got := AdaptivePalette(img, 4)
		assertValidPalette(t, got, 4)
		if len(got) != 1 || quantPairs(got)[0] != (quantPair{0, 0, 0, 0xff}) {
			t.Fatalf("got %v, want a single opaque black color", quantPairs(got))
		}
	}

	// A nil image is handled like an empty one.
	got := AdaptivePalette(nil, 4)
	assertValidPalette(t, got, 4)
	if len(got) != 1 {
		t.Fatalf("got %v, want a single usable color", quantPairs(got))
	}
}

func TestPaletteQuantizerNumColorsBounds(t *testing.T) {
	img := makeQuantTestImage(2, 2, []color.NRGBA{
		{0, 0, 0, 0xff}, {0xff, 0, 0, 0xff},
		{0, 0xff, 0, 0xff}, {0, 0, 0xff, 0xff},
	})
	for _, n := range []int{-100, 0, 1, 256, 257, 1000} {
		got := PaletteQuantizer{NumColors: n}.Quantize(make(color.Palette, 0, 256), img)
		wantMax := n
		if wantMax < 1 || wantMax > 256 {
			wantMax = 256
		}
		assertValidPalette(t, got, wantMax)
		if len(got) == 0 {
			t.Fatalf("n=%d: got an empty palette", n)
		}
	}
}

func TestPaletteQuantizerRespectsCapacity(t *testing.T) {
	img := makeQuantTestImage(4, 1, []color.NRGBA{
		{0, 0, 0, 0xff}, {0xff, 0, 0, 0xff}, {0, 0xff, 0, 0xff}, {0, 0, 0xff, 0xff},
	})
	for _, capacity := range []int{0, 1, 2, 4} {
		// The quantizer may append to p but must never exceed its capacity.
		got := PaletteQuantizer{NumColors: 256}.Quantize(make(color.Palette, 0, capacity), img)
		if len(got) > capacity {
			t.Fatalf("capacity %d: got %d colors", capacity, len(got))
		}
		assertValidPalette(t, got, capacity)
	}
}

func TestPaletteQuantizerDeterministic(t *testing.T) {
	// A gradient with more distinct colors than the exact histogram limit
	// exercises the fallback path, while also including transparent pixels.
	img := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			if (x+y)%7 == 0 {
				img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 0x99, 0x00})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
			}
		}
	}

	first := quantPairs(AdaptivePalette(img, 64))
	for i := 0; i < 5; i++ {
		got := quantPairs(AdaptivePalette(img, 64))
		if len(got) != len(first) {
			t.Fatalf("run %d: got %d colors, want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d: color %d = %v, want %v", i, j, got[j], first[j])
			}
		}
	}
}

// testImageWrapper hides the concrete image type so the quantizer has to use
// the generic scanner path.
type testImageWrapper struct {
	img image.Image
}

func (w testImageWrapper) ColorModel() color.Model { return w.img.ColorModel() }
func (w testImageWrapper) Bounds() image.Rectangle { return w.img.Bounds() }
func (w testImageWrapper) At(x, y int) color.Color { return w.img.At(x, y) }

func TestAdaptivePaletteNonZeroBoundsAndImplementations(t *testing.T) {
	w, h := 16, 12
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x * 16), uint8(y * 20), uint8((x * y) % 256), 0xff})
		}
	}

	want := quantPairs(AdaptivePalette(src, 8))

	// The same pixels with a non-zero origin.
	offset := image.NewNRGBA(image.Rect(11, 23, 11+w, 23+h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			offset.SetNRGBA(11+x, 23+y, src.NRGBAAt(x, y))
		}
	}
	got := quantPairs(AdaptivePalette(offset, 8))
	if len(got) != len(want) {
		t.Fatalf("non-zero bounds: got %d colors, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("non-zero bounds: color %d = %v, want %v", i, got[i], want[i])
		}
	}

	// A different image.Image implementation with non-zero bounds.
	wrapped := testImageWrapper{img: offset}
	got = quantPairs(AdaptivePalette(wrapped, 8))
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("wrapped image: color %d = %v, want %v", i, got[i], want[i])
		}
	}

	// A sub-image view of a larger image.
	parent := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			parent.SetNRGBA(x, y, color.NRGBA{0xaa, 0xbb, 0xcc, 0xff})
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			parent.SetNRGBA(5+x, 6+y, src.NRGBAAt(x, y))
		}
	}
	sub := parent.SubImage(image.Rect(5, 6, 5+w, 6+h))
	got = quantPairs(AdaptivePalette(sub, 8))
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sub-image: color %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestAdaptivePaletteGrayscaleAndPaletted(t *testing.T) {
	gray := image.NewGray(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			gray.SetGray(x, y, color.Gray{Y: uint8(x * 16)})
		}
	}
	grayPalette := AdaptivePalette(gray, 4)
	assertValidPalette(t, grayPalette, 4)
	for _, q := range quantPairs(grayPalette) {
		if q.r != q.g || q.g != q.b || q.a != 0xff {
			t.Fatalf("grayscale palette color %v is not opaque gray", q)
		}
	}

	paletted := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{
		color.NRGBA{0, 0, 0, 0},
		color.NRGBA{0xff, 0, 0, 0xff},
		color.NRGBA{0, 0xff, 0, 0xff},
	})
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			paletted.SetColorIndex(x, y, uint8((x+y)%3))
		}
	}
	got := AdaptivePalette(paletted, 8)
	assertValidPalette(t, got, 8)
	if len(got) != 3 {
		t.Fatalf("got %d colors, want 3: %v", len(got), quantPairs(got))
	}
	if quantPairs(got)[0] != (quantPair{0, 0, 0, 0}) {
		t.Fatalf("palette[0] = %v, want transparent", quantPairs(got)[0])
	}
}

func TestAdaptivePaletteGradientError(t *testing.T) {
	const w, h = 128, 128
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 2), uint8(y * 2), uint8((x + y) % 256), 0xff})
		}
	}

	// The error must shrink as the palette grows.
	prevMean := -1.0
	for _, n := range []int{4, 8, 16, 32, 64} {
		pal := AdaptivePalette(img, n)
		assertValidPalette(t, pal, n)
		if len(pal) != n {
			t.Fatalf("n=%d: got %d colors", n, len(pal))
		}

		var sum float64
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := img.NRGBAAt(x, y)
				sum += nearestPaletteDistance(c, pal)
			}
		}
		mean := sum / float64(w*h)
		if prevMean >= 0 && mean >= prevMean {
			t.Fatalf("n=%d: mean error %.3f did not decrease from %.3f", n, mean, prevMean)
		}
		prevMean = mean
	}

	// Exact colors must be reproduced with zero error when the budget is
	// large enough.
	exact := makeFourColorImage()
	pal := AdaptivePalette(exact, 4)
	for i := 0; i < len(exact.Pix); i += 4 {
		c := exact.NRGBAAt(i/4%8, i/4/8)
		if d := nearestPaletteDistance(c, pal); d != 0 {
			t.Fatalf("color %v has quantization distance %v, want 0", c, d)
		}
	}
}

// nearestPaletteDistance returns the Euclidean distance from c to its nearest
// entry in p.
func nearestPaletteDistance(c color.NRGBA, p color.Palette) float64 {
	best := -1.0
	for _, q := range p {
		n := color.NRGBAModel.Convert(q).(color.NRGBA)
		dr := float64(c.R) - float64(n.R)
		dg := float64(c.G) - float64(n.G)
		db := float64(c.B) - float64(n.B)
		d := dr*dr + dg*dg + db*db
		if best < 0 || d < best {
			best = d
		}
	}
	if best < 0 {
		return 0
	}
	// Integer square root approximation is enough for comparisons here.
	return best
}

// makeFourColorImage builds the 8x8 four-color test image.
func makeFourColorImage() *image.NRGBA {
	pal := []color.NRGBA{
		{0xff, 0x00, 0x00, 0xff},
		{0x00, 0xff, 0x00, 0xff},
		{0x00, 0x00, 0xff, 0xff},
		{0xff, 0xff, 0x00, 0xff},
	}
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, pal[(x/2+y/2)%4])
		}
	}
	return img
}

func TestAdaptivePaletteEquivalentImplementations(t *testing.T) {
	// The same opaque pixels must yield the same palette whether they are
	// stored as NRGBA, RGBA, Gray or Paletted images.
	colors := []color.NRGBA{
		{0x10, 0x20, 0x30, 0xff},
		{0x40, 0x50, 0x60, 0xff},
		{0x70, 0x80, 0x90, 0xff},
		{0xa0, 0xb0, 0xc0, 0xff},
	}
	w, h := 8, 8
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nrgba.SetNRGBA(x, y, colors[(x/2+y/2)%4])
		}
	}
	want := quantPairs(AdaptivePalette(nrgba, 4))

	rgba := image.NewRGBA(nrgba.Rect)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := nrgba.NRGBAAt(x, y)
			rgba.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A})
		}
	}
	if got := quantPairs(AdaptivePalette(rgba, 4)); !equalQuantPairs(got, want) {
		t.Fatalf("RGBA palette %v, want %v", got, want)
	}

	paletted := image.NewPaletted(nrgba.Rect, color.Palette{
		colors[0], colors[1], colors[2], colors[3],
	})
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			paletted.SetColorIndex(x, y, uint8((x/2+y/2)%4))
		}
	}
	if got := quantPairs(AdaptivePalette(paletted, 4)); !equalQuantPairs(got, want) {
		t.Fatalf("Paletted palette %v, want %v", got, want)
	}

	// A gray image must keep gray, opaque colors.
	gray := image.NewGray(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			gray.SetGray(x, y, color.Gray{Y: uint8(x * 60)})
		}
	}
	for _, q := range quantPairs(AdaptivePalette(gray, 4)) {
		if q.r != q.g || q.g != q.b || q.a != 0xff {
			t.Fatalf("gray palette color %v is not opaque gray", q)
		}
	}
}

func equalQuantPairs(a, b []quantPair) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAdaptivePalettePartialAlphaVisible(t *testing.T) {
	// Pixels that are not fully transparent are visible and contribute their
	// (non-premultiplied) RGB values to the palette.
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, color.NRGBA{0xff, 0x00, 0x00, 0x00}) // fully transparent
	img.SetNRGBA(1, 0, color.NRGBA{0x0a, 0x14, 0x1e, 0x01})
	img.SetNRGBA(2, 0, color.NRGBA{0x0a, 0x14, 0x1e, 0x80})

	got := AdaptivePalette(img, 4)
	assertValidPalette(t, got, 4)
	if len(got) != 2 {
		t.Fatalf("got %d colors, want 2: %v", len(got), quantPairs(got))
	}
	pairs := quantPairs(got)
	if pairs[0] != (quantPair{0, 0, 0, 0}) {
		t.Fatalf("palette[0] = %v, want transparent", pairs[0])
	}
	if pairs[1] != (quantPair{0x0a, 0x14, 0x1e, 0xff}) {
		t.Fatalf("palette[1] = %v, want the semi-transparent pixel color", pairs[1])
	}
}

func TestPaletteQuantizerBoundedMemory(t *testing.T) {
	// An image with more distinct colors than the exact histogram limit must
	// use the fixed-size fallback histogram, so a repeated run allocates a
	// bounded amount of memory regardless of the image size.
	img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
		}
	}
	// The image has far more than maxExactQuantColors distinct colors.
	distinct := make(map[uint32]struct{})
	for i := 0; i < len(img.Pix); i += 4 {
		distinct[uint32(img.Pix[i])<<16|uint32(img.Pix[i+1])<<8|uint32(img.Pix[i+2])] = struct{}{}
		if len(distinct) > maxExactQuantColors {
			break
		}
	}
	if len(distinct) <= maxExactQuantColors {
		t.Fatalf("test image has only %d distinct colors, need more than %d", len(distinct), maxExactQuantColors)
	}

	// The fallback histogram has a fixed size, so the allocated bytes must
	// not grow with the image area. Compare a 128x128 image with a 512x512
	// image that have the same color pattern.
	small := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			small.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
		}
	}
	large := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			large.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
		}
	}

	smallBytes := quantizeBytesPerOp(small)
	largeBytes := quantizeBytesPerOp(large)
	if largeBytes > smallBytes*3/2+1<<20 {
		t.Fatalf("quantizer allocated %d bytes for 512x512, %d bytes for 128x128: memory grows with image size",
			largeBytes, smallBytes)
	}
	// A fixed histogram plus the palette and sort scratch space stays well
	// below a few megabytes.
	const maxBytes = 8 << 20
	if largeBytes > maxBytes {
		t.Fatalf("quantizer allocated %d bytes, want at most %d", largeBytes, maxBytes)
	}
}

// quantizeBytesPerOp returns the average bytes allocated by one
// AdaptivePalette call on img.
func quantizeBytesPerOp(img image.Image) uint64 {
	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			AdaptivePalette(img, 256)
		}
	})
	return uint64(res.AllocedBytesPerOp())
}

func TestPaletteQuantizerConcurrent(t *testing.T) {
	img := makeFourColorImage()
	want := quantPairs(AdaptivePalette(img, 4))

	done := make(chan []quantPair, 8)
	for i := 0; i < cap(done); i++ {
		go func() {
			done <- quantPairs(AdaptivePalette(img, 4))
		}()
	}
	for i := 0; i < cap(done); i++ {
		got := <-done
		if len(got) != len(want) {
			t.Fatalf("goroutine %d: got %d colors, want %d", i, len(got), len(want))
		}
		for j := range got {
			if got[j] != want[j] {
				t.Fatalf("goroutine %d: color %d = %v, want %v", i, j, got[j], want[j])
			}
		}
	}
}

func TestPaletteQuantizerGIFRoundTrip(t *testing.T) {
	testCases := []struct {
		name    string
		img     image.Image
		colors  int
		opaque  bool
		wantGIF int
	}{
		{
			name:    "four colors",
			img:     makeFourColorImage(),
			colors:  4,
			opaque:  true,
			wantGIF: 4,
		},
		{
			name: "transparent",
			img: func() image.Image {
				img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
				for y := 0; y < 8; y++ {
					for x := 0; x < 8; x++ {
						if x < 2 {
							img.SetNRGBA(x, y, color.NRGBA{0xee, 0x11, 0x22, 0x00})
						} else {
							img.SetNRGBA(x, y, color.NRGBA{0x00, 0x00, 0xff, 0xff})
						}
					}
				}
				return img
			}(),
			colors:  8,
			opaque:  false,
			wantGIF: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := Encode(&buf, tc.img, GIF,
				GIFNumColors(tc.colors),
				GIFQuantizer(NewPaletteQuantizer(tc.colors)),
			)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			decoded, err := gif.Decode(&buf)
			if err != nil {
				t.Fatalf("gif.Decode: %v", err)
			}
			pm, ok := decoded.(*image.Paletted)
			if !ok {
				t.Fatalf("decoded image has type %T, want *image.Paletted", decoded)
			}
			if len(pm.Palette) != tc.wantGIF {
				t.Fatalf("encoded palette has %d colors, want %d: %v", len(pm.Palette), tc.wantGIF, pm.Palette)
			}
			if pm.Bounds() != tc.img.Bounds() {
				t.Fatalf("decoded bounds %v, want %v", pm.Bounds(), tc.img.Bounds())
			}

			if tc.opaque {
				// Opaque test images must round-trip exactly, because their
				// colors are present verbatim in the palette.
				for y := tc.img.Bounds().Min.Y; y < tc.img.Bounds().Max.Y; y++ {
					for x := tc.img.Bounds().Min.X; x < tc.img.Bounds().Max.X; x++ {
						want := color.NRGBAModel.Convert(tc.img.At(x, y)).(color.NRGBA)
						got := color.NRGBAModel.Convert(pm.At(x, y)).(color.NRGBA)
						if got != want {
							t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
						}
					}
				}
			} else {
				// Transparent pixels must decode as fully transparent.
				for y := 0; y < 8; y++ {
					for x := 0; x < 2; x++ {
						if _, _, _, a := pm.At(x, y).RGBA(); a != 0 {
							t.Fatalf("pixel (%d,%d) alpha = %d, want 0", x, y, a)
						}
					}
				}
			}
		})
	}
}

func TestPaletteQuantizerGIFDirectUse(t *testing.T) {
	img := makeFourColorImage()
	var buf bytes.Buffer
	// Use the quantizer directly with image/gif, without imaging.Encode.
	err := gif.Encode(&buf, img, &gif.Options{
		NumColors: 4,
		Quantizer: NewPaletteQuantizer(4),
	})
	if err != nil {
		t.Fatalf("gif.Encode: %v", err)
	}
	decoded, err := gif.Decode(&buf)
	if err != nil {
		t.Fatalf("gif.Decode: %v", err)
	}
	pm := decoded.(*image.Paletted)
	if len(pm.Palette) != 4 {
		t.Fatalf("got %d colors, want 4", len(pm.Palette))
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			want := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			got := color.NRGBAModel.Convert(pm.At(x, y)).(color.NRGBA)
			if got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}

	// Using GIFQuantizer with the default number of palette entries works too.
	buf.Reset()
	err = Encode(&buf, img, GIF, GIFQuantizer(NewPaletteQuantizer(256)))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err = gif.Decode(&buf)
	if err != nil {
		t.Fatalf("gif.Decode: %v", err)
	}
	if pm := decoded.(*image.Paletted); len(pm.Palette) != 4 {
		t.Fatalf("got %d colors, want 4: %v", len(pm.Palette), pm.Palette)
	}
}

func TestPaletteQuantizerExistingPaletteAppended(t *testing.T) {
	img := makeFourColorImage()
	existing := make(color.Palette, 1, 4)
	existing[0] = color.NRGBA{0x01, 0x02, 0x03, 0xff}
	got := PaletteQuantizer{NumColors: 4}.Quantize(existing, img)
	if len(got) != 4 {
		t.Fatalf("got %d colors, want 4: %v", len(got), quantPairs(got))
	}
	if quantPairs(got)[0] != (quantPair{0x01, 0x02, 0x03, 0xff}) {
		t.Fatalf("existing color was not preserved: %v", quantPairs(got))
	}
	assertValidPalette(t, got, 4)
}

func TestPaletteQuantizerSkipsExistingColorsAndNil(t *testing.T) {
	img := makeQuantTestImage(4, 1, []color.NRGBA{
		{0x10, 0x20, 0x30, 0xff},
		{0x40, 0x50, 0x60, 0xff},
		{0x70, 0x80, 0x90, 0xff},
		{0xa0, 0xb0, 0xc0, 0xff},
	})

	// Preload one of the image colors verbatim; the quantizer must not add a
	// duplicate of it.
	pre := make(color.Palette, 1, 8)
	pre[0] = color.NRGBA{0x40, 0x50, 0x60, 0xff}
	got := PaletteQuantizer{NumColors: 4}.Quantize(pre, img)
	assertValidPalette(t, got, 8)
	if len(got) != 4 {
		t.Fatalf("got %d colors, want 4: %v", len(got), quantPairs(got))
	}
	if quantPairs(got)[0] != (quantPair{0x40, 0x50, 0x60, 0xff}) {
		t.Fatalf("preloaded color was not preserved: %v", quantPairs(got))
	}

	// A nil entry in the caller palette must not panic or be duplicated.
	preNil := make(color.Palette, 1, 8)
	preNil[0] = nil
	got = PaletteQuantizer{NumColors: 4}.Quantize(preNil, img)
	assertValidPalette(t, got, 8)
	if len(got) != 4 {
		t.Fatalf("got %d colors, want 4: %v", len(got), quantPairs(got))
	}

	// Nil entries mixed with real colors are dropped, and the real colors
	// keep their order.
	preMixed := make(color.Palette, 3, 8)
	preMixed[0] = nil
	preMixed[1] = color.NRGBA{0x10, 0x20, 0x30, 0xff}
	preMixed[2] = nil
	got = PaletteQuantizer{NumColors: 4}.Quantize(preMixed, img)
	assertValidPalette(t, got, 8)
	if quantPairs(got)[0] != (quantPair{0x10, 0x20, 0x30, 0xff}) {
		t.Fatalf("surviving color was not kept first: %v", quantPairs(got))
	}
	// The caller's palette slice must not be modified in place.
	if preMixed[0] != nil || preMixed[2] != nil {
		t.Fatalf("caller palette was mutated: %v", preMixed)
	}
}

func TestAdaptivePaletteInvalidNumColors(t *testing.T) {
	img := makeFourColorImage()
	for _, n := range []int{-1, 0, 257} {
		got := AdaptivePalette(img, n)
		assertValidPalette(t, got, 256)
		if len(got) != 4 {
			t.Fatalf("n=%d: got %d colors, want 4", n, len(got))
		}
	}
}

func TestSplitQuantBoxTieBreakers(t *testing.T) {
	// Colors share R and G and differ only in B; with R as the widest
	// channel the remaining field comparisons must still order them.
	samples := []weightedColor{
		{r: 0, g: 10, b: 30, count: 1},
		{r: 0, g: 10, b: 10, count: 1},
		{r: 100, g: 10, b: 20, count: 1},
		{r: 100, g: 10, b: 5, count: 1},
	}
	if _, ok := splitQuantBox(samples, quantBox{lo: 0, hi: len(samples), count: 4}); !ok {
		t.Fatalf("expected a split")
	}
	for i := 1; i < 2; i++ {
		if samples[i-1].b > samples[i].b && samples[i-1].r == samples[i].r {
			t.Fatalf("samples not ordered by B for equal R: %v", samples)
		}
	}

	// Colors share B and R and differ only in G; with G as the widest
	// channel the remaining comparisons must order them.
	samples = []weightedColor{
		{r: 10, g: 0, b: 5, count: 1},
		{r: 10, g: 0, b: 50, count: 1},
		{r: 10, g: 100, b: 5, count: 1},
		{r: 10, g: 100, b: 50, count: 1},
	}
	if _, ok := splitQuantBox(samples, quantBox{lo: 0, hi: len(samples), count: 4}); !ok {
		t.Fatalf("expected a split")
	}
}

func TestSplitQuantBoxDuplicateColorGuard(t *testing.T) {
	// Two samples that are not deduplicated but have the same RGB value have
	// no color range to split; the function must report that instead of
	// producing an empty half.
	samples := []weightedColor{
		{r: 5, g: 5, b: 5, count: 1},
		{r: 5, g: 5, b: 5, count: 1},
	}
	if split, ok := splitQuantBox(samples, quantBox{lo: 0, hi: 2, count: 2}); ok {
		t.Fatalf("expected no split, got split=%d", split)
	}
}

func TestBuildQuantPaletteManySmallColors(t *testing.T) {
	// Many colors with equal weight: no color reaches the reservation
	// threshold, so the whole budget is produced by the cut.
	colors := make([]weightedColor, 0, 100)
	for i := 0; i < 100; i++ {
		colors = append(colors, weightedColor{r: uint8(i * 2), g: uint8(255 - i*2), b: uint8(i), count: 1})
	}
	got := buildQuantPalette(colors, 10)
	if len(got) != 10 {
		t.Fatalf("got %d colors, want 10", len(got))
	}
	seen := make(map[quantPair]bool, len(got))
	for _, c := range got {
		q := quantPair{c.r, c.g, c.b, 0xff}
		if seen[q] {
			t.Fatalf("duplicate color %v", q)
		}
		seen[q] = true
	}
}

func TestBuildQuantPaletteReservedColors(t *testing.T) {
	// One color holds 60% of the pixels, so it is reserved verbatim; the
	// remaining budget is filled from the rest.
	colors := []weightedColor{
		{r: 10, g: 20, b: 30, count: 600},
		{r: 200, g: 0, b: 0, count: 100},
		{r: 0, g: 200, b: 0, count: 100},
		{r: 0, g: 0, b: 200, count: 100},
		{r: 200, g: 200, b: 0, count: 100},
	}
	got := buildQuantPalette(colors, 3)
	if len(got) != 3 {
		t.Fatalf("got %d colors, want 3: %v", len(got), got)
	}
	if got[0].r != 10 || got[0].g != 20 || got[0].b != 30 {
		t.Fatalf("dominant color was not reserved verbatim: %v", got)
	}
}

func TestBuildQuantPaletteSingleColorBox(t *testing.T) {
	// Only one distinct color: no split is possible, and the palette must
	// still contain it rather than becoming empty.
	got := buildQuantPalette([]weightedColor{{r: 1, g: 2, b: 3, count: 5}}, 8)
	if len(got) != 1 || got[0].r != 1 || got[0].g != 2 || got[0].b != 3 {
		t.Fatalf("got %v, want the single input color", got)
	}

	// Duplicate samples are merged before the cut.
	got = buildQuantPalette([]weightedColor{
		{r: 9, g: 9, b: 9, count: 3},
		{r: 9, g: 9, b: 9, count: 4},
	}, 4)
	if len(got) != 1 || got[0].count != 7 {
		t.Fatalf("got %v, want one merged color with count 7", got)
	}
}

func TestBuildQuantPaletteFillsFromRest(t *testing.T) {
	// A reserved color plus fewer remaining colors than the leftover budget
	// must append the rest verbatim rather than cutting them.
	colors := []weightedColor{
		{r: 1, g: 1, b: 1, count: 1000},
		{r: 2, g: 2, b: 2, count: 1},
		{r: 3, g: 3, b: 3, count: 1},
	}
	got := buildQuantPalette(colors, 4)
	if len(got) != 3 {
		t.Fatalf("got %d colors, want 3: %v", len(got), got)
	}
	if got[0].r != 1 {
		t.Fatalf("reserved color should come first: %v", got)
	}
}

func TestSplitQuantBoxGuard(t *testing.T) {
	samples := []weightedColor{{r: 1, g: 2, b: 3, count: 1}}
	if _, ok := splitQuantBox(samples, quantBox{lo: 0, hi: 1, count: 1}); ok {
		t.Fatalf("expected no split for a box with fewer than two samples")
	}
}

func TestSplitQuantBoxChannelSelection(t *testing.T) {
	samples := []weightedColor{
		{r: 0, g: 100, b: 0, count: 1},
		{r: 200, g: 0, b: 0, count: 1},
		{r: 0, g: 200, b: 0, count: 1},
		{r: 100, g: 50, b: 200, count: 1},
	}
	split, ok := splitQuantBox(samples, quantBox{lo: 0, hi: len(samples), count: 4})
	if !ok {
		t.Fatalf("expected a split")
	}
	if split <= 0 || split >= len(samples) {
		t.Fatalf("split %d is out of range", split)
	}

	// A box with a single distinct color cannot be split.
	if _, ok := splitQuantBox(samples[:1], quantBox{lo: 0, hi: 1, count: 1}); ok {
		t.Fatalf("expected no split for a single-color box")
	}
}

func TestMedianCutExhausted(t *testing.T) {
	// Asking for more boxes than distinct colors must stop instead of
	// looping or emitting empty boxes.
	got := medianCut([]weightedColor{
		{r: 0, g: 0, b: 0, count: 1},
		{r: 255, g: 255, b: 255, count: 1},
	}, 16)
	if len(got) != 2 {
		t.Fatalf("got %d colors, want 2: %v", len(got), got)
	}
}

func BenchmarkAdaptivePalette(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AdaptivePalette(img, 256)
	}
}
