package imaging

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"testing"
)

// solidImage returns an opaque w x h NRGBA image filled with one color.
func solidImage(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	return img
}

// checkerImage returns an alternating 4x4 checkerboard of c1 and c2.
func checkerImage(w, h int, c1, c2 color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := c1
			if ((x/4)+(y/4))%2 == 1 {
				c = c2
			}
			off := y*img.Stride + x*4
			img.Pix[off] = c.R
			img.Pix[off+1] = c.G
			img.Pix[off+2] = c.B
			img.Pix[off+3] = c.A
		}
	}
	return img
}

// addNoise returns a copy of img with additive Gaussian luma noise applied
// independently to every channel of opaque pixels.
func addNoise(img *image.NRGBA, sigma float64, seed int64) *image.NRGBA {
	dst := Clone(img)
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < len(dst.Pix); i += 4 {
		if dst.Pix[i+3] == 0 {
			continue
		}
		for k := 0; k < 3; k++ {
			v := int(dst.Pix[i+k]) + int(math.Round(rng.NormFloat64()*sigma))
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			dst.Pix[i+k] = uint8(v)
		}
	}
	return dst
}

func maxScoreRegion(regions []SharpnessRegion) (int, SharpnessRegion) {
	idx := 0
	for i, r := range regions {
		if r.Score > regions[idx].Score {
			idx = i
		}
	}
	return idx, regions[idx]
}

func TestSharpnessValidation(t *testing.T) {
	src := solidImage(64, 64, color.NRGBA{100, 100, 100, 255})

	testCases := []struct {
		name string
		img  image.Image
		opts *SharpnessOptions
		err  error
	}{
		{"nil image", nil, nil, ErrEmptyImage},
		{"empty bounds", image.NewNRGBA(image.Rect(0, 0, 0, 0)), nil, ErrEmptyImage},
		{"zero window", src, &SharpnessOptions{WindowSize: 0, Step: 8}, ErrZeroWindow},
		{"negative window", src, &SharpnessOptions{WindowSize: -1, Step: 8}, ErrZeroWindow},
		{"zero step", src, &SharpnessOptions{WindowSize: 8, Step: 0}, ErrZeroStep},
		{"negative step", src, &SharpnessOptions{WindowSize: 8, Step: -1}, ErrZeroStep},
		{"NaN threshold", src, &SharpnessOptions{WindowSize: 8, Step: 8, NoiseThreshold: math.NaN()}, ErrNonFiniteThreshold},
		{"+Inf threshold", src, &SharpnessOptions{WindowSize: 8, Step: 8, NoiseThreshold: math.Inf(1)}, ErrNonFiniteThreshold},
		{"-Inf sigma", src, &SharpnessOptions{WindowSize: 8, Step: 8, Robust: true, NoiseSigma: math.Inf(-1)}, ErrNonFiniteThreshold},
		{"NaN sigma", src, &SharpnessOptions{WindowSize: 8, Step: 8, Robust: true, NoiseSigma: math.NaN()}, ErrNonFiniteThreshold},
		{"negative threshold", src, &SharpnessOptions{WindowSize: 8, Step: 8, NoiseThreshold: -1}, ErrNegativeThreshold},
		{"negative sigma", src, &SharpnessOptions{WindowSize: 8, Step: 8, Robust: true, NoiseSigma: -1}, ErrNegativeNoiseSigma},
		{"oversized image", src, &SharpnessOptions{WindowSize: 8, Step: 8, MaxPixels: 63}, ErrImageTooLarge},
		{"oversized grid", src, &SharpnessOptions{WindowSize: 8, Step: 1, MaxPixels: 10}, ErrImageTooLarge},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Sharpness(tc.img, tc.opts)
			if err != tc.err {
				t.Fatalf("got error %v want %v", err, tc.err)
			}
			if got != nil {
				t.Fatalf("expected nil result, got %#v", got)
			}
		})
	}
}

func TestSharpnessSolidIsZero(t *testing.T) {
	// A solid color carries no gradient energy: every window must score zero
	// regardless of the suppression settings.
	src := solidImage(48, 48, color.NRGBA{30, 120, 200, 255})
	for _, robust := range []bool{false, true} {
		m, err := Sharpness(src, &SharpnessOptions{WindowSize: 16, Step: 16, Robust: robust})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, r := range m.Regions {
			if math.Abs(r.Score) > 1e-12 {
				t.Fatalf("solid image window %v score %f, want 0", r.Rect, r.Score)
			}
			if r.Coverage != 1 {
				t.Fatalf("coverage %f want 1", r.Coverage)
			}
		}
	}
}

func TestSharpnessTransparent(t *testing.T) {
	// Fully transparent image: zero scores, zero coverage, but a full-size
	// heatmap in the original coordinate system.
	src := image.NewNRGBA(image.Rect(0, 0, 24, 24))
	m, err := Sharpness(src, &SharpnessOptions{WindowSize: 8, Step: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Regions) != 9 {
		t.Fatalf("got %d regions, want 9", len(m.Regions))
	}
	for _, r := range m.Regions {
		if r.Score != 0 || r.Coverage != 0 {
			t.Fatalf("transparent window %+v must be zero, got score=%f coverage=%f", r, r.Score, r.Coverage)
		}
	}
	if !m.Heatmap.Bounds().Eq(src.Bounds()) || !m.GrayHeatmap.Bounds().Eq(src.Bounds()) {
		t.Fatalf("heatmap bounds %v / %v, want %v", m.Heatmap.Bounds(), m.GrayHeatmap.Bounds(), src.Bounds())
	}
	for i := 3; i < len(m.Heatmap.Pix); i += 4 {
		if m.Heatmap.Pix[i] != 0xff {
			t.Fatalf("heatmap alpha must be opaque")
		}
	}

	// Half-transparent window: only opaque pixels count, coverage is reported.
	src2 := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			off := y*src2.Stride + x*4
			src2.Pix[off] = 255
			src2.Pix[off+3] = 255
		}
	}
	m2, err := Sharpness(src2, &SharpnessOptions{WindowSize: 8, Step: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(m2.Regions[0].Coverage-0.5) > 1e-9 {
		t.Fatalf("coverage %f want 0.5", m2.Regions[0].Coverage)
	}
	// The opaque half is flat and the alpha boundary must not manufacture
	// gradient energy; the window therefore still scores zero in both modes.
	for _, robust := range []bool{false, true} {
		m3, err := Sharpness(src2, &SharpnessOptions{WindowSize: 8, Step: 8, Robust: robust})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if math.Abs(m3.Regions[0].Score) > 1e-12 {
			t.Fatalf("flat opaque region next to transparency must score zero (robust=%v), got %g",
				robust, m3.Regions[0].Score)
		}
	}
}

func TestSharpnessBoundsAndPartialWindows(t *testing.T) {
	// Non-zero Bounds.Min must be preserved through every reported rectangle.
	src := image.NewNRGBA(image.Rect(100, 200, 120, 216)) // 20x16, offset (100,200)
	for i := range src.Pix {
		src.Pix[i] = 0
	}
	for y := 0; y < 16; y++ {
		for x := 0; x < 20; x++ {
			c := uint8(0)
			if x >= 10 {
				c = 255 // sharp vertical edge inside the image
			}
			off := y*src.Stride + x*4
			src.Pix[off] = c
			src.Pix[off+1] = c
			src.Pix[off+2] = c
			src.Pix[off+3] = 255
		}
	}

	m, err := Sharpness(src, &SharpnessOptions{WindowSize: 12, Step: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.Bounds.Eq(src.Bounds()) {
		t.Fatalf("map bounds %v want %v", m.Bounds, src.Bounds())
	}
	if !m.Heatmap.Bounds().Eq(src.Bounds()) {
		t.Fatalf("heatmap bounds %v want %v", m.Heatmap.Bounds(), src.Bounds())
	}

	// 20/16 pixels with step 8 -> cols = 1+floor(19/8) = 3, rows = 1+floor(15/8) = 2.
	if len(m.Regions) != 6 {
		t.Fatalf("got %d regions want 6", len(m.Regions))
	}
	for _, r := range m.Regions {
		if !r.Rect.In(src.Bounds()) {
			t.Fatalf("region rect %v outside source bounds %v", r.Rect, src.Bounds())
		}
		if r.Rect.Empty() {
			t.Fatalf("empty region rect")
		}
	}
	// Last column and row windows are clipped, never dropped.
	last := m.Regions[5]
	if last.Rect.Max.X != 120 || last.Rect.Max.Y != 216 {
		t.Fatalf("partial window %v should reach the image corner (120,216)", last.Rect)
	}
	if last.Rect.Dx() != 4 || last.Rect.Dy() != 8 {
		t.Fatalf("partial window size %dx%d, want 4x8", last.Rect.Dx(), last.Rect.Dy())
	}

	// The rendering maps each pixel to exactly one window via floor division
	// by the step, so the painted heatmap covers every source pixel once.
	if len(m.Heatmap.Pix) != 20*16*4 || len(m.GrayHeatmap.Pix) != 20*16*2 {
		t.Fatalf("heatmap has %d color / %d gray bytes, want full-size buffers",
			len(m.Heatmap.Pix), len(m.GrayHeatmap.Pix))
	}
	// The union of the window rectangles still covers every source pixel.
	seen := make(map[image.Point]bool)
	for _, r := range m.Regions {
		for y := r.Rect.Min.Y; y < r.Rect.Max.Y; y++ {
			for x := r.Rect.Min.X; x < r.Rect.Max.X; x++ {
				seen[image.Pt(x, y)] = true
			}
		}
	}
	if len(seen) != 20*16 {
		t.Fatalf("window union covers %d pixels, want %d", len(seen), 20*16)
	}
}

func TestSharpnessNoBorderArtifacts(t *testing.T) {
	// Regression: edge-clamped neighbors must not turn image or window borders
	// into fake edges. A flat image with a window that touches every border
	// must still score zero.
	src := solidImage(40, 48, color.NRGBA{70, 130, 200, 255})
	for _, robust := range []bool{false, true} {
		m, err := Sharpness(src, &SharpnessOptions{WindowSize: 40, Step: 40, Robust: robust})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 1 column, 2 rows; the second row window is clipped at the border.
		if len(m.Regions) != 2 {
			t.Fatalf("got %d regions, want 2", len(m.Regions))
		}
		for _, r := range m.Regions {
			if math.Abs(r.Score) > 1e-12 {
				t.Fatalf("border-touching flat window %v must score zero (robust=%v), got %g",
					r.Rect, robust, r.Score)
			}
		}
	}

	// A flat strip placed next to different content keeps its own window score
	// at zero because the border row/column is excluded from aggregation.
	pair := image.NewNRGBA(image.Rect(0, 0, 24, 8))
	for x := 0; x < 12; x++ {
		for y := 0; y < 8; y++ {
			off := y*pair.Stride + x*4
			pair.Pix[off] = 0
			pair.Pix[off+1] = 0
			pair.Pix[off+2] = 0
			pair.Pix[off+3] = 255
		}
	}
	for x := 12; x < 24; x++ {
		for y := 0; y < 8; y++ {
			off := y*pair.Stride + x*4
			pair.Pix[off] = 200
			pair.Pix[off+1] = 200
			pair.Pix[off+2] = 200
			pair.Pix[off+3] = 255
		}
	}
	m, err := Sharpness(pair, &SharpnessOptions{WindowSize: 12, Step: 12})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Regions) != 2 {
		t.Fatalf("got %d regions, want 2", len(m.Regions))
	}
	for i, r := range m.Regions {
		// Each half is internally uniform; the single shared edge lives on the
		// excluded border of both windows, so both must read as flat.
		if math.Abs(r.Score) > 1e-12 {
			t.Fatalf("uniform window %d %v must score zero, got %g", i, r.Rect, r.Score)
		}
	}
}

func TestSharpnessRanking(t *testing.T) {
	// Three 32x32 patches of identical content side by side:
	// sharp, defocused (Gaussian blur), noisy solid. Window size 32 gives one
	// window per patch so ranking is unambiguous.
	const n = 32
	sharp := checkerImage(n, n, color.NRGBA{20, 20, 20, 255}, color.NRGBA{220, 220, 220, 255})
	defocused := Blur(sharp, 2.0)
	noisy := addNoise(solidImage(n, n, color.NRGBA{128, 128, 128, 255}), 8, 1)

	canvas := image.NewNRGBA(image.Rect(0, 0, 3*n, n))
	canvas = Paste(canvas, sharp, image.Pt(0, 0))
	canvas = Paste(canvas, defocused, image.Pt(n, 0))
	canvas = Paste(canvas, noisy, image.Pt(2*n, 0))

	m, err := Sharpness(canvas, &SharpnessOptions{WindowSize: n, Step: n})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Regions) != 3 {
		t.Fatalf("got %d regions, want 3", len(m.Regions))
	}
	sSharp, sBlur, sNoisy := m.Regions[0].Score, m.Regions[1].Score, m.Regions[2].Score

	// 1. Defocused texture must still beat the solid noisy patch, and sharp
	//    texture must beat defocused texture: gradient energy distinguishes
	//    "solid" from "defocused texture".
	if !(sSharp > sBlur && sBlur > sNoisy) {
		t.Fatalf("ranking sharp(%f) > defocused(%f) > noisy-solid(%f) failed", sSharp, sBlur, sNoisy)
	}
	if sNoisy > sBlur*0.5 {
		t.Fatalf("noisy solid score %f should stay well below defocused %f", sNoisy, sBlur)
	}

	// 2. Position check: the maximum-score window must be the leftmost patch.
	idx, top := maxScoreRegion(m.Regions)
	if idx != 0 || top.Rect.Min.X != 0 {
		t.Fatalf("sharpest window index %d rect %v, want leftmost patch", idx, top.Rect)
	}
}

func TestSharpnessRobustSuppression(t *testing.T) {
	// With the fixed threshold disabled, plain mode lets heavy noise through;
	// robust mode must suppress the same noisy solid patch back toward zero.
	const n = 48
	noisy := addNoise(solidImage(n, n, color.NRGBA{90, 90, 90, 255}), 20, 7)
	sharp := checkerImage(n, n, color.NRGBA{10, 10, 10, 255}, color.NRGBA{240, 240, 240, 255})

	canvas := image.NewNRGBA(image.Rect(0, 0, 2*n, n))
	canvas = Paste(canvas, sharp, image.Pt(0, 0))
	canvas = Paste(canvas, noisy, image.Pt(n, 0))

	plain, err := Sharpness(canvas, &SharpnessOptions{WindowSize: n, Step: n, NoiseThreshold: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	robust, err := Sharpness(canvas, &SharpnessOptions{WindowSize: n, Step: n, NoiseThreshold: 0, Robust: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if plain.Regions[1].Score <= 0 {
		t.Fatalf("plain mode with no suppression should register noise, got 0")
	}
	if robust.Regions[1].Score >= plain.Regions[1].Score {
		t.Fatalf("robust score %f should be below plain score %f for noisy solid",
			robust.Regions[1].Score, plain.Regions[1].Score)
	}
	// Robust mode should suppress the pure-noise patch to (near) zero: the
	// adaptive floor exceeds the median residual of unstructured noise.
	if robust.Regions[1].Score > 1e-9 {
		t.Fatalf("robust mode should (near-)fully suppress pure noise, got %f", robust.Regions[1].Score)
	}
	// The genuinely sharp patch must remain sharpest in both modes.
	if plain.Regions[0].Score <= plain.Regions[1].Score {
		t.Fatalf("sharp patch must outrank noise in plain mode")
	}
	if robust.Regions[0].Score <= robust.Regions[1].Score || robust.Regions[0].Score == 0 {
		t.Fatalf("sharp patch must stay sharp under robust mode, got %f vs %f",
			robust.Regions[0].Score, robust.Regions[1].Score)
	}

	// A pinned NoiseSigma matching the true level suppresses the noise just
	// like the per-window MAD estimate (both within a small tolerance).
	pinned, err := Sharpness(canvas, &SharpnessOptions{
		WindowSize: n, Step: n, NoiseThreshold: 0, Robust: true, NoiseSigma: 20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(pinned.Regions[1].Score-robust.Regions[1].Score) > 1e-6 {
		t.Fatalf("pinned sigma noisy score %f differs from robust %f", pinned.Regions[1].Score, robust.Regions[1].Score)
	}
}

func TestSharpnessFilterConsistency(t *testing.T) {
	// Cross-check against the package's own filters: blurring must lower the
	// window score, sharpening must raise it again, and resizing with
	// NearestNeighbor must preserve more edge energy than the Gaussian filter.
	src := checkerImage(64, 64, color.NRGBA{0, 0, 0, 255}, color.NRGBA{255, 255, 255, 255})
	score := func(img image.Image) float64 {
		m, err := Sharpness(img, &SharpnessOptions{WindowSize: 32, Step: 32})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_, r := maxScoreRegion(m.Regions)
		return r.Score
	}

	sSharp := score(src)
	sBlur := score(Blur(src, 1.5))
	if !(sSharp > sBlur) {
		t.Fatalf("blur must reduce sharpness: sharp=%f blurred=%f", sSharp, sBlur)
	}

	resharpened := Sharpen(Blur(src, 1.5), 1.5)
	sResharp := score(resharpened)
	if sResharp <= sBlur {
		t.Fatalf("sharpen must restore edge energy: blurred=%f resharpened=%f", sBlur, sResharp)
	}

	nearest := Resize(src, 128, 128, NearestNeighbor)
	gauss := Resize(src, 128, 128, Gaussian)
	if score(nearest) < score(gauss) {
		t.Fatalf("nearest-neighbor upscale must keep at least as much edge energy as Gaussian")
	}
}

func TestSharpnessDeterminismAndHeatmap(t *testing.T) {
	src := addNoise(checkerImage(40, 40, color.NRGBA{30, 30, 30, 255}, color.NRGBA{200, 200, 200, 255}), 5, 3)
	opts := &SharpnessOptions{WindowSize: 10, Step: 10, Robust: true}

	m1, err := Sharpness(src, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m2, err := Sharpness(src, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m1.Regions) != len(m2.Regions) {
		t.Fatalf("non-deterministic region count")
	}
	for i := range m1.Regions {
		if m1.Regions[i] != m2.Regions[i] {
			t.Fatalf("non-deterministic region %d: %+v vs %+v", i, m1.Regions[i], m2.Regions[i])
		}
	}
	if !compareNRGBA(m1.Heatmap, m2.Heatmap, 0) {
		t.Fatalf("non-deterministic color heatmap")
	}

	// Gray16 heatmap matches the region score painted over the window.
	for _, r := range m1.Regions {
		want := uint16(r.Score*65535 + 0.5)
		// Sample the window center.
		cx, cy := r.Rect.Min.X+1, r.Rect.Min.Y+1
		off := (cy-m1.Bounds.Min.Y)*m1.GrayHeatmap.Stride + (cx-m1.Bounds.Min.X)*2
		got := uint16(m1.GrayHeatmap.Pix[off])<<8 | uint16(m1.GrayHeatmap.Pix[off+1])
		if got != want {
			t.Fatalf("gray heatmap value %d at %v, want %d (score %f)", got, r.Rect.Min, want, r.Score)
		}
	}

	// A zero-score window paints pure blue; the sharpest window paints toward
	// red. This pins the color ramp orientation.
	solid := solidImage(10, 10, color.NRGBA{120, 120, 120, 255})
	ms, err := Sharpness(solid, &SharpnessOptions{WindowSize: 10, Step: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	blue := ms.Heatmap.Pix[:4]
	if !(blue[0] == 0 && blue[1] == 0 && blue[2] == 255) {
		t.Fatalf("zero score color %v, want pure blue", blue[:3])
	}
	_, top := maxScoreRegion(m1.Regions)
	if top.Score == 0 {
		t.Fatalf("checkerboard should contain a sharp window")
	}
	off := (top.Rect.Min.Y-m1.Bounds.Min.Y)*m1.Heatmap.Stride + (top.Rect.Min.X-m1.Bounds.Min.X)*4
	rr, gg, bb := m1.Heatmap.Pix[off], m1.Heatmap.Pix[off+1], m1.Heatmap.Pix[off+2]
	// The painted color must match the documented blue->green->red ramp for
	// the window's score.
	er, eg, eb := sharpnessColor(top.Score)
	if rr != er || gg != eg || bb != eb {
		t.Fatalf("heatmap color (%d,%d,%d) does not match ramp (%d,%d,%d) for score %f",
			rr, gg, bb, er, eg, eb, top.Score)
	}
}

func TestSharpnessColorRamp(t *testing.T) {
	testCases := []struct {
		score   float64
		r, g, b uint8
	}{
		{-0.5, 0, 0, 255}, // clamped below
		{0, 0, 0, 255},
		{0.5, 0, 255, 0},
		{1, 255, 0, 0},
		{1.5, 255, 0, 0}, // clamped above
	}
	for _, tc := range testCases {
		r, g, b := sharpnessColor(tc.score)
		if r != tc.r || g != tc.g || b != tc.b {
			t.Fatalf("sharpnessColor(%f) = (%d,%d,%d), want (%d,%d,%d)",
				tc.score, r, g, b, tc.r, tc.g, tc.b)
		}
	}
}

func TestSharpnessDefaults(t *testing.T) {
	src := checkerImage(64, 64, color.NRGBA{0, 0, 0, 255}, color.NRGBA{255, 255, 255, 255})
	m, err := Sharpness(src, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.WindowSize != defaultSharpnessWindow || m.Step != defaultSharpnessStep {
		t.Fatalf("defaults %d/%d, want %d/%d", m.WindowSize, m.Step, defaultSharpnessWindow, defaultSharpnessStep)
	}
	if len(m.Regions) != 4 {
		t.Fatalf("got %d default-grid regions on 64x64, want 4", len(m.Regions))
	}
}

func BenchmarkSharpness(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Sharpness(testdataBranchesJPG, &SharpnessOptions{WindowSize: 32, Step: 32, Robust: true}); err != nil {
			b.Fatal(err)
		}
	}
}
