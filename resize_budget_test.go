package imaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// intBits is the width of the platform int type in bits.
const intBits = int(unsafe.Sizeof(int(0)) * 8)

// namedFilter pairs a filter with its name for subtest labels.
type namedFilter struct {
	name string
	f    ResampleFilter
}

// allResampleFilters lists every non-nearest filter so the equivalence tests
// exercise kernels with support radii 0.5 through 3. It is a function (not a
// package variable) because the filter globals are populated in init().
func allResampleFilters() []namedFilter {
	return []namedFilter{
		{"Box", Box}, {"Linear", Linear}, {"Hermite", Hermite},
		{"MitchellNetravali", MitchellNetravali}, {"CatmullRom", CatmullRom},
		{"BSpline", BSpline}, {"Gaussian", Gaussian}, {"Bartlett", Bartlett},
		{"Lanczos", Lanczos}, {"Hann", Hann}, {"Hamming", Hamming},
		{"Blackman", Blackman}, {"Welch", Welch}, {"Cosine", Cosine},
	}
}

// makeRandomNRGBA builds a deterministic pseudo-random NRGBA image. The alpha
// channel includes fully transparent, fully opaque and partial values so that
// both the alpha-weighted color path and the a==0 zeroing path are exercised.
func makeRandomNRGBA(r image.Rectangle, seed uint64) *image.NRGBA {
	img := image.NewNRGBA(r)
	state := seed
	next := func() uint8 {
		// xorshift64*
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		return uint8((state * 0x2545F4914F6CDD1D) >> 56)
	}
	for i := range img.Pix {
		v := next()
		img.Pix[i] = v
	}
	// Force a spread of alpha values on every fourth pixel.
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := img.PixOffset(x, y) + 3
			switch (x*7 + y*13) % 5 {
			case 0:
				img.Pix[i] = 0
			case 1:
				img.Pix[i] = 255
			default:
				img.Pix[i] = 1 + next()%254
			}
		}
	}
	return img
}

// makeRandomRGBA builds an RGBA image with premultiplied colors so the
// scanner's unpremultiply path is covered too.
func makeRandomRGBA(r image.Rectangle, seed uint64) *image.RGBA {
	src := makeRandomNRGBA(r, seed)
	img := image.NewRGBA(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := src.PixOffset(x, y)
			c := color.NRGBA{
				R: src.Pix[i], G: src.Pix[i+1], B: src.Pix[i+2], A: src.Pix[i+3],
			}
			r16, g16, b16, a16 := color.NRGBAModel.Convert(c).RGBA()
			j := img.PixOffset(x, y)
			img.Pix[j] = uint8(r16 >> 8)
			img.Pix[j+1] = uint8(g16 >> 8)
			img.Pix[j+2] = uint8(b16 >> 8)
			img.Pix[j+3] = uint8(a16 >> 8)
		}
	}
	return img
}

// TestBandedResizeEquivalence verifies byte-for-byte equivalence between the
// legacy unbounded composition and the budgeted implementation across image
// types, non-zero bounds, narrow/wide shapes, every resampling filter and
// several banding budgets (including budgets that force one-row bands).
func TestBandedResizeEquivalence(t *testing.T) {
	type shape struct {
		name       string
		srcW, srcH int
		dstW, dstH int
	}
	shapes := []shape{
		{"tiny", 3, 3, 2, 2},
		{"narrow-tall", 2, 37, 1, 11},
		{"wide-short", 41, 3, 13, 2},
		{"upscale", 7, 5, 23, 19},
		{"downscale", 64, 48, 17, 13},
		{"mixed", 31, 53, 47, 19},
		{"strip-1px", 1, 30, 1, 12},
		{"strip-2px", 2, 40, 5, 30},
	}
	rects := []image.Rectangle{
		image.Rect(0, 0, 0, 0), // placeholder, replaced per shape
		image.Rect(-7, -3, 0, 0),
		image.Rect(100, 200, 0, 0),
	}

	for _, sh := range shapes {
		rects[0] = image.Rect(0, 0, sh.srcW, sh.srcH)
		rects[1] = image.Rect(-7, -3, -7+sh.srcW, -3+sh.srcH)
		rects[2] = image.Rect(100, 200, 100+sh.srcW, 200+sh.srcH)
		for ri, r := range rects {
			nrgba := makeRandomNRGBA(r, 0x9E3779B97F4A7C15+uint64(sh.srcW)*131+uint64(sh.srcH)*17)
			rgba := makeRandomRGBA(r, 0xD1B54A32D192ED03+uint64(sh.dstW)*977+uint64(sh.dstH)*31)
			gray := image.NewGray(r)
			for i := range gray.Pix {
				gray.Pix[i] = nrgba.Pix[i*4]
			}
			imgs := []struct {
				name string
				img  image.Image
			}{
				{"NRGBA", nrgba},
				{"RGBA", rgba},
				{"Gray", gray},
			}
			for _, ii := range imgs {
				for _, nf := range allResampleFilters() {
					f := nf.f
					want := legacyResizeTwoPass(ii.img, sh.dstW, sh.dstH, f)

					// Minimum feasible budget for this filter: the largest
					// single-output-row kernel footprint, i.e. the tightest
					// budget that must still succeed.
					vw, perr := precomputeWeights(sh.dstH, sh.srcH, f)
					if perr != nil {
						t.Fatal(perr)
					}
					minRows := 0
					for _, ws := range vw {
						if n := len(ws); n > 0 {
							if span := ws[len(ws)-1].index - ws[0].index + 1; span > minRows {
								minRows = span
							}
						}
					}
					minBudget := int64(minRows) * int64(sh.dstW) * 4
					budgets := []int64{
						0,         // unlimited: single full-height band
						minBudget, // tightest valid budget, forces single-row bands
						minBudget * 3,
						1 << 20, // 1 MiB
					}
					for _, budget := range budgets {
						name := fmt.Sprintf("%s/rect%d/%s/%s/budget%d", sh.name, ri, ii.name,
							nf.name, budget)
						t.Run(name, func(t *testing.T) {
							got, err := ResizeWithBuffer(ii.img, sh.dstW, sh.dstH, f, budget)
							if err != nil {
								t.Fatalf("ResizeWithBuffer returned error: %v", err)
							}
							if !compareNRGBA(got, want, 0) {
								t.Fatalf("banded result differs from legacy composition")
							}
						})
					}
				}
			}
		}
	}
}

// TestBandedResizeParallel verifies that results do not depend on the number
// of worker goroutines: the parallel helpers share one scratch buffer per band,
// so any cross-goroutine contamination would show up here.
func TestBandedResizeParallel(t *testing.T) {
	src := makeRandomNRGBA(image.Rect(-5, -9, 59, 43), 12345)
	want := legacyResizeTwoPass(src, 33, 27, Lanczos)
	// Smallest feasible budget for this geometry/filter: the widest single
	// vertical kernel footprint (forces one-row bands).
	vw, err := precomputeWeights(27, 52, Lanczos)
	if err != nil {
		t.Fatal(err)
	}
	minRows := 0
	for _, ws := range vw {
		if span := ws[len(ws)-1].index - ws[0].index + 1; span > minRows {
			minRows = span
		}
	}
	minBudget := int64(minRows) * 33 * 4
	for _, procs := range []int{1, 2, 3, 16} {
		SetMaxProcs(procs)
		for _, budget := range []int64{minBudget, minBudget * 2} {
			got, err := ResizeWithBuffer(src, 33, 27, Lanczos, budget)
			if err != nil {
				t.Fatalf("procs=%d budget=%d: %v", procs, budget, err)
			}
			if !compareNRGBA(got, want, 0) {
				t.Fatalf("procs=%d budget=%d: banded result differs from legacy composition", procs, budget)
			}
		}
	}
	SetMaxProcs(0)
}

// TestBandedResizeTransparentAlpha makes sure fully transparent samples are
// emitted as zero even though the scratch buffer is reused across bands. The
// image contains large zero-alpha regions that would expose stale data.
func TestBandedResizeTransparentAlpha(t *testing.T) {
	r := image.Rect(0, 0, 60, 60)
	src := image.NewNRGBA(r)
	// Opaque checkerboard in the top half, fully transparent bottom half.
	for y := 0; y < 30; y++ {
		for x := 0; x < 60; x++ {
			i := src.PixOffset(x, y)
			if (x/5+y/5)%2 == 0 {
				src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 200, 100, 50, 255
			} else {
				src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 10, 20, 30, 255
			}
		}
	}
	// Tightest feasible budget for this geometry: the widest single-row
	// Lanczos footprint, plus a deliberately too-small budget that must
	// return ErrResizeBufferTooSmall.
	vw40, err := precomputeWeights(40, 60, Lanczos)
	if err != nil {
		t.Fatal(err)
	}
	minRows40 := 0
	for _, ws := range vw40 {
		if span := ws[len(ws)-1].index - ws[0].index + 1; span > minRows40 {
			minRows40 = span
		}
	}
	minBudget40 := int64(minRows40) * 40 * 4
	for _, budget := range []int64{minBudget40, minBudget40 * 2, 4096, 0} {
		got, err := ResizeWithBuffer(src, 40, 40, Lanczos, budget)
		if err != nil {
			t.Fatalf("budget=%d: %v", budget, err)
		}
		want := legacyResizeTwoPass(src, 40, 40, Lanczos)
		if !compareNRGBA(got, want, 0) {
			t.Fatalf("budget=%d: transparent-region result differs", budget)
		}
		// Where the legacy result is fully transparent, the banded result
		// must be zero too. Without the explicit a==0 zeroing, reused scratch
		// rows would leak stale pixels from previous bands through the
		// vertical pass.
		for i := 0; i < len(want.Pix); i += 4 {
			if want.Pix[i] == 0 && want.Pix[i+1] == 0 && want.Pix[i+2] == 0 && want.Pix[i+3] == 0 {
				if got.Pix[i] != 0 || got.Pix[i+1] != 0 || got.Pix[i+2] != 0 || got.Pix[i+3] != 0 {
					t.Fatalf("budget=%d: stale scratch pixel at offset %d: %v", budget, i, got.Pix[i:i+4])
				}
			}
		}
	}
}

// TestPlanResizeBands checks the band planner directly: each band's footprint
// stays within the budget, footprints cover every weight index, adjacent
// bands are split on output-row boundaries, and an unlimited budget produces
// exactly one band.
func TestPlanResizeBands(t *testing.T) {
	for _, tc := range []struct {
		dstH, srcH int
		filter     ResampleFilter
	}{
		{17, 53, Lanczos},
		{40, 40, Linear}, // scale 1: vertical weights are single indices
		{100, 7, Box},    // upscaling: scale clamps to 1, support radius 1
		{3, 200, Lanczos},
		{50, 50, CatmullRom},
	} {
		weights, err := precomputeWeights(tc.dstH, tc.srcH, tc.filter)
		if err != nil {
			t.Fatalf("precomputeWeights: %v", err)
		}
		rowBytes := int64(23 * 4)
		// Derive the tightest feasible budget for this weight set.
		minFootprint := int64(0)
		for _, ws := range weights {
			if len(ws) > 0 {
				span := int64(ws[len(ws)-1].index - ws[0].index + 1)
				if n := span * rowBytes; n > minFootprint {
					minFootprint = n
				}
			}
		}
		// A budget smaller than any single-row footprint must be rejected
		// explicitly rather than degraded to a full-size allocation.
		if _, _, err := planResizeBands(weights, rowBytes, minFootprint-1); !errors.Is(err, ErrResizeBufferTooSmall) {
			t.Fatalf("dstH=%d srcH=%d: tight budget got %v, want ErrResizeBufferTooSmall", tc.dstH, tc.srcH, err)
		}
		for _, budget := range []int64{minFootprint, minFootprint * 3, 1 << 16, math.MaxInt64} {
			bands, scratchRows, err := planResizeBands(weights, rowBytes, budget)
			if err != nil {
				t.Fatalf("dstH=%d srcH=%d budget=%d: %v", tc.dstH, tc.srcH, budget, err)
			}
			if budget == math.MaxInt64 {
				if len(bands) != 1 {
					t.Fatalf("unlimited budget produced %d bands, want 1", len(bands))
				}
			}
			if len(bands) == 0 || bands[0].y0 != 0 || bands[len(bands)-1].y1 != tc.dstH {
				t.Fatalf("bands do not cover all output rows: %+v", bands)
			}
			// scratchRows is the max per-band footprint.
			maxRows := 0
			prevY1 := 0
			for bi, b := range bands {
				if b.y0 != prevY1 {
					t.Fatalf("band %d starts at %d, want %d", bi, b.y0, prevY1)
				}
				prevY1 = b.y1
				rows := b.src1 - b.src0
				if rows > maxRows {
					maxRows = rows
				}
				if int64(rows)*rowBytes > budget && budget > 0 {
					t.Fatalf("band %d exceeds budget: %d > %d", bi, int64(rows)*rowBytes, budget)
				}
				// Every weight index of covered rows must lie inside the band.
				for y := b.y0; y < b.y1; y++ {
					for _, w := range weights[y] {
						if w.index < b.src0 || w.index >= b.src1 {
							t.Fatalf("band %+v misses index %d needed by row %d", b, w.index, y)
						}
					}
				}
			}
			if maxRows != scratchRows {
				t.Fatalf("scratchRows=%d but max band rows=%d", scratchRows, maxRows)
			}
		}
	}
}

// TestBudgetTooSmall verifies that budgets below the single-row footprint
// fail explicitly instead of silently allocating a full intermediate image.
func TestBudgetTooSmall(t *testing.T) {
	// Lanczos downscaling 200 -> 3 rows reaches up to ceil(scale*3) source
	// rows per output row; a 1-byte budget can never hold a 40-pixel row.
	src := makeRandomNRGBA(image.Rect(0, 0, 40, 200), 42)
	_, err := ResizeWithBuffer(src, 10, 3, Lanczos, 1)
	if !errors.Is(err, ErrResizeBufferTooSmall) {
		t.Fatalf("got %v, want ErrResizeBufferTooSmall", err)
	}

	// A budget that fits one destination row but not one kernel footprint
	// must fail as well. Use a wide-support custom filter.
	wide := ResampleFilter{
		Support: 100,
		Kernel: func(x float64) float64 {
			if math.Abs(x) < 100 {
				return 1
			}
			return 0
		},
	}
	_, err = ResizeWithBuffer(src, 10, 50, wide, 40)
	if !errors.Is(err, ErrResizeBufferTooSmall) {
		t.Fatalf("wide kernel: got %v, want ErrResizeBufferTooSmall", err)
	}

	// Resize must surface the same condition as a panic rather than silently
	// ignoring the package-wide budget.
	SetResizeBufferBytes(1)
	defer SetResizeBufferBytes(0)
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("Resize did not panic on an insufficient budget")
			}
			if !errors.Is(r.(error), ErrResizeBufferTooSmall) {
				t.Fatalf("panic value %v does not wrap ErrResizeBufferTooSmall", r)
			}
		}()
		Resize(src, 10, 3, Lanczos)
	}()
}

// TestBudgetRespected instruments the planner to prove no band allocation can
// exceed the configured budget during a real two-axis resize.
func TestBudgetRespected(t *testing.T) {
	src := makeRandomNRGBA(image.Rect(-3, -7, 77, 65), 7)
	const budget = 8192
	for _, nf := range allResampleFilters() {
		f := nf.f
		got, err := ResizeWithBuffer(src, 31, 29, f, budget)
		if err != nil {
			t.Fatalf("%s: %v", nf.name, err)
		}
		want := legacyResizeTwoPass(src, 31, 29, f)
		if !compareNRGBA(got, want, 0) {
			t.Fatalf("%s: output mismatch", nf.name)
		}
		// Re-derive the plan and check every band independently.
		vw, err := precomputeWeights(29, 72, f)
		if err != nil {
			t.Fatal(err)
		}
		bands, _, err := planResizeBands(vw, 31*4, budget)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bands {
			if int64(b.src1-b.src0)*31*4 > budget {
				t.Fatalf("%s: band footprint exceeds budget", nf.name)
			}
		}
	}
}

// TestOverflowGuards verifies explicit errors for dimension multiplication
// overflow and for unallocatable intermediate buffers.
func TestOverflowGuards(t *testing.T) {
	// A filter whose support radius cannot be safely converted to an int
	// allocation size: precomputeWeights must reject it.
	huge := ResampleFilter{
		Support: math.MaxFloat64,
		Kernel:  func(x float64) float64 { return 1 },
	}
	_, err := precomputeWeights(2, 2, huge)
	if !errors.Is(err, ErrResizeDimensionsTooLarge) {
		t.Fatalf("precomputeWeights huge support: got %v", err)
	}
	small := makeRandomNRGBA(image.Rect(0, 0, 4, 4), 1)
	if _, err := ResizeWithBuffer(small, 2, 2, huge, 0); !errors.Is(err, ErrResizeDimensionsTooLarge) {
		t.Fatalf("huge filter resize: got %v, want ErrResizeDimensionsTooLarge", err)
	}

	// Dimensions whose pixel-byte product overflows int on this platform.
	if intBits == 32 {
		_, err = ResizeWithBuffer(small, math.MaxInt32, 2, Lanczos, 0)
		if !errors.Is(err, ErrResizeDimensionsTooLarge) {
			t.Fatalf("32-bit overflow: got %v", err)
		}
	} else {
		// On 64-bit ports an int64 product cannot wrap, but a width whose
		// row itself does not fit must still be rejected.
		_, err = ResizeWithBuffer(small, (math.MaxInt64/4)+1, 2, Lanczos, 0)
		if !errors.Is(err, ErrResizeDimensionsTooLarge) {
			t.Fatalf("row overflow: got %v", err)
		}
	}

	// Planner-level guard: footprints that cannot be represented as int
	// allocations must fail explicitly instead of wrapping. With a two-row
	// footprint at the maximum per-row byte size, the product cannot fit in
	// an int; an unlimited budget (MaxInt64) makes this an overflow rather
	// than an insufficient-budget condition.
	_, _, err = planResizeBands(
		[][]indexWeight{{{index: 0, weight: 1}, {index: 1, weight: 1}}},
		math.MaxInt64, math.MaxInt64)
	if !errors.Is(err, ErrResizeDimensionsTooLarge) {
		t.Fatalf("planner overflow: got %v", err)
	}
}

// TestSingleAxisUnchanged covers the paths where only one axis changes: they
// keep their original single-pass implementation and must match the legacy
// output while ignoring the buffer budget entirely.
func TestSingleAxisUnchanged(t *testing.T) {
	src := makeRandomNRGBA(image.Rect(-4, -2, 28, 22), 99)
	for _, tc := range []struct {
		w, h int
		f    ResampleFilter
	}{
		{13, 24, Lanczos}, // horizontal only
		{32, 11, Box},     // vertical only
		{32, 24, Linear},  // unchanged size -> Clone path
	} {
		got, err := ResizeWithBuffer(src, tc.w, tc.h, tc.f, 1)
		if err != nil {
			t.Fatal(err)
		}
		var want *image.NRGBA
		if tc.w == 32 && tc.h == 24 {
			want = Clone(src)
		} else if tc.h == 24 {
			want = legacyResizeHorizontal(src, tc.w, tc.f)
		} else {
			want = legacyResizeVertical(src, tc.h, tc.f)
		}
		if !compareNRGBA(got, want, 0) {
			t.Fatalf("single-axis %dx%d mismatch", tc.w, tc.h)
		}
	}
}

// TestNearestNeighborUnaffected ensures the nearest-neighbor fast path keeps
// working under a tight budget.
func TestNearestNeighborUnaffected(t *testing.T) {
	src := makeRandomNRGBA(image.Rect(0, 0, 20, 20), 5)
	got, err := ResizeWithBuffer(src, 7, 11, NearestNeighbor, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := Resize(src, 7, 11, NearestNeighbor)
	if !compareNRGBA(got, want, 0) {
		t.Fatal("nearest-neighbor mismatch")
	}
}

// --- Large-image memory / timing report -------------------------------------

// largeReportCase describes one large-image comparison scenario. FilterName
// (not the filter itself) is stored so the spec is usable in child processes
// without marshaling a func-containing struct.
type largeReportCase struct {
	name       string
	srcW, srcH int
	dstW, dstH int
	filterName string
	budgets    []int64
}

// largeReportCases is defined at package scope so re-executed test binaries
// (child processes) see the exact same scenario list.
var largeReportCases = []largeReportCase{
	// Exporting a huge map preview: the output (1920x1080, ~7.9 MiB) fits
	// comfortably, but the legacy horizontal intermediate is
	// 1920*20000*4 = ~146 MiB and, for the taller variant, far more. This is
	// the exact failure mode where a modest output coexists with a
	// process-topping temporary image.
	{
		name: "map_preview_12000x20000->1920x1080",
		srcW: 12000, srcH: 20000, dstW: 1920, dstH: 1080,
		filterName: "Lanczos",
		budgets: []int64{
			0,         // unlimited: legacy-style single band (~146 MiB)
			128 << 20, // 128 MiB
			32 << 20,  // 32 MiB
			8 << 20,   // 8 MiB
			2 << 20,   // 2 MiB
			1 << 20,   // 1 MiB
		},
	},
	// Squared-ish photographic downscale for a timing reference.
	{
		name: "photo_4000x3000->1200x900",
		srcW: 4000, srcH: 3000, dstW: 1200, dstH: 900,
		filterName: "CatmullRom",
		budgets:    []int64{0, 32 << 20, 8 << 20, 1 << 20, 256 << 10},
	},
}

func filterByName(name string) ResampleFilter {
	for _, nf := range allResampleFilters() {
		if nf.name == name {
			return nf.f
		}
	}
	panic("unknown filter " + name)
}

// largeReportResult is one JSON line emitted by the child process.
type largeReportResult struct {
	Case         string  `json:"case"`
	Mode         string  `json:"mode"`
	Budget       int64   `json:"budget"`
	ScratchBytes int64   `json:"intermediate_scratch_bytes"`
	Bands        int     `json:"bands"`
	PeakRSSMiB   float64 `json:"peak_rss_mib"`
	HeapPeakB    uint64  `json:"heap_peak_bytes"`
	TotalAlloc   uint64  `json:"total_alloc_bytes"`
	Mallocs      uint64  `json:"mallocs"`
	ElapsedMS    int64   `json:"elapsed_ms"`
	SHA256       string  `json:"sha256"`
	Error        string  `json:"error,omitempty"`
}

const largeReportEnv = "IMAGING_LARGE_REPORT"

// TestLargeImageReport re-executes the test binary in child processes so each
// implementation gets a clean address space for peak-RSS accounting. It is
// opt-in via IMAGING_LARGE_TEST=1 because it allocates multi-hundred-MiB
// images. The report is written to artifacts/resize_memory_report.json.
func TestLargeImageReport(t *testing.T) {
	if os.Getenv("IMAGING_LARGE_TEST") == "" {
		t.Skip("set IMAGING_LARGE_TEST=1 to run the large-image memory report")
	}
	if mode := os.Getenv(largeReportEnv); mode != "" {
		runLargeReportChild(t, mode)
		return
	}

	var all []largeReportResult
	for _, tc := range largeReportCases {
		// Legacy reference run.
		all = append(all, runLargeReportChildProcess(t, tc, "legacy", 0))
		for _, b := range tc.budgets {
			all = append(all, runLargeReportChildProcess(t, tc, "banded", b))
		}
	}

	// Verify every run of the same scenario produced identical pixels.
	refs := map[string]string{}
	for _, r := range all {
		if r.Error != "" {
			t.Fatalf("case %s mode %s: %s", r.Case, r.Mode, r.Error)
		}
		if ref, ok := refs[r.Case]; !ok {
			refs[r.Case] = r.SHA256
		} else if r.SHA256 != ref {
			t.Fatalf("checksum mismatch for %s: %+v", r.Case, r)
		}
	}

	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("artifacts", "resize_memory_report.json")
	if err := os.MkdirAll("artifacts", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report written to %s", path)
}

func runLargeReportChildProcess(t *testing.T, tc largeReportCase, mode string, budget int64) largeReportResult {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run", "^TestLargeImageReport$", "-test.count=1"}
	if testing.Verbose() {
		args = append(args, "-test.v")
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(),
		"IMAGING_LARGE_TEST=1",
		largeReportEnv+"="+mode,
		largeReportEnv+"_CASE="+tc.name,
		largeReportEnv+"_BUDGET="+fmt.Sprint(budget),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("child %s/%s failed: %v\n%s\n%s", tc.name, mode, err, stdout.String(), stderr.String())
	}
	var res largeReportResult
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.HasPrefix(line, "LARGE_RESULT ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "LARGE_RESULT ")), &res); err != nil {
				t.Fatal(err)
			}
		}
	}
	if res.Case == "" {
		t.Fatalf("no result from child %s/%s:\n%s", tc.name, mode, stdout.String())
	}
	return res
}

func runLargeReportChild(t *testing.T, mode string) {
	caseName := os.Getenv(largeReportEnv + "_CASE")
	var budget int64
	fmt.Sscan(os.Getenv(largeReportEnv+"_BUDGET"), &budget)

	var tc largeReportCase
	for _, c := range largeReportCases {
		if c.name == caseName {
			tc = c
		}
	}
	if tc.name == "" {
		t.Fatalf("unknown case %q", caseName)
	}
	filter := filterByName(tc.filterName)

	// Deterministic image: generated the same way in every child.
	src := makeRandomNRGBA(image.Rect(0, 0, tc.srcW, tc.srcH), 0xCAFEBABE)
	runtime.GC()

	var msBefore, msAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&msBefore)
	start := time.Now()

	var out *image.NRGBA
	var err error
	res := largeReportResult{Case: tc.name, Mode: mode, Budget: budget}
	switch mode {
	case "legacy":
		// The legacy composition always materializes the full horizontal
		// intermediate: dstW * srcH * 4 bytes.
		res.ScratchBytes = int64(tc.dstW) * int64(tc.srcH) * 4
		res.Bands = 1
		out = legacyResizeTwoPass(src, tc.dstW, tc.dstH, filter)
	case "banded":
		// Compute the same plan the resize will use, for the report columns.
		vw, perr := precomputeWeights(tc.dstH, tc.srcH, filter)
		if perr != nil {
			t.Fatal(perr)
		}
		bands, scratchRows, perr := planResizeBands(vw, int64(tc.dstW)*4, budget)
		if perr != nil {
			t.Fatal(perr)
		}
		res.Bands = len(bands)
		res.ScratchBytes = int64(scratchRows) * int64(tc.dstW) * 4
		out, err = ResizeWithBuffer(src, tc.dstW, tc.dstH, filter, budget)
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&msAfter)

	res.ElapsedMS = elapsed.Milliseconds()
	res.PeakRSSMiB = float64(peakRSSBytes()) / 1024 / 1024
	res.HeapPeakB = msAfter.HeapAlloc
	res.TotalAlloc = msAfter.TotalAlloc - msBefore.TotalAlloc
	res.Mallocs = msAfter.Mallocs - msBefore.Mallocs
	if err != nil {
		res.Error = err.Error()
	} else {
		sum := sha256.Sum256(out.Pix)
		res.SHA256 = fmt.Sprintf("%x", sum)
	}
	b, _ := json.Marshal(res)
	fmt.Printf("LARGE_RESULT %s\n", b)
}

// peakRSSBytes reads the resident-set high-water mark from /proc on Linux.
func peakRSSBytes() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				var kb int64
				fmt.Sscan(fields[1], &kb)
				return kb * 1024
			}
		}
	}
	return 0
}
