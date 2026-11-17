package imaging

import (
	"image"
	"image/color"
	"runtime"
	"strconv"
	"testing"
)

// mipmapTestDelta is the allowed per-channel difference when comparing
// computed levels. Results are exact on amd64; other architectures may round
// floating-point operations slightly differently (see compareNRGBAGolden).
var mipmapTestDelta = func() int {
	if runtime.GOARCH == "amd64" {
		return 0
	}
	return 1
}()

// newTestNRGBA builds a zero-origin NRGBA image from a row-major list of
// pixels. Every row must contain the same number of pixels.
func newTestNRGBA(t *testing.T, width int, pix []color.NRGBA) *image.NRGBA {
	t.Helper()
	if width <= 0 || len(pix)%width != 0 {
		t.Fatalf("invalid dimensions: width %d, %d pixels", width, len(pix))
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, len(pix)/width))
	for i, c := range pix {
		off := (i/width)*img.Stride + (i%width)*4
		img.Pix[off+0] = c.R
		img.Pix[off+1] = c.G
		img.Pix[off+2] = c.B
		img.Pix[off+3] = c.A
	}
	return img
}

func mipmapPixelEqual(got, want color.NRGBA) bool {
	if absint(int(got.R)-int(want.R)) > mipmapTestDelta ||
		absint(int(got.G)-int(want.G)) > mipmapTestDelta ||
		absint(int(got.B)-int(want.B)) > mipmapTestDelta ||
		absint(int(got.A)-int(want.A)) > mipmapTestDelta {
		return false
	}
	return true
}

func mipmapPixel(img *image.NRGBA, x, y int) color.NRGBA {
	off := y*img.Stride + x*4
	return color.NRGBA{
		R: img.Pix[off+0],
		G: img.Pix[off+1],
		B: img.Pix[off+2],
		A: img.Pix[off+3],
	}
}

// checkMipmapLevel verifies the geometry of a level and that every pixel
// matches want (row-major, rows repeated as needed).
func checkMipmapLevel(t *testing.T, level int, img *image.NRGBA, w, h int, want []color.NRGBA) {
	t.Helper()
	if img.Rect != image.Rect(0, 0, w, h) {
		t.Fatalf("level %d rect = %v, want (0,0)-(%d,%d)", level, img.Rect, w, h)
	}
	if img.Stride != w*4 {
		t.Fatalf("level %d stride = %d, want %d", level, img.Stride, w*4)
	}
	if len(img.Pix) != w*h*4 {
		t.Fatalf("level %d pix length = %d, want %d", level, len(img.Pix), w*h*4)
	}
	if len(want) == 0 {
		return
	}
	if len(want) != w*h {
		t.Fatalf("test bug: %d expected pixels for %dx%d level", len(want), w, h)
	}
	for i := 0; i < w*h; i++ {
		got := mipmapPixel(img, i%w, i/w)
		if !mipmapPixelEqual(got, want[i]) {
			t.Fatalf("level %d pixel (%d,%d) = %v, want %v", level, i%w, i/w, got, want[i])
		}
	}
}

func TestGenerateMipmapsSizes(t *testing.T) {
	testCases := []struct {
		name string
		w, h int
		want [][2]int
	}{
		{"1x1", 1, 1, [][2]int{{1, 1}}},
		{"2x3", 2, 3, [][2]int{{2, 3}, {1, 1}}},
		{"5x3 odd", 5, 3, [][2]int{{5, 3}, {2, 1}, {1, 1}}},
		{"8x8", 8, 8, [][2]int{{8, 8}, {4, 4}, {2, 2}, {1, 1}}},
		{"1x7 odd", 1, 7, [][2]int{{1, 7}, {1, 3}, {1, 1}}},
		{"9x1 odd", 9, 1, [][2]int{{9, 1}, {4, 1}, {2, 1}, {1, 1}}},
		{"17x11 odd", 17, 11, [][2]int{{17, 11}, {8, 5}, {4, 2}, {2, 1}, {1, 1}}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			src := image.NewNRGBA(image.Rect(0, 0, tc.w, tc.h))
			levels, err := GenerateMipmaps(src, MipmapOptions{})
			if err != nil {
				t.Fatalf("GenerateMipmaps returned error: %v", err)
			}
			if len(levels) != len(tc.want) {
				t.Fatalf("got %d levels, want %d", len(levels), len(tc.want))
			}
			for i, want := range tc.want {
				checkMipmapLevel(t, i, levels[i], want[0], want[1], nil)
			}
			// The chain must always end at 1x1.
			last := levels[len(levels)-1].Rect
			if last.Dx() != 1 || last.Dy() != 1 {
				t.Fatalf("last level is %v, want 1x1", last)
			}
		})
	}
}

// TestGenerateMipmapsPremultipliedAlpha checks that the RGB of fully
// transparent pixels never leaks into the result and that semi-transparent
// colored edges are averaged with alpha weighting.
func TestGenerateMipmapsPremultipliedAlpha(t *testing.T) {
	// Opaque red next to transparent black: the average must stay fully red
	// instead of darkening towards black.
	red := color.NRGBA{0xff, 0x00, 0x00, 0xff}
	src := newTestNRGBA(t, 2, []color.NRGBA{red, {0x00, 0x00, 0x00, 0x00}})
	levels, err := GenerateMipmaps(src, MipmapOptions{})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	checkMipmapLevel(t, 1, levels[1], 1, 1, []color.NRGBA{{0xff, 0x00, 0x00, 0x80}})

	// Fully transparent pixels with bright "garbage" colors must not tint
	// their neighbours: the transparent green and white pixels below must
	// not affect the resulting red and blue halves.
	src = newTestNRGBA(t, 4, []color.NRGBA{
		{0xff, 0x00, 0x00, 0xff},
		{0x00, 0xff, 0x00, 0x00},
		{0x00, 0x00, 0xff, 0xff},
		{0xff, 0xff, 0xff, 0x00},
	})
	levels, err = GenerateMipmaps(src, MipmapOptions{})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3", len(levels))
	}
	checkMipmapLevel(t, 1, levels[1], 2, 1, []color.NRGBA{
		{0xff, 0x00, 0x00, 0x80},
		{0x00, 0x00, 0xff, 0x80},
	})
	// The final level averages two half-transparent, fully saturated
	// complementary pixels: no green or white may leak in.
	checkMipmapLevel(t, 2, levels[2], 1, 1, []color.NRGBA{{0x80, 0x00, 0x80, 0x80}})

	// An image that is fully transparent except for hidden RGB values must
	// produce transparent levels with no residual color.
	src = newTestNRGBA(t, 2, []color.NRGBA{
		{0xff, 0x00, 0x00, 0x00},
		{0x00, 0xff, 0xff, 0x00},
	})
	levels, err = GenerateMipmaps(src, MipmapOptions{LinearLight: true})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	checkMipmapLevel(t, 1, levels[1], 1, 1, []color.NRGBA{{0x00, 0x00, 0x00, 0x00}})
}

// TestGenerateMipmapsOddSizeLevels verifies every level of a 5x3 chain that
// combines opaque, semi-transparent and fully transparent edge pixels.
func TestGenerateMipmapsOddSizeLevels(t *testing.T) {
	row := []color.NRGBA{
		{0xff, 0x00, 0x00, 0xff}, // opaque red
		{0x00, 0xff, 0x00, 0x60}, // semi-transparent green
		{0x00, 0x00, 0x00, 0x00}, // transparent black
		{0x00, 0x00, 0xff, 0x40}, // faint blue
		{0xff, 0xff, 0xff, 0x00}, // transparent white
	}
	pix := make([]color.NRGBA, 0, 15)
	for i := 0; i < 3; i++ {
		pix = append(pix, row...)
	}
	src := newTestNRGBA(t, 5, pix)

	levels, err := GenerateMipmaps(src, MipmapOptions{})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3", len(levels))
	}

	// Level 0 is the untouched source.
	checkMipmapLevel(t, 0, levels[0], 5, 3, pix)

	// Level 1 (2x1): destination 0 covers columns [0,2.5), destination 1
	// covers [2.5,5). Weights are 0.4/0.4/0.2 and 0.2/0.4/0.4.
	checkMipmapLevel(t, 1, levels[1], 2, 1, []color.NRGBA{
		{0xb9, 0x46, 0x00, 0x8c},
		{0x00, 0x00, 0xff, 0x1a},
	})

	// Level 2 (1x1) is the alpha-weighted average of the whole image.
	checkMipmapLevel(t, 2, levels[2], 1, 1, []color.NRGBA{{0x9c, 0x3b, 0x28, 0x53}})
}

func TestGenerateMipmapsUniformColor(t *testing.T) {
	// A uniform color is invariant under area averaging, including for odd
	// sizes and partial transparency.
	want := color.NRGBA{37, 211, 99, 137}
	pix := make([]color.NRGBA, 7*5)
	for i := range pix {
		pix[i] = want
	}
	src := newTestNRGBA(t, 7, pix)

	levels, err := GenerateMipmaps(src, MipmapOptions{})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	if len(levels) != 3 { // 7x5, 3x2, 1x1
		t.Fatalf("got %d levels, want 3", len(levels))
	}
	for i, lvl := range levels {
		for y := 0; y < lvl.Rect.Dy(); y++ {
			for x := 0; x < lvl.Rect.Dx(); x++ {
				if got := mipmapPixel(lvl, x, y); !mipmapPixelEqual(got, want) {
					t.Fatalf("level %d pixel (%d,%d) = %v, want %v", i, x, y, got, want)
				}
			}
		}
	}
}

func TestGenerateMipmapsLinearLight(t *testing.T) {
	src := newTestNRGBA(t, 2, []color.NRGBA{
		{0x00, 0x00, 0x00, 0xff},
		{0xff, 0xff, 0xff, 0xff},
	})

	levels, err := GenerateMipmaps(src, MipmapOptions{})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	srgbAvg := mipmapPixel(levels[1], 0, 0)
	if !mipmapPixelEqual(srgbAvg, color.NRGBA{0x80, 0x80, 0x80, 0xff}) {
		t.Fatalf("sRGB average = %v, want 128 gray", srgbAvg)
	}

	levels, err = GenerateMipmaps(src, MipmapOptions{LinearLight: true})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	// Level 0 is a plain copy even in linear light mode.
	if got := mipmapPixel(levels[0], 0, 0); !mipmapPixelEqual(got, color.NRGBA{0, 0, 0, 0xff}) {
		t.Fatalf("linear level 0 pixel = %v, want black", got)
	}
	if got := mipmapPixel(levels[0], 1, 0); !mipmapPixelEqual(got, color.NRGBA{0xff, 0xff, 0xff, 0xff}) {
		t.Fatalf("linear level 0 pixel = %v, want white", got)
	}
	linAvg := mipmapPixel(levels[1], 0, 0)
	if linAvg.A != 0xff {
		t.Fatalf("linear average alpha = %d, want 255", linAvg.A)
	}
	// Linear 0.5 encodes to sRGB 0.735 -> 188.
	if absint(int(linAvg.R)-188) > mipmapTestDelta ||
		absint(int(linAvg.G)-188) > mipmapTestDelta ||
		absint(int(linAvg.B)-188) > mipmapTestDelta {
		t.Fatalf("linear average = %v, want 188 gray", linAvg)
	}
	if linAvg.R <= srgbAvg.R+10 {
		t.Fatalf("linear average %d must be brighter than sRGB average %d", linAvg.R, srgbAvg.R)
	}

	// Transparent pixels must stay excluded in linear mode too.
	src = newTestNRGBA(t, 2, []color.NRGBA{
		{0x00, 0x00, 0xff, 0x80},
		{0xff, 0xff, 0xff, 0x00},
	})
	levels, err = GenerateMipmaps(src, MipmapOptions{LinearLight: true})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	if got := mipmapPixel(levels[1], 0, 0); !mipmapPixelEqual(got, color.NRGBA{0x00, 0x00, 0xff, 0x40}) {
		t.Fatalf("linear edge average = %v, want half-transparent blue", got)
	}

	// The linear sRGB encoding also has a low-value branch; averaging a
	// very dark pixel must not change it noticeably.
	src = newTestNRGBA(t, 2, []color.NRGBA{
		{0x01, 0x01, 0x01, 0xff},
		{0x01, 0x01, 0x01, 0xff},
	})
	levels, err = GenerateMipmaps(src, MipmapOptions{LinearLight: true})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	if got := mipmapPixel(levels[1], 0, 0); got.A != 0xff || got.R > 1 || got.G > 1 || got.B > 1 {
		t.Fatalf("linear dark average = %v, want a dark opaque pixel", got)
	}
}

func TestGenerateMipmapsMaxLevels(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))

	testCases := []struct {
		maxLevels int
		want      int
	}{
		{0, 4},   // complete chain
		{1, 1},   // base level only
		{2, 2},   // 8x8, 4x4
		{3, 3},   // 8x8, 4x4, 2x2
		{100, 4}, // clamped to the complete chain
	}
	for _, tc := range testCases {
		levels, err := GenerateMipmaps(src, MipmapOptions{MaxLevels: tc.maxLevels})
		if err != nil {
			t.Fatalf("MaxLevels=%d: unexpected error: %v", tc.maxLevels, err)
		}
		if len(levels) != tc.want {
			t.Fatalf("MaxLevels=%d: got %d levels, want %d", tc.maxLevels, len(levels), tc.want)
		}
	}

	if _, err := GenerateMipmaps(src, MipmapOptions{MaxLevels: -1}); err != ErrMipmapInvalidMaxLevels {
		t.Fatalf("negative MaxLevels error = %v, want ErrMipmapInvalidMaxLevels", err)
	}
}

func TestGenerateMipmapsMaxPixels(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4)) // total 16+4+1 = 21 pixels

	levels, err := GenerateMipmaps(src, MipmapOptions{MaxPixels: 21})
	if err != nil {
		t.Fatalf("exact budget returned error: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3", len(levels))
	}

	if _, err := GenerateMipmaps(src, MipmapOptions{MaxPixels: 20}); err != ErrMipmapTooLarge {
		t.Fatalf("small budget error = %v, want ErrMipmapTooLarge", err)
	}
	if _, err := GenerateMipmaps(src, MipmapOptions{MaxPixels: -1}); err != ErrMipmapInvalidMaxPixels {
		t.Fatalf("negative MaxPixels error = %v, want ErrMipmapInvalidMaxPixels", err)
	}

	// MaxPixels only accounts for the returned levels.
	if _, err := GenerateMipmaps(src, MipmapOptions{MaxLevels: 1, MaxPixels: 16}); err != nil {
		t.Fatalf("base level within budget returned error: %v", err)
	}
	// A base level that alone exceeds the budget is rejected too.
	if _, err := GenerateMipmaps(src, MipmapOptions{MaxPixels: 15}); err != ErrMipmapTooLarge {
		t.Fatalf("base level over budget error = %v, want ErrMipmapTooLarge", err)
	}

	// The budget covers the whole chain, not just the base level, and an
	// explicit budget replaces the default one.
	big := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	if _, err := GenerateMipmaps(big, MipmapOptions{MaxPixels: 64 * 64}); err == nil {
		t.Fatalf("budget smaller than the chain must be rejected")
	}
	if _, err := GenerateMipmaps(big, MipmapOptions{MaxPixels: 64*64 + 32*32 + 16*16 + 8*8 + 4*4 + 2*2 + 1}); err != nil {
		t.Fatalf("exact chain budget returned error: %v", err)
	}

	// The default limit rejects a huge image before allocating anything.
	// On 32-bit platforms these dimensions would already trip the overflow
	// check, so the assertion is restricted to 64-bit builds.
	if strconv.IntSize == 64 {
		huge := emptyBoundsImage{image.Rect(0, 0, 1<<20, 1<<20)} // 2^40 pixels
		if _, err := GenerateMipmaps(huge, MipmapOptions{}); err != ErrMipmapTooLarge {
			t.Fatalf("default budget error = %v, want ErrMipmapTooLarge", err)
		}
	}
}

// emptyBoundsImage is an image.Image with arbitrary bounds and no pixel data.
// It is never expected to be read.
type emptyBoundsImage struct {
	rect image.Rectangle
}

func (e emptyBoundsImage) ColorModel() color.Model { return color.NRGBAModel }
func (e emptyBoundsImage) Bounds() image.Rectangle { return e.rect }
func (e emptyBoundsImage) At(int, int) color.Color { return color.NRGBA{} }

func TestGenerateMipmapsInvalidImages(t *testing.T) {
	if _, err := GenerateMipmaps(nil, MipmapOptions{}); err != ErrMipmapEmptyImage {
		t.Fatalf("nil image error = %v, want ErrMipmapEmptyImage", err)
	}
	if _, err := GenerateMipmaps(image.NewNRGBA(image.Rect(0, 0, 0, 0)), MipmapOptions{}); err != ErrMipmapEmptyImage {
		t.Fatalf("empty image error = %v, want ErrMipmapEmptyImage", err)
	}
	if _, err := GenerateMipmaps(emptyBoundsImage{image.Rect(5, 7, 5, 9)}, MipmapOptions{}); err != ErrMipmapEmptyImage {
		t.Fatalf("zero width image error = %v, want ErrMipmapEmptyImage", err)
	}

	// Oversized dimensions must be rejected before any allocation, both
	// for a single huge side and for a product that overflows.
	maxInt := int(^uint(0) >> 1)
	if _, err := GenerateMipmaps(emptyBoundsImage{image.Rect(0, 0, maxInt, 1)}, MipmapOptions{}); err != ErrMipmapSizeOverflow {
		t.Fatalf("single huge side error = %v, want ErrMipmapSizeOverflow", err)
	}
	if _, err := GenerateMipmaps(emptyBoundsImage{image.Rect(0, 0, maxInt, maxInt)}, MipmapOptions{}); err != ErrMipmapSizeOverflow {
		t.Fatalf("overflowing area error = %v, want ErrMipmapSizeOverflow", err)
	}
	if strconv.IntSize == 64 {
		// The rectangle itself fits, but four bytes per pixel do not.
		n := int64(1)
		side := int(n << 31)
		if _, err := GenerateMipmaps(emptyBoundsImage{image.Rect(0, 0, side, side)}, MipmapOptions{}); err != ErrMipmapSizeOverflow {
			t.Fatalf("byte size overflow error = %v, want ErrMipmapSizeOverflow", err)
		}
		// Width times height overflows int64 even though both sides fit.
		side = int(n << 32)
		if _, err := GenerateMipmaps(emptyBoundsImage{image.Rect(0, 0, side, side)}, MipmapOptions{}); err != ErrMipmapSizeOverflow {
			t.Fatalf("area overflow error = %v, want ErrMipmapSizeOverflow", err)
		}
	}
}

func TestGenerateMipmapsNonZeroOriginAndIsolation(t *testing.T) {
	// The source keeps its non-zero origin and is not modified.
	src := image.NewNRGBA(image.Rect(10, 20, 18, 26))
	for i := range src.Pix {
		src.Pix[i] = uint8(i*7 + 1)
	}
	before := make([]uint8, len(src.Pix))
	copy(before, src.Pix)

	levels, err := GenerateMipmaps(src, MipmapOptions{})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	if src.Rect != image.Rect(10, 20, 18, 26) {
		t.Fatalf("source rect changed to %v", src.Rect)
	}
	for i := range before {
		if src.Pix[i] != before[i] {
			t.Fatalf("source pixel %d changed from %d to %d", i, before[i], src.Pix[i])
		}
	}

	// Level 0 copies the visible content to a zero origin.
	if levels[0].Rect != image.Rect(0, 0, 8, 6) {
		t.Fatalf("level 0 rect = %v, want (0,0)-(8,6)", levels[0].Rect)
	}
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			want := src.NRGBAAt(10+x, 20+y)
			if got := mipmapPixel(levels[0], x, y); !mipmapPixelEqual(got, want) {
				t.Fatalf("level 0 pixel (%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}

	// Mutating a returned level must not affect the source or the other
	// levels.
	levels[1].Pix[0] ^= 0xff
	levels[1].Pix[1] ^= 0xff
	levels[1].Pix[2] ^= 0xff
	if src.Pix[0] != before[0] {
		t.Fatalf("mutating a level changed the source image")
	}
}

// TestGenerateMipmapsAnyImageType checks that the function accepts arbitrary
// image.Image implementations and non-NRGBA color models.
func TestGenerateMipmapsAnyImageType(t *testing.T) {
	src := image.NewGray(image.Rect(3, 4, 8, 9)) // 5x5, non-zero origin
	for y := 4; y < 9; y++ {
		for x := 3; x < 8; x++ {
			src.SetGray(x, y, color.Gray{Y: uint8(x*30 + y)})
		}
	}

	levels, err := GenerateMipmaps(src, MipmapOptions{})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	want := [][2]int{{5, 5}, {2, 2}, {1, 1}}
	if len(levels) != len(want) {
		t.Fatalf("got %d levels, want %d", len(levels), len(want))
	}
	for i, size := range want {
		checkMipmapLevel(t, i, levels[i], size[0], size[1], nil)
	}

	// Gray source pixels become opaque NRGBA pixels with equal channels.
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			want := src.GrayAt(3+x, 4+y)
			got := mipmapPixel(levels[0], x, y)
			if got.R != want.Y || got.G != want.Y || got.B != want.Y || got.A != 0xff {
				t.Fatalf("level 0 pixel (%d,%d) = %v, want opaque gray %d", x, y, got, want.Y)
			}
		}
	}
}

// TestGenerateMipmapsMatchesResize checks that on even dimensions and a
// uniform box footprint the levels agree with Resize using the Box filter,
// which is the established behavior of the package for 2x2 averaging.
func TestGenerateMipmapsMatchesResize(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 60, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 60; x++ {
			off := y*src.Stride + x*4
			src.Pix[off+0] = uint8(x * 4)
			src.Pix[off+1] = uint8(y * 6)
			src.Pix[off+2] = uint8((x + y) * 3)
			src.Pix[off+3] = uint8((x*7 + y*11) % 256)
		}
	}

	levels, err := GenerateMipmaps(src, MipmapOptions{MaxLevels: 2})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	checkMipmapLevel(t, 1, levels[1], 30, 18, nil)

	// Resize with Box performs two separable passes and rounds each one,
	// so a difference of one step per channel is expected.
	want := Resize(src, 30, 18, Box)
	if !compareNRGBA(levels[1], want, 1+mipmapTestDelta) {
		t.Fatalf("level 1 differs from Resize with Box")
	}
}

func TestGenerateMipmapsDeterministic(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 13, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 13; x++ {
			off := y*src.Stride + x*4
			src.Pix[off+0] = uint8(x*17 + y*3)
			src.Pix[off+1] = uint8(x*5 + y*29)
			src.Pix[off+2] = uint8((x*y*11 + 7) % 256)
			src.Pix[off+3] = uint8((x*13 + y*19) % 256)
		}
	}

	first, err := GenerateMipmaps(src, MipmapOptions{LinearLight: true})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	second, err := GenerateMipmaps(src, MipmapOptions{LinearLight: true})
	if err != nil {
		t.Fatalf("GenerateMipmaps returned error: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("level count differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Rect != second[i].Rect {
			t.Fatalf("level %d rect differs: %v vs %v", i, first[i].Rect, second[i].Rect)
		}
		if !compareNRGBA(first[i], second[i], 0) {
			t.Fatalf("level %d is not deterministic", i)
		}
	}
}
