package imaging

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// linearRefSRGBToLinear is an independent sRGB -> linear light transfer used
// as a fixed reference in the tests below. It intentionally does not use the
// implementation's lookup table.
func linearRefSRGBToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// linearRefLinearToSRGB is an independent linear light -> sRGB transfer.
func linearRefLinearToSRGB(c float64) float64 {
	if c <= 0 {
		return 0
	}
	if c >= 1 {
		return 1
	}
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1.0/2.4) - 0.055
}

// linearRefByte returns the 8-bit sRGB encoding of a linear light value,
// rounded and clamped with the same rule used by the package.
func linearRefByte(c float64) uint8 {
	return clamp(linearRefLinearToSRGB(c) * 255)
}

// solidNRGBA is a test helper that builds an NRGBA image with a non-zero
// rectangle and a solid color.
func solidNRGBA(rect image.Rectangle, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(rect)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// checkerboardNRGBA builds a black/white checkerboard with the given cell
// size; all pixels are fully opaque.
func checkerboardNRGBA(rect image.Rectangle, cell int) *image.NRGBA {
	img := image.NewNRGBA(rect)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			c := color.NRGBA{0, 0, 0, 0xff}
			if ((x-rect.Min.X)/cell+(y-rect.Min.Y)/cell)%2 == 0 {
				c = color.NRGBA{0xff, 0xff, 0xff, 0xff}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// colorAt returns the NRGBA pixel at (x, y) of an image whose bounds start at
// (0, 0).
func colorAt(img *image.NRGBA, x, y int) color.NRGBA {
	i := y*img.Stride + x*4
	return color.NRGBA{
		R: img.Pix[i+0],
		G: img.Pix[i+1],
		B: img.Pix[i+2],
		A: img.Pix[i+3],
	}
}

// requirePixel compares one output pixel against the expected NRGBA value with
// a per-channel tolerance.
func requirePixel(t *testing.T, img *image.NRGBA, x, y int, want color.NRGBA, delta int) {
	t.Helper()
	got := colorAt(img, x, y)
	for i, pair := range [][2]uint8{
		{got.R, want.R}, {got.G, want.G}, {got.B, want.B}, {got.A, want.A},
	} {
		if absint(int(pair[0])-int(pair[1])) > delta {
			t.Fatalf("pixel (%d,%d) = %v, want %v (channel %d, delta %d)",
				x, y, got, want, i, delta)
		}
	}
}

// TestResizeLinearBlackWhiteAverage checks the core motivation for the
// feature: an equal mix of black and white must become sRGB 188 in linear
// light instead of the darker 128 produced by averaging encoded values.
func TestResizeLinearBlackWhiteAverage(t *testing.T) {
	// 2x1: black, white. Box down to 1x1 averages both exactly.
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{0x00, 0x00, 0x00, 0xff})
	src.SetNRGBA(1, 0, color.NRGBA{0xff, 0xff, 0xff, 0xff})

	got := ResizeLinear(src, 1, 1, Box)
	// (0 + 1) / 2 = 0.5 linear -> sRGB 188.
	want := linearRefByte(0.5)
	if want != 188 {
		t.Fatalf("reference sanity: expected 188, got %d", want)
	}
	requirePixel(t, got, 0, 0, color.NRGBA{want, want, want, 0xff}, 1)

	// The default path must keep the legacy, darker result.
	legacy := Resize(src, 1, 1, Box)
	requirePixel(t, legacy, 0, 0, color.NRGBA{0x80, 0x80, 0x80, 0xff}, 1)

	// Both helpers must agree with the option form.
	helper := ResizeLinear(src, 1, 1, Box)
	option := Resize(src, 1, 1, Box, LinearLight(true))
	if !compareNRGBA(helper, option, 0) {
		t.Fatalf("ResizeLinear and LinearLight option disagree")
	}
}

// TestResizeLinearCheckerboardNotDark verifies that downscaling a black/white
// checkerboard in linear light yields a mid-gray that is brighter than the
// encoded-space average, and that no pixel is darker than the linear-light
// expectation.
func TestResizeLinearCheckerboardNotDark(t *testing.T) {
	// 8x8 checkerboard of 1px cells; Box to 2x2 means each destination
	// pixel averages a 4x4 source block, i.e. 8 black and 8 white pixels.
	src := checkerboardNRGBA(image.Rect(0, 0, 8, 8), 1)

	linear := ResizeLinear(src, 2, 2, Box)
	legacy := Resize(src, 2, 2, Box)

	// Every linear result pixel must be the linear average of 50% white.
	want := linearRefByte(0.5)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			requirePixel(t, linear, x, y, color.NRGBA{want, want, want, 0xff}, 1)
			if colorAt(legacy, x, y).R >= want {
				t.Fatalf("legacy result should be darker than linear %d", want)
			}
		}
	}
}

// TestResizeLinearNonPremultipliedRoundedInputs checks that the transfer
// functions round-trip byte values exactly when no filtering is needed.
func TestResizeLinearTransferRoundTrip(t *testing.T) {
	for v := 0; v < 256; v++ {
		c := float64(v) / 255.0
		got := linearRefByte(linearRefSRGBToLinear(c))
		if int(got) != v {
			t.Fatalf("transfer round trip for %d: got %d", v, got)
		}
	}
	if srgbToLinearLUT[0] != 0 || srgbToLinearLUT[255] != 1 {
		t.Fatalf("lookup table endpoints wrong: %v %v",
			srgbToLinearLUT[0], srgbToLinearLUT[255])
	}
	for v := 0; v < 256; v++ {
		want := linearRefSRGBToLinear(float64(v) / 255.0)
		if math.Abs(srgbToLinearLUT[v]-want) > 1e-12 {
			t.Fatalf("lookup table mismatch at %d: got %v want %v",
				v, srgbToLinearLUT[v], want)
		}
	}
}

// TestResizeLinearTransparentEdges checks that transparent pixels do not bleed
// their color into visible pixels. A fully opaque red block surrounded by
// transparent green must stay pure red in linear light.
func TestResizeLinearTransparentEdges(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	red := color.NRGBA{0xff, 0x00, 0x00, 0xff}
	clearGreen := color.NRGBA{0x00, 0xff, 0x00, 0x00}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			c := clearGreen
			if x == 1 && y == 1 {
				c = red
			}
			src.SetNRGBA(x, y, c)
		}
	}

	// 1x1 box average: only the red pixel contributes color because the
	// others have zero alpha, while the alpha is the average of all 16
	// coverage weights (255/16 = 16).
	got := ResizeLinear(src, 1, 1, Box)
	requirePixel(t, got, 0, 0, color.NRGBA{0xff, 0x00, 0x00, 0x10}, 1)

	// The default path premultiplies too, so it must also stay red. This
	// guards the shared premultiplied-alpha contract.
	legacy := Resize(src, 1, 1, Box)
	p := colorAt(legacy, 0, 0)
	if p.R != 0xff || p.G != 0 || p.B != 0 {
		t.Fatalf("legacy transparent edge bled color: %v", p)
	}
}

// TestResizeLinearSemiTransparentColor checks a 50% alpha edge. A half
// transparent red pixel averaged with a fully transparent black pixel must
// keep the red hue and halve its alpha, in both linear and default modes.
func TestResizeLinearSemiTransparentColor(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{0xff, 0x00, 0x00, 0xff})
	src.SetNRGBA(1, 0, color.NRGBA{0x00, 0x00, 0x00, 0x00})

	got := ResizeLinear(src, 1, 1, Box)
	// Alpha 0.5, color stays red.
	requirePixel(t, got, 0, 0, color.NRGBA{0xff, 0x00, 0x00, 0x80}, 1)

	// Half transparent mid-gray against transparent: the color of the
	// visible pixel must be preserved, not darkened toward black.
	src2 := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src2.SetNRGBA(0, 0, color.NRGBA{0x80, 0x80, 0x80, 0x80})
	src2.SetNRGBA(1, 0, color.NRGBA{0x00, 0x00, 0x00, 0x00})
	got2 := ResizeLinear(src2, 1, 1, Box)
	requirePixel(t, got2, 0, 0, color.NRGBA{0x80, 0x80, 0x80, 0x40}, 1)
}

// TestResizeLinearNonZeroBounds checks that sources with a non-zero Min point
// produce the same result as the equivalent zero-origin image.
func TestResizeLinearNonZeroBounds(t *testing.T) {
	base := checkerboardNRGBA(image.Rect(0, 0, 8, 8), 1)
	shifted := image.NewNRGBA(image.Rect(-5, 3, 3, 11))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			shifted.SetNRGBA(-5+x, 3+y, base.NRGBAAt(x, y))
		}
	}

	gotBase := ResizeLinear(base, 3, 3, Lanczos)
	gotShift := ResizeLinear(shifted, 3, 3, Lanczos)
	if !compareNRGBA(gotBase, gotShift, 0) {
		t.Fatalf("non-zero bounds changed the result")
	}
	if gotShift.Rect.Min != (image.Point{}) {
		t.Fatalf("output must have a zero origin, got %v", gotShift.Rect)
	}
}

// TestResizeLinearSingleAxis checks single-axis scaling against an independent
// reference computation for both a horizontal and a vertical resize.
func TestResizeLinearSingleAxis(t *testing.T) {
	// 2x1 black/white horizontal resize to 1x1 (Box).
	horiz := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	horiz.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 0xff})
	horiz.SetNRGBA(1, 0, color.NRGBA{0xff, 0xff, 0xff, 0xff})
	gotH := ResizeLinear(horiz, 1, 1, Box)
	requirePixel(t, gotH, 0, 0, color.NRGBA{188, 188, 188, 0xff}, 1)

	// The same values arranged vertically must produce the same result.
	vert := image.NewNRGBA(image.Rect(0, 0, 1, 2))
	vert.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 0xff})
	vert.SetNRGBA(0, 1, color.NRGBA{0xff, 0xff, 0xff, 0xff})
	gotV := ResizeLinear(vert, 1, 1, Box)
	requirePixel(t, gotV, 0, 0, color.NRGBA{188, 188, 188, 0xff}, 1)

	// Horizontal-only upscale must not touch the height.
	wide := ResizeLinear(vert, 4, 2, Linear)
	if wide.Bounds().Dx() != 4 || wide.Bounds().Dy() != 2 {
		t.Fatalf("horizontal-only result size = %v", wide.Bounds())
	}
}

// TestResizeLinearTwoAxesMatchesLinearPhysics checks a two-axis resize against
// a hand-computed linear-light value. A 2x2 image with one white and three
// black opaque pixels averaged to 1x1 is 25% white in linear light.
func TestResizeLinearTwoAxesMatchesLinearPhysics(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	black := color.NRGBA{0, 0, 0, 0xff}
	src.SetNRGBA(0, 0, white)
	src.SetNRGBA(1, 0, black)
	src.SetNRGBA(0, 1, black)
	src.SetNRGBA(1, 1, black)

	// Box: each destination pixel covers exactly one source pixel; the 1x1
	// output averages all four, i.e. 25% white.
	got := ResizeLinear(src, 1, 1, Box)
	want := linearRefByte(0.25)
	requirePixel(t, got, 0, 0, color.NRGBA{want, want, want, 0xff}, 1)
}

// TestResizeLinearDefaultUnchanged verifies that calling the existing functions
// without options produces exactly the same bytes as before the feature was
// added. The golden images are the upstream reference renderings.
func TestResizeLinearDefaultUnchanged(t *testing.T) {
	cases := []struct {
		name string
		got  *image.NRGBA
		want string
	}{
		{"resize_linear", Resize(testdataBranchesPNG, 150, 0, Linear), "out_resize_linear.png"},
		{"resize_lanczos", Resize(testdataBranchesPNG, 150, 0, Lanczos), "out_resize_lanczos.png"},
		{"resize_nearest", Resize(testdataBranchesPNG, 150, 0, NearestNeighbor), "out_resize_nearest.png"},
		{"resize_catrom", Resize(testdataBranchesPNG, 150, 0, CatmullRom), "out_resize_catrom.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := Open("testdata/" + tc.want)
			if err != nil {
				t.Fatalf("failed to open golden image: %v", err)
			}
			if !compareNRGBAGolden(tc.got, toNRGBA(want)) {
				t.Fatalf("default path changed for %s", tc.name)
			}
		})
	}

	// Explicitly disabling linear light must be identical to the default.
	a := Resize(testdataBranchesPNG, 150, 100, Lanczos)
	b := Resize(testdataBranchesPNG, 150, 100, Lanczos, LinearLight(false))
	if !compareNRGBA(a, b, 0) {
		t.Fatalf("LinearLight(false) differs from the default")
	}
}

// TestResizeLinearFitFillThumbnail checks that the option reaches Fit, Fill
// and Thumbnail and that the helper variants agree.
func TestResizeLinearFitFillThumbnail(t *testing.T) {
	src := checkerboardNRGBA(image.Rect(0, 0, 40, 40), 1)

	if got, want := FitLinear(src, 8, 8, Box), Fit(src, 8, 8, Box, LinearLight(true)); !compareNRGBA(got, want, 0) {
		t.Fatalf("FitLinear and Fit option disagree")
	}
	if got, want := FillLinear(src, 8, 8, Center, Box), Fill(src, 8, 8, Center, Box, LinearLight(true)); !compareNRGBA(got, want, 0) {
		t.Fatalf("FillLinear and Fill option disagree")
	}
	if got, want := ThumbnailLinear(src, 8, 8, Box), Thumbnail(src, 8, 8, Box, LinearLight(true)); !compareNRGBA(got, want, 0) {
		t.Fatalf("ThumbnailLinear and Thumbnail option disagree")
	}

	// The linear result must be brighter than the default for a 50% area
	// checkerboard.
	linear := Fit(src, 8, 8, Box, LinearLight(true))
	legacy := Fit(src, 8, 8, Box)
	if colorAt(linear, 4, 4).R <= colorAt(legacy, 4, 4).R {
		t.Fatalf("linear Fit should be brighter than default")
	}
}

// TestResizeLinearInvalidAndExtreme checks the documented error behavior for
// invalid sizes, empty images and extreme scale ratios.
func TestResizeLinearInvalidAndExtreme(t *testing.T) {
	valid := checkerboardNRGBA(image.Rect(0, 0, 4, 4), 1)
	empty := image.NewNRGBA(image.Rectangle{})

	cases := []struct {
		name string
		img  image.Image
		w, h int
	}{
		{"negative width", valid, -1, 4},
		{"negative height", valid, 4, -1},
		{"both zero", valid, 0, 0},
		{"empty image", empty, 4, 4},
		{"huge width", valid, int(^uint(0)>>1) / 16, 1},
		{"huge both", valid, 1 << 30, 1 << 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResizeLinear(tc.img, tc.w, tc.h, Lanczos)
			if got == nil || got.Rect != (image.Rectangle{}) || len(got.Pix) != 0 {
				t.Fatalf("expected an empty image, got %v with %d bytes", got.Rect, len(got.Pix))
			}
		})
	}

	// An extreme scale ratio below the pixel budget must still return a
	// stable, finite result rather than panicking.
	small := solidNRGBA(image.Rect(0, 0, 1, 1), color.NRGBA{0x40, 0x80, 0xc0, 0xff})
	up := ResizeLinear(small, 64, 64, Lanczos)
	if up.Bounds() != image.Rect(0, 0, 64, 64) {
		t.Fatalf("extreme upscale size = %v", up.Bounds())
	}
	for i := 0; i+3 < len(up.Pix); i += 4 {
		want := small.NRGBAAt(0, 0)
		if absint(int(up.Pix[i+0])-int(want.R)) > 1 ||
			absint(int(up.Pix[i+1])-int(want.G)) > 1 ||
			absint(int(up.Pix[i+2])-int(want.B)) > 1 ||
			up.Pix[i+3] != want.A {
			t.Fatalf("uniform upscale drift at byte %d: got %v", i, up.Pix[i:i+4])
		}
	}
}

// TestResizeLinearNearZeroAlpha checks that a very small alpha does not produce
// NaN or wild color values. The result must be either the original hue with a
// rounded alpha or fully transparent black.
func TestResizeLinearNearZeroAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{0xff, 0x00, 0x00, 0x01})
	src.SetNRGBA(1, 0, color.NRGBA{0x00, 0xff, 0x00, 0x00})

	got := ResizeLinear(src, 1, 1, Box)
	p := colorAt(got, 0, 0)
	if p.A == 0 {
		if p.R != 0 || p.G != 0 || p.B != 0 {
			t.Fatalf("invisible pixel must be transparent black, got %v", p)
		}
		return
	}
	// The only contributing pixel is red with alpha ~1/255.
	if p.R != 0xff || p.G != 0 || p.B != 0 {
		t.Fatalf("near-zero alpha changed the hue: %v", p)
	}
}

// TestResizeLinearNearestNoOp checks that NearestNeighbor sampling is not
// affected by the linear light option, since it does not mix pixels.
func TestResizeLinearNearestNoOp(t *testing.T) {
	src := checkerboardNRGBA(image.Rect(0, 0, 8, 8), 1)
	a := Resize(src, 3, 5, NearestNeighbor)
	b := Resize(src, 3, 5, NearestNeighbor, LinearLight(true))
	if !compareNRGBA(a, b, 0) {
		t.Fatalf("NearestNeighbor result changed under linear light")
	}
}

// TestResizeLinearUniformColor checks that a uniform image is preserved by
// linear light resampling, apart from rounding.
func TestResizeLinearUniformColor(t *testing.T) {
	for _, c := range []color.NRGBA{
		{0x12, 0x34, 0x56, 0xff},
		{0xab, 0xcd, 0xef, 0x80},
	} {
		src := solidNRGBA(image.Rect(0, 0, 5, 7), c)
		got := ResizeLinear(src, 11, 3, Lanczos)
		for y := 0; y < 3; y++ {
			for x := 0; x < 11; x++ {
				requirePixel(t, got, x, y, c, 1)
			}
		}
	}
}

// TestResizeLinearTwoPassMatchesReference verifies the separable two-pass
// implementation against an independent, non-separable 2D area-average
// reference in linear light. Only exact integer downscale ratios are used so
// the box filter weights are uniform. Any difference must be rounding only.
func TestResizeLinearTwoPassMatchesReference(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 24, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 24; x++ {
			src.SetNRGBA(x, y, color.NRGBA{
				uint8(x * 10), uint8(y * 7), uint8((x * y) % 255), uint8(40 + (x+y)*3),
			})
		}
	}

	cases := []struct{ w, h int }{{6, 8}, {3, 4}, {12, 16}, {1, 1}}
	for _, tc := range cases {
		got := ResizeLinear(src, tc.w, tc.h, Box)
		want := linearRefAreaAverage(src, tc.w, tc.h)
		if !compareNRGBA(got, want, 1) {
			t.Fatalf("%dx%d differs from the 2D reference", tc.w, tc.h)
		}
	}
}

// linearRefAreaAverage is an independent, non-separable 2D area-average
// reference in linear light. It requires dstW to divide the source width and
// dstH to divide the source height.
func linearRefAreaAverage(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	fx, fy := sw/dstW, sh/dstH
	weight := 1.0 / float64(fx*fy)

	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	for dy := 0; dy < dstH; dy++ {
		for dx := 0; dx < dstW; dx++ {
			var r, g, b, a float64
			for yy := 0; yy < fy; yy++ {
				for xx := 0; xx < fx; xx++ {
					c := src.NRGBAAt(dx*fx+xx, dy*fy+yy)
					aw := float64(c.A) / 255.0 * weight
					r += linearRefSRGBToLinear(float64(c.R)/255.0) * aw
					g += linearRefSRGBToLinear(float64(c.G)/255.0) * aw
					b += linearRefSRGBToLinear(float64(c.B)/255.0) * aw
					a += aw
				}
			}
			var p color.NRGBA
			if a > 0 {
				inv := 1 / a
				p = color.NRGBA{
					R: clamp(linearRefLinearToSRGB(r*inv) * 255),
					G: clamp(linearRefLinearToSRGB(g*inv) * 255),
					B: clamp(linearRefLinearToSRGB(b*inv) * 255),
					A: clamp(a * 255),
				}
			}
			dst.SetNRGBA(dx, dy, p)
		}
	}
	return dst
}

// TestResizeLinearMemoryBudget checks that an extreme upscale beyond the
// documented pixel budget is rejected before allocating memory, while a large
// but legal request still succeeds.
func TestResizeLinearMemoryBudget(t *testing.T) {
	src := solidNRGBA(image.Rect(0, 0, 2, 2), color.NRGBA{0x11, 0x22, 0x33, 0xff})

	// One side alone exceeds the pixel budget.
	huge := int(DefaultResizeLinearMaxPixels) + 1
	if huge <= 0 {
		t.Skip("pixel budget does not fit in int")
	}
	if got := ResizeLinear(src, huge, 1, Linear); got.Rect != (image.Rectangle{}) {
		t.Fatalf("over-budget width was not rejected: %v", got.Rect)
	}
	if got := ResizeLinear(src, 1, huge, Linear); got.Rect != (image.Rectangle{}) {
		t.Fatalf("over-budget height was not rejected: %v", got.Rect)
	}
}

// TestResizeLinearWeightBudget checks that an extreme upscale whose weight
// table would exceed the budget is rejected instead of allocating gigabytes
// of weights, while a reasonable upscale still succeeds.
func TestResizeLinearWeightBudget(t *testing.T) {
	src := solidNRGBA(image.Rect(0, 0, 1, 1), color.NRGBA{0x11, 0x22, 0x33, 0xff})

	// A 16M wide upscale needs a huge weight table even though the image
	// itself would be within the pixel budget.
	if got := ResizeLinear(src, 1<<24, 1, Lanczos); got.Rect != (image.Rectangle{}) {
		t.Fatalf("over-budget weight table was not rejected: %v", got.Rect)
	}

	// A 60000x1 upscale stays within the weight budget.
	got := ResizeLinear(src, 60000, 1, Lanczos)
	if got.Bounds() != image.Rect(0, 0, 60000, 1) {
		t.Fatalf("reasonable upscale was rejected: %v", got.Rect)
	}
	requirePixel(t, got, 30000, 0, color.NRGBA{0x11, 0x22, 0x33, 0xff}, 1)
}

// BenchmarkResizeLinear compares the linear light path with the default one.
func BenchmarkResizeLinear(b *testing.B) {
	b.Run("Default", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Resize(testdataBranchesPNG, 100, 100, Lanczos)
		}
	})
	b.Run("LinearLight", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Resize(testdataBranchesPNG, 100, 100, Lanczos, LinearLight(true))
		}
	})
}
