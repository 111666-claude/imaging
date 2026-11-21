package imaging

import (
	"image"
	"image/color"
	"math/rand"
	"testing"
)

// coverageAt returns the fraction of pixels whose alpha channel is greater
// than or equal to threshold.
func coverageAt(img *image.NRGBA, threshold int) float64 {
	if threshold < 0 || threshold > 255 {
		return 0
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 || h <= 0 {
		return 0
	}
	var count int
	for y := 0; y < h; y++ {
		off := y * img.Stride
		for x := 0; x < w; x++ {
			if int(img.Pix[off+x*4+3]) >= threshold {
				count++
			}
		}
	}
	return float64(count) / float64(w*h)
}

// newAlphaImage builds an NRGBA image of the given size using the alphaAt
// function to generate alpha values. RGB channels are set to distinct values
// derived from the pixel coordinates so any RGB modification is detectable.
func newAlphaImage(w, h int, alphaAt func(x, y int) uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*img.Stride + x*4
			img.Pix[i+0] = uint8((x*7 + 11) % 256)
			img.Pix[i+1] = uint8((y*13 + 17) % 256)
			img.Pix[i+2] = uint8((x + y*5) % 256)
			img.Pix[i+3] = alphaAt(x, y)
		}
	}
	return img
}

// rgbChannels returns a copy of the RGB bytes of an NRGBA image.
func rgbChannels(img *image.NRGBA) []uint8 {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]uint8, 0, w*h*3)
	for y := 0; y < h; y++ {
		off := y * img.Stride
		for x := 0; x < w; x++ {
			i := off + x*4
			out = append(out, img.Pix[i], img.Pix[i+1], img.Pix[i+2])
		}
	}
	return out
}

// checkerboardAlpha produces an alpha cutout checkerboard: fully opaque on
// half of the pixels and fully transparent on the other half.
func checkerboardAlpha(x, y int) uint8 {
	if (x+y)%2 == 0 {
		return 255
	}
	return 0
}

// twigAlpha draws two full-height opaque "twigs" (one column wide) on a
// transparent background, covering a quarter of the width.
func twigAlpha(x, y int) uint8 {
	if x == 0 || x == 4 {
		return 255
	}
	return 0
}

// TestResizeCoverageDefaultPath verifies that invalid thresholds and threshold
// 0 leave the regular Resize output untouched.
func TestResizeCoverageDefaultPath(t *testing.T) {
	src := newAlphaImage(8, 8, checkerboardAlpha)
	for _, threshold := range []int{-1, -128, 256, 1000, 0} {
		got := ResizeCoverage(src, 4, 4, Box, threshold)
		want := Resize(src, 4, 4, Box)
		if !compareNRGBA(got, want, 0) {
			t.Fatalf("threshold %d: coverage result differs from plain Resize", threshold)
		}
	}
}

// TestResizeCoverageFullyTransparentAndOpaque verifies that 0/255-alpha images
// keep the exact plain resize result, including all interpolation filters.
func TestResizeCoverageFullyTransparentAndOpaque(t *testing.T) {
	filters := []ResampleFilter{
		NearestNeighbor, Box, Linear, Hermite, MitchellNetravali,
		CatmullRom, BSpline, Gaussian, Lanczos, Hann, Hamming,
		Blackman, Bartlett, Welch, Cosine,
	}
	builders := map[string]func(x, y int) uint8{
		"transparent": func(x, y int) uint8 { return 0 },
		"opaque":      func(x, y int) uint8 { return 255 },
	}
	for name, alphaAt := range builders {
		src := newAlphaImage(8, 8, alphaAt)
		for _, f := range filters {
			got := ResizeCoverage(src, 3, 3, f, 128)
			want := Resize(src, 3, 3, f)
			if !compareNRGBA(got, want, 0) {
				t.Fatalf("%s image, filter %+v: coverage result differs from plain Resize", name, f)
			}
		}
	}
}

// TestResizeCoverageRGBInvariance verifies that the coverage adjustment never
// touches RGB channels, across every interpolation filter.
func TestResizeCoverageRGBInvariance(t *testing.T) {
	filters := []ResampleFilter{
		NearestNeighbor, Box, Linear, Hermite, MitchellNetravali,
		CatmullRom, BSpline, Gaussian, Lanczos, Hann, Hamming,
		Blackman, Bartlett, Welch, Cosine,
	}
	src := newAlphaImage(16, 16, func(x, y int) uint8 {
		if x%3 == 0 || y%3 == 0 {
			return 255
		}
		return 0
	})
	for _, f := range filters {
		plain := Resize(src, 5, 5, f)
		got := ResizeCoverage(src, 5, 5, f, 129)
		if r, b := rgbChannels(got), rgbChannels(plain); !compareBytes(r, b, 0) {
			t.Fatalf("filter %+v changed RGB channels", f)
		}
	}
}

// TestResizeCoverageExactMatch uses a pattern whose resized coverage already
// equals the source coverage, so the image is returned byte-for-byte unchanged.
func TestResizeCoverageExactMatch(t *testing.T) {
	const threshold = 128
	// One row [255, 255, 0, 0]: source coverage 0.5.
	src := newAlphaImage(4, 1, func(x, y int) uint8 {
		if x < 2 {
			return 255
		}
		return 0
	})

	if got := alphaCoverage(src, threshold); got != 0.5 {
		t.Fatalf("source coverage = %v, want 0.5", got)
	}

	// Box downscale to 2x1 averages the pairs into [255, 0]: coverage is
	// still exactly 0.5, so no gain may be applied.
	got := ResizeCoverage(src, 2, 1, Box, threshold)
	want := Resize(src, 2, 1, Box)
	if !compareNRGBA(got, want, 0) {
		t.Fatalf("got %#v want unchanged resize %#v", got, want)
	}
	if c := coverageAt(got, threshold); c != 0.5 {
		t.Fatalf("resized coverage = %v, want 0.5", c)
	}
}

// TestResizeCoverageNearestCount downsamples a checkerboard to a single pixel
// with a threshold the averaged alpha cannot reach, and checks the
// deterministic nearest feasible choice and the exact resulting values.
func TestResizeCoverageNearestCount(t *testing.T) {
	const threshold = 200
	src := newAlphaImage(4, 4, checkerboardAlpha)

	if got := alphaCoverage(src, threshold); got != 0.5 {
		t.Fatalf("source coverage = %v, want 0.5", got)
	}

	// Plain Box resize gives one pixel with alpha 128: zero coverage.
	plain := Resize(src, 1, 1, Box)
	if plain.Pix[3] != 128 {
		t.Fatalf("plain resize alpha = %d, want 128", plain.Pix[3])
	}
	if c := coverageAt(plain, threshold); c != 0 {
		t.Fatalf("plain resized coverage = %v, want 0", c)
	}

	got := ResizeCoverage(src, 1, 1, Box, threshold)

	// The only non-zero alpha level is 128. Gain = 199.5/128 (~1.5586)
	// rounds the single pixel alpha to 200, which crosses the threshold.
	if got.Pix[3] != 200 {
		t.Fatalf("adjusted alpha = %d, want 200", got.Pix[3])
	}
	if c := coverageAt(got, threshold); c != 1.0 {
		t.Fatalf("adjusted coverage = %v, want 1.0 (nearest feasible to 0.5)", c)
	}
	// RGB must not be touched by the coverage adjustment.
	for c := 0; c < 3; c++ {
		if got.Pix[c] != plain.Pix[c] {
			t.Fatalf("RGB channel %d changed: got %d want %d", c, got.Pix[c], plain.Pix[c])
		}
	}
}

// TestResizeCoverageTwig checks a thin opaque "twig" pattern losing coverage
// when downscaled: the adjustment restores the source coverage exactly.
func TestResizeCoverageTwig(t *testing.T) {
	const threshold = 129
	src := newAlphaImage(8, 8, twigAlpha)

	if got := alphaCoverage(src, threshold); got != 0.25 {
		t.Fatalf("source coverage = %v, want 0.25", got)
	}

	// Downscale to 4x1: box averaging gives alphas [128, 0, 128, 0], none of
	// which reaches threshold 129, so the plain coverage is 0. The gain
	// lifts the 128 values to 129 while leaving zero-alpha pixels untouched,
	// restoring the 0.25 coverage.
	plain4 := Resize(src, 4, 1, Box)
	if c := coverageAt(plain4, threshold); c != 0 {
		t.Fatalf("plain 4x1 coverage = %v, want 0 (precondition)", c)
	}
	got4 := ResizeCoverage(src, 4, 1, Box, threshold)
	wantAlphas := []uint8{129, 0, 129, 0}
	for x, want := range wantAlphas {
		if got := got4.Pix[x*4+3]; got != want {
			t.Fatalf("4x1 pixel %d alpha = %d, want %d", x, got, want)
		}
	}
	if c := coverageAt(got4, threshold); c != 0.5 {
		t.Fatalf("4x1 coverage = %v, want 0.5 (nearest feasible to 0.25)", c)
	}
	if r, b := rgbChannels(got4), rgbChannels(plain4); !compareBytes(r, b, 0) {
		t.Fatal("4x1 coverage adjustment changed RGB channels")
	}

	// Downscale to 2x1: box averaging gives alpha 64 everywhere. The target
	// 0.25 means 0.5 survivors out of two pixels, so both feasible counts (0
	// or 2) are equally far from it. On a count tie the unchanged image is
	// preferred over an aggressive gain, so the quantization limits leave
	// the result untouched.
	got2 := ResizeCoverage(src, 2, 1, Box, threshold)
	want2 := Resize(src, 2, 1, Box)
	if !compareNRGBA(got2, want2, 0) {
		t.Fatalf("2x1 quantization tie should leave the image unchanged: got %#v want %#v", got2, want2)
	}
}

// TestResizeCoverageMultiLevel uses a source whose resize produces several
// distinct alpha levels, so the gain can restore the coverage at an
// intermediate survivor count instead of an all-or-nothing quantization.
func TestResizeCoverageMultiLevel(t *testing.T) {
	const threshold = 129
	// One row of eight: [255, 255, 0, 255, 0, 0, 255, 0], coverage 4/8 = 0.5.
	src := newAlphaImage(8, 1, func(x, y int) uint8 {
		alpha := []uint8{255, 255, 0, 255, 0, 0, 255, 0}
		return alpha[x]
	})
	if got := alphaCoverage(src, threshold); got != 0.5 {
		t.Fatalf("source coverage = %v, want 0.5", got)
	}

	// Box downscale to 4x1 gives [255, 128, 0, 128]: two pixels are below
	// the threshold, so plain coverage is 0.25 while the target is 0.5.
	plain := Resize(src, 4, 1, Box)
	if c := coverageAt(plain, threshold); c != 0.25 {
		t.Fatalf("plain coverage = %v, want 0.25 (precondition)", c)
	}
	got := ResizeCoverage(src, 4, 1, Box, threshold)

	// The 128-alpha pixels are lifted just over the threshold while the
	// already-opaque and fully transparent pixels keep their values.
	wantAlphas := []uint8{255, 129, 0, 129}
	for x, want := range wantAlphas {
		if got := got.Pix[x*4+3]; got != want {
			t.Fatalf("pixel %d alpha = %d, want %d", x, got, want)
		}
	}
	if c := coverageAt(got, threshold); c != 0.75 {
		t.Fatalf("adjusted coverage = %v, want 0.75 (nearest feasible to 0.5)", c)
	}
	if r, b := rgbChannels(got), rgbChannels(plain); !compareBytes(r, b, 0) {
		t.Fatal("RGB channels changed during coverage adjustment")
	}
}

// TestResizeCoverageSemiTransparentEdge uses soft alpha values instead of a
// hard cutout and verifies the exact gain, survivor count and color values.
func TestResizeCoverageSemiTransparentEdge(t *testing.T) {
	const threshold = 128
	// One row: alphas [0, 64, 255] -> source coverage 1/3 at threshold 128.
	src := newAlphaImage(3, 1, func(x, y int) uint8 {
		return []uint8{0, 64, 255}[x]
	})
	if got := alphaCoverage(src, threshold); got != 1.0/3.0 {
		t.Fatalf("source coverage = %v, want 1/3", got)
	}

	// Nearest-neighbor upscale to 6x1 keeps the same alpha levels; the
	// coverage at threshold 128 stays at 1/3, so the output must be unchanged.
	got := ResizeCoverage(src, 6, 1, NearestNeighbor, threshold)
	want := Resize(src, 6, 1, NearestNeighbor)
	if !compareNRGBA(got, want, 0) {
		t.Fatalf("nearest-neighbor upscale changed: got %#v want %#v", got, want)
	}

	// Box downscale to 1x1: alpha = (0+64+255)/3 = 106.33 -> 106, coverage 0.
	plain := Resize(src, 1, 1, Box)
	if plain.Pix[3] != 106 {
		t.Fatalf("plain alpha = %d, want 106", plain.Pix[3])
	}
	got = ResizeCoverage(src, 1, 1, Box, threshold)

	// The resized image is below the target 1/3. The only alternative gain
	// lifts the single pixel to the threshold (coverage 1.0, diff 2/3),
	// while leaving it alone keeps coverage 0 (diff 1/3). Since the
	// unchanged image is already the nearest feasible option, no gain is
	// applied and the alpha stays 106.
	if got.Pix[3] != 106 {
		t.Fatalf("adjusted alpha = %d, want 106", got.Pix[3])
	}
	if c := coverageAt(got, threshold); c != 0 {
		t.Fatalf("adjusted coverage = %v, want 0", c)
	}
	for c := 0; c < 3; c++ {
		if got.Pix[c] != plain.Pix[c] {
			t.Fatalf("RGB channel %d changed: got %d want %d", c, got.Pix[c], plain.Pix[c])
		}
	}
}

// TestResizeCoverageOvershoot verifies the gain-down direction: when the
// resized image has more coverage than the source, alpha is reduced.
func TestResizeCoverageOvershoot(t *testing.T) {
	const threshold = 200
	// One row [255, 100, 0, 0]: source coverage at threshold 200 is 0.25.
	src := newAlphaImage(4, 1, func(x, y int) uint8 {
		return []uint8{255, 100, 0, 0}[x]
	})
	if got := alphaCoverage(src, threshold); got != 0.25 {
		t.Fatalf("source coverage = %v, want 0.25", got)
	}

	// Box downscale to 2x1 gives [178, 0], coverage 0 (below target).
	// Instead use nearest-neighbor: it keeps the 255 and 100 levels, so a
	// threshold of 100 makes both survive while the source coverage at that
	// threshold is also 0.5. To get a real overshoot, build a resize whose
	// average lifts a sub-threshold alpha above the threshold:
	// [255, 150, 0, 0] at threshold 200 has source coverage 0.25, and box
	// averaging the first pair into one output pixel gives alpha 203.
	over := newAlphaImage(4, 1, func(x, y int) uint8 {
		return []uint8{255, 150, 0, 0}[x]
	})
	if got := alphaCoverage(over, threshold); got != 0.25 {
		t.Fatalf("overshoot source coverage = %v, want 0.25", got)
	}
	plain := Resize(over, 2, 1, Box)
	if plain.Pix[3] != 203 {
		t.Fatalf("plain alpha = %d, want 203", plain.Pix[3])
	}
	if c := coverageAt(plain, threshold); c != 0.5 {
		t.Fatalf("plain coverage = %v, want 0.5 (overshoot precondition)", c)
	}
	got := ResizeCoverage(over, 2, 1, Box, threshold)

	// Target is 0.25 (0.5 of two pixels). Feasible counts are 0 (gain
	// below 1) and 1 (unchanged), both 0.25 away. The direction rule picks
	// the smaller count, so the gain pulls alpha 203 back below threshold.
	if c := coverageAt(got, threshold); c != 0 {
		t.Fatalf("adjusted coverage = %v, want 0 (gain down towards target)", c)
	}
	if r, b := rgbChannels(got), rgbChannels(plain); !compareBytes(r, b, 0) {
		t.Fatal("RGB channels changed during coverage adjustment")
	}
}

// TestResizeCoverageSmallImage checks the 1x1 image edge cases.
func TestResizeCoverageSmallImage(t *testing.T) {
	opaque := newAlphaImage(1, 1, func(x, y int) uint8 { return 255 })
	if got := ResizeCoverage(opaque, 1, 1, Box, 128); !compareNRGBA(got, Resize(opaque, 1, 1, Box), 0) {
		t.Fatal("1x1 opaque image was modified")
	}

	transparent := newAlphaImage(1, 1, func(x, y int) uint8 { return 0 })
	if got := ResizeCoverage(transparent, 1, 1, Box, 128); !compareNRGBA(got, Resize(transparent, 1, 1, Box), 0) {
		t.Fatal("1x1 transparent image was modified")
	}

	empty := &image.NRGBA{}
	if got := ResizeCoverage(empty, 4, 4, Box, 128); got.Rect != (image.Rectangle{}) || len(got.Pix) != 0 {
		t.Fatalf("empty source produced non-empty result: %#v", got)
	}
}

// TestFitCoverage checks that Fit coverage mode preserves coverage through the
// Fit entry point and falls back to plain Fit for invalid thresholds,
// including the no-scaling branch.
func TestFitCoverage(t *testing.T) {
	const threshold = 129
	src := newAlphaImage(8, 8, twigAlpha)

	// Downscale into a 4x4 box. The 0.25 target means 4 of 16 pixels, but
	// the box-averaged alpha levels only allow 0 or 8 survivors, so 8
	// (coverage 0.5) is the nearest feasible result.
	got := FitCoverage(src, 4, 4, Box, threshold)
	if got.Bounds().Dx() != 4 || got.Bounds().Dy() != 4 {
		t.Fatalf("fit size = %v, want 4x4", got.Bounds())
	}
	if c := coverageAt(got, threshold); c != 0.5 {
		t.Fatalf("fit coverage = %v, want 0.5 (nearest feasible to 0.25)", c)
	}
	if r, b := rgbChannels(got), rgbChannels(Fit(src, 4, 4, Box)); !compareBytes(r, b, 0) {
		t.Fatal("Fit coverage mode changed RGB channels")
	}

	// Image already fits: result equals the source clone with coverage intact.
	noScale := FitCoverage(src, 10, 10, Box, threshold)
	if c := coverageAt(noScale, threshold); c != 0.25 {
		t.Fatalf("no-scale fit coverage = %v, want 0.25", c)
	}

	// Invalid threshold behaves exactly like plain Fit.
	if got := FitCoverage(src, 4, 4, Box, 256); !compareNRGBA(got, Fit(src, 4, 4, Box), 0) {
		t.Fatal("invalid threshold did not fall back to plain Fit")
	}

	// Zero-size arguments are handled like Fit.
	if got := FitCoverage(src, 0, 4, Box, threshold); len(got.Pix) != 0 {
		t.Fatalf("zero width produced non-empty result: %#v", got)
	}
}

// TestThumbnailCoverage checks the Thumbnail entry point, including the crop
// branches used for large and small source images.
func TestThumbnailCoverage(t *testing.T) {
	const threshold = 128

	// Large source: crop-and-resize branch (both dimensions >= 100).
	// Full-height opaque/transparent stripes give 50% coverage. After the
	// center crop and the 2x box downscale every output alpha averages to
	// 128, which falls below threshold 129, so the plain thumbnail loses
	// all coverage. The only feasible counts are 0 or all 2500 pixels,
	// equidistant from the 1250-pixel target; the direction rule restores
	// coverage rather than leaving the thumbnail empty.
	const largeThreshold = 129
	large := newAlphaImage(200, 100, func(x, y int) uint8 {
		if x%2 == 0 {
			return 255
		}
		return 0
	})
	plainLarge := Thumbnail(large, 50, 50, Box)
	if c := coverageAt(plainLarge, largeThreshold); c != 0 {
		t.Fatalf("plain large thumbnail coverage = %v, want 0 (precondition)", c)
	}
	got := ThumbnailCoverage(large, 50, 50, Box, largeThreshold)
	if got.Bounds().Dx() != 50 || got.Bounds().Dy() != 50 {
		t.Fatalf("thumbnail size = %v, want 50x50", got.Bounds())
	}
	if c := coverageAt(got, largeThreshold); c != 1.0 {
		t.Fatalf("large thumbnail coverage = %v, want 1.0 (tie resolved towards the target)", c)
	}
	if r, b := rgbChannels(got), rgbChannels(plainLarge); !compareBytes(r, b, 0) {
		t.Fatal("large thumbnail coverage mode changed RGB channels")
	}
	if r, b := rgbChannels(got), rgbChannels(plainLarge); !compareBytes(r, b, 0) {
		t.Fatal("large thumbnail coverage mode changed RGB channels")
	}

	// Small source: resize-and-crop branch.
	const smallThreshold = 129
	small := newAlphaImage(8, 4, twigAlpha)
	gotSmall := ThumbnailCoverage(small, 4, 4, Box, smallThreshold)
	if gotSmall.Bounds().Dx() != 4 || gotSmall.Bounds().Dy() != 4 {
		t.Fatalf("small thumbnail size = %v, want 4x4", gotSmall.Bounds())
	}
	if r, b := rgbChannels(gotSmall), rgbChannels(Thumbnail(small, 4, 4, Box)); !compareBytes(r, b, 0) {
		t.Fatal("small thumbnail coverage mode changed RGB channels")
	}

	// Invalid threshold falls back to plain Thumbnail.
	if got := ThumbnailCoverage(small, 4, 4, Box, -1); !compareNRGBA(got, Thumbnail(small, 4, 4, Box), 0) {
		t.Fatal("invalid threshold did not fall back to plain Thumbnail")
	}

	// Zero-size arguments are handled like Thumbnail.
	if got := ThumbnailCoverage(small, 0, 4, Box, smallThreshold); len(got.Pix) != 0 {
		t.Fatalf("zero width produced non-empty result: %#v", got)
	}
}

// TestAlphaCoverage checks the coverage estimator on non-NRGBA images and
// images with a non-zero bounds origin.
func TestAlphaCoverage(t *testing.T) {
	src := newAlphaImage(4, 4, checkerboardAlpha)
	if got := alphaCoverage(src, 128); got != 0.5 {
		t.Fatalf("NRGBA coverage = %v, want 0.5", got)
	}
	if got := alphaCoverage(src, 255); got != 0.5 {
		t.Fatalf("coverage at threshold 255 = %v, want 0.5", got)
	}
	if got := alphaCoverage(&image.NRGBA{}, 128); got != 0 {
		t.Fatalf("coverage of empty image = %v, want 0", got)
	}

	// RGBA goes through the generic scanner path.
	rgba := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			i := y*rgba.Stride + x*4
			if (x+y)%2 == 0 {
				rgba.Pix[i+3] = 255
			}
		}
	}
	if got := alphaCoverage(rgba, 128); got != 0.5 {
		t.Fatalf("RGBA coverage = %v, want 0.5", got)
	}

	// Non-zero bounds origin on an NRGBA image, with a backing array large
	// enough to hold the margins.
	backing := image.NewNRGBA(image.Rect(0, 0, 14, 16))
	for y := 9; y < 13; y++ {
		for x := 7; x < 11; x++ {
			backing.SetNRGBA(x, y, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
		}
	}
	shifted := &image.NRGBA{
		Pix:    backing.Pix,
		Stride: backing.Stride,
		Rect:   image.Rect(7, 9, 11, 13),
	}
	if got := alphaCoverage(shifted, 128); got != 1.0 {
		t.Fatalf("shifted-bounds coverage = %v, want 1.0", got)
	}
}

// TestResizeCoverageRandomGraded uses a randomized image with graded alpha
// values, so intermediate survivor counts are attainable. The coverage
// adjustment must never increase the coverage error relative to a plain
// resize, and for a strong downscale it must reduce it substantially.
func TestResizeCoverageRandomGraded(t *testing.T) {
	const (
		N   = 128
		thr = 128
	)
	rnd := rand.New(rand.NewSource(42))
	src := newAlphaImage(N, N, func(x, y int) uint8 {
		r := rnd.Intn(256)
		if r < 120 {
			return 0
		}
		return uint8(r)
	})

	srcCov := alphaCoverage(src, thr)
	var sawReduction bool
	for _, sz := range []int{64, 32, 16, 8, 4} {
		plain := Resize(src, sz, sz, Box)
		got := ResizeCoverage(src, sz, sz, Box, thr)

		// RGB must be byte-identical to the plain resize.
		if r, b := rgbChannels(got), rgbChannels(plain); !compareBytes(r, b, 0) {
			t.Fatalf("size %d: RGB channels changed", sz)
		}

		errPlain := absCoverage(coverageAt(plain, thr) - srcCov)
		errAdjusted := absCoverage(coverageAt(got, thr) - srcCov)
		if errAdjusted > errPlain+1e-9 {
			t.Fatalf("size %d: coverage error grew from %.4f to %.4f", sz, errPlain, errAdjusted)
		}
		if errAdjusted < errPlain {
			sawReduction = true
		}
	}
	if !sawReduction {
		t.Fatal("expected the coverage adjustment to reduce the error for at least one size")
	}
}

func absCoverage(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
