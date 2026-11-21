package imaging

import (
	"errors"
	"image"
	"math"
	"sort"
)

// Sharpness analysis errors. They are returned by Sharpness so that invalid
// configurations can be distinguished from legitimate zero-valued results.
var (
	// ErrEmptyImage is returned when the input image has empty bounds.
	ErrEmptyImage = errors.New("imaging: sharpness analysis requires a non-empty image")

	// ErrZeroWindow is returned when the analysis window size is not positive.
	ErrZeroWindow = errors.New("imaging: sharpness window size must be greater than zero")

	// ErrZeroStep is returned when the window step is not positive.
	ErrZeroStep = errors.New("imaging: sharpness window step must be greater than zero")

	// ErrNonFiniteThreshold is returned when NoiseThreshold or NoiseSigma is
	// NaN or infinite.
	ErrNonFiniteThreshold = errors.New("imaging: sharpness threshold and noise parameters must be finite")

	// ErrNegativeThreshold is returned when NoiseThreshold is negative.
	ErrNegativeThreshold = errors.New("imaging: sharpness noise threshold must not be negative")

	// ErrNegativeNoiseSigma is returned when NoiseSigma is negative.
	ErrNegativeNoiseSigma = errors.New("imaging: sharpness noise sigma must not be negative")

	// ErrImageTooLarge is returned when the image or the analysis work set
	// exceeds the memory limit configured via SharpnessOptions.MaxPixels.
	ErrImageTooLarge = errors.New("imaging: image too large for sharpness analysis, raise SharpnessOptions.MaxPixels")
)

// Default parameters for Sharpness.
const (
	defaultSharpnessWindow = 32
	defaultSharpnessStep   = 32

	// defaultSharpnessThreshold is the default gradient floor in luma units
	// (0..255). Gradient magnitudes below it are treated as sensor noise.
	defaultSharpnessThreshold = 4.0

	// plainModeMinFloor is the smallest suppression floor applied when robust
	// estimation is disabled. It keeps quantization noise on solid regions
	// from registering as sharpness even if NoiseThreshold is set to zero.
	plainModeMinFloor = 2.0

	// sharpStepReference is the Sobel magnitude of an ideal 0<->255 luma step
	// (|gx| = |gy| = 4*255 = 1020 at the step center, magnitude ~1442; the
	// mean magnitude over the three affected columns is about 901). Window
	// scores are normalized by this value so a hard focused edge approaches 1.
	sharpStepReference = 901.0

	// defaultMaxSharpnessPixels bounds the working set (the source image plus
	// the per-pixel analysis buffers) to roughly 200 megapixels.
	defaultMaxSharpnessPixels = 200000000
)

// SharpnessOptions configures the local sharpness analysis performed by
// Sharpness.
type SharpnessOptions struct {
	// WindowSize is the side length in pixels of the square analysis window.
	// It must be greater than zero. Default: 32.
	WindowSize int

	// Step is the distance in pixels between consecutive window origins.
	// It must be greater than zero. Default: WindowSize (non-overlapping grid).
	Step int

	// NoiseThreshold is the gradient magnitude floor in luma units (0..255).
	// Per-pixel edge strength below this floor is suppressed before window
	// aggregation, which keeps flat and solid-color regions at score zero even
	// when the image carries mild sensor noise. It must be finite and
	// non-negative. Default: 4.
	NoiseThreshold float64

	// Robust switches to noise-robust estimation. When enabled, the noise
	// level of every window is estimated from its high-frequency residual
	// (median absolute deviation of the Laplacian), the per-window suppression
	// floor adapts to that estimate, and the window score aggregates surviving
	// edge strengths with a median instead of a mean. Default: false.
	Robust bool

	// NoiseSigma optionally pins the noise standard deviation in luma units.
	// When zero or positive and Robust is enabled, window-level MAD estimates
	// are replaced by this value. It must be finite and non-negative. Default: 0
	// (estimate per window).
	NoiseSigma float64

	// MaxPixels bounds the number of pixels the analysis is allowed to buffer.
	// The function fails with ErrImageTooLarge instead of allocating beyond
	// this limit. Zero selects defaultMaxSharpnessPixels.
	MaxPixels int
}

// SharpnessRegion describes the sharpness of one analysis window.
type SharpnessRegion struct {
	// Rect is the window rectangle expressed in the original image
	// coordinates, including any non-zero Bounds().Min offset of the source.
	// Windows clipped at the image border keep the intersected rectangle.
	Rect image.Rectangle

	// Score is the normalized local sharpness in the range [0, 1].
	// Solid-color and transparent windows score zero; defocused texture scores
	// below the same texture in focus.
	Score float64

	// Coverage is the fraction [0, 1] of opaque pixels (alpha > 0) inside the
	// window. Fully transparent windows report zero coverage and zero score.
	Coverage float64
}

// SharpnessMap is the result of a Sharpness analysis.
type SharpnessMap struct {
	// Bounds equals the source image Bounds() and therefore carries the
	// original coordinate system, including any non-zero Min.
	Bounds image.Rectangle

	// WindowSize and Step record the effective analysis parameters.
	WindowSize int
	Step       int

	// Regions is the row-major grid of analysis windows ordered top to bottom,
	// left to right. Every Rect uses the original image coordinates.
	Regions []SharpnessRegion

	// Heatmap is a full-resolution color visualization of the window scores.
	// Each pixel takes the color of the window it belongs to; the color ramp
	// runs from blue (score 0) through green to red (score 1). Its Bounds
	// match the source image Bounds.
	Heatmap *image.NRGBA

	// GrayHeatmap is the same visualization as a 16-bit grayscale image, where
	// 0 means no sharpness and 65535 the maximum score. Its Bounds match the
	// source image Bounds.
	GrayHeatmap *image.Gray16
}

// Sharpness performs local sharpness analysis of img and returns a windowed
// sharpness map in the original image coordinates.
//
// Edge strength is measured from the per-pixel Sobel gradient magnitude of the
// luma channel; in robust mode a Laplacian high-frequency residual provides a
// per-window noise estimate (MAD). A configurable threshold suppresses
// sub-noise magnitudes so that solid-color areas are not confused with
// defocused texture, and transparent pixels (alpha == 0) never contribute.
// Windows that do not fit a full window at the image border are intersected
// with the image bounds instead of being dropped.
//
// A nil *SharpnessOptions selects the default parameters.
func Sharpness(img image.Image, opts *SharpnessOptions) (*SharpnessMap, error) {
	if img == nil || img.Bounds().Empty() {
		return nil, ErrEmptyImage
	}
	// Validate the caller-supplied values first; otherwise an explicit zero
	// would be masked by the default applied below. A nil options set skips
	// validation entirely and uses the defaults.
	if opts != nil {
		if err := opts.validate(); err != nil {
			return nil, err
		}
	}
	o := sharpnessDefaults(opts)

	srcBounds := img.Bounds()
	w, h := srcBounds.Dx(), srcBounds.Dy()

	// Memory guard: source copy plus per-pixel luma and gradient buffers.
	if uint64(w)*uint64(h) > uint64(o.MaxPixels) {
		return nil, ErrImageTooLarge
	}
	// Normalize the source into a tight NRGBA image while remembering the
	// original coordinate offset so that all reported rectangles stay in the
	// original coordinate system.
	offset := srcBounds.Min
	src := Clone(img)
	stride := src.Stride

	// Per-pixel luma and Sobel gradient magnitude, laid out over the tight
	// (w*h) grid. Border pixels use edge-clamped neighbors.
	luma := make([]float64, w*h)
	grad := make([]float64, w*h)

	parallel(0, h, func(ys <-chan int) {
		for y := range ys {
			row := y * stride
			for x := 0; x < w; x++ {
				off := row + x*4
				if src.Pix[off+3] == 0 {
					continue
				}
				luma[y*w+x] = luma64(src.Pix[off], src.Pix[off+1], src.Pix[off+2])
			}
		}
	})

	parallel(0, h, func(ys <-chan int) {
		for y := range ys {
			for x := 0; x < w; x++ {
				off := y*stride + x*4
				if src.Pix[off+3] == 0 {
					continue
				}
				grad[y*w+x] = sobelMagnitude(src, luma, w, stride, x, y)
			}
		}
	})

	// Laplacian residuals are only needed for robust noise estimation.
	var lapl []float64
	if o.Robust {
		lapl = make([]float64, w*h)
		parallel(0, h, func(ys <-chan int) {
			for y := range ys {
				for x := 0; x < w; x++ {
					off := y*stride + x*4
					if src.Pix[off+3] == 0 {
						continue
					}
					lapl[y*w+x] = laplacian(src, luma, w, stride, x, y)
				}
			}
		})
	}

	// Build the window grid. Border windows are intersected with the bounds so
	// that partial windows remain stable and never read out of range.
	cols := 1 + (w-1)/o.Step
	rows := 1 + (h-1)/o.Step
	if uint64(cols) > uint64(o.MaxPixels) ||
		uint64(rows) > uint64(o.MaxPixels) ||
		uint64(cols)*uint64(rows) > uint64(o.MaxPixels) {
		return nil, ErrImageTooLarge
	}
	regions := make([]SharpnessRegion, 0, cols*rows)

	for gy := 0; gy < rows; gy++ {
		y0 := gy * o.Step
		y1 := y0 + o.WindowSize
		if y1 > h {
			y1 = h
		}
		for gx := 0; gx < cols; gx++ {
			x0 := gx * o.Step
			x1 := x0 + o.WindowSize
			if x1 > w {
				x1 = w
			}

			region := scoreWindow(src, luma, grad, lapl, w, stride, x0, y0, x1, y1, o)
			region.Rect = image.Rect(x0, y0, x1, y1).Add(offset)
			regions = append(regions, region)
		}
	}

	heat := image.NewNRGBA(image.Rect(0, 0, w, h))
	gray := image.NewGray16(image.Rect(0, 0, w, h))
	renderSharpnessHeatmaps(regions, cols, rows, o.Step, heat, gray)

	heat.Rect = heat.Rect.Add(offset)
	gray.Rect = gray.Rect.Add(offset)

	return &SharpnessMap{
		Bounds:      srcBounds,
		WindowSize:  o.WindowSize,
		Step:        o.Step,
		Regions:     regions,
		Heatmap:     heat,
		GrayHeatmap: gray,
	}, nil
}

func sharpnessDefaults(opts *SharpnessOptions) SharpnessOptions {
	var o SharpnessOptions
	if opts != nil {
		o = *opts
	}
	if o.WindowSize == 0 {
		o.WindowSize = defaultSharpnessWindow
	}
	if o.Step == 0 {
		o.Step = o.WindowSize
	}
	if opts == nil {
		// An explicitly zero NoiseThreshold means "no suppression" and is
		// preserved when the caller supplies options.
		o.NoiseThreshold = defaultSharpnessThreshold
	}
	if o.MaxPixels == 0 {
		o.MaxPixels = defaultMaxSharpnessPixels
	}
	return o
}

func (o SharpnessOptions) validate() error {
	if o.WindowSize <= 0 {
		return ErrZeroWindow
	}
	if o.Step <= 0 {
		return ErrZeroStep
	}
	if math.IsNaN(o.NoiseThreshold) || math.IsInf(o.NoiseThreshold, 0) ||
		math.IsNaN(o.NoiseSigma) || math.IsInf(o.NoiseSigma, 0) {
		return ErrNonFiniteThreshold
	}
	if o.NoiseThreshold < 0 {
		return ErrNegativeThreshold
	}
	if o.NoiseSigma < 0 {
		return ErrNegativeNoiseSigma
	}
	if o.MaxPixels < 0 {
		return ErrImageTooLarge
	}
	return nil
}

func luma64(r, g, b uint8) float64 {
	return 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
}

func clampIndex(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// neighborLuma returns the luma at (x, y), substituting the center luma when
// the pixel is transparent or outside the image. This keeps the alpha
// boundary from manufacturing artificial gradients.
func neighborLuma(src *image.NRGBA, luma []float64, w, stride, x, y int, center float64) float64 {
	if src.Pix[y*stride+x*4+3] == 0 {
		return center
	}
	return luma[y*w+x]
}

// sobelMagnitude returns the Sobel gradient magnitude of the luma field at
// (x, y). Neighbors outside the image are edge-clamped; transparent neighbors
// contribute the center luma so the alpha edge is invisible.
func sobelMagnitude(src *image.NRGBA, luma []float64, w, stride, x, y int) float64 {
	h := len(luma) / w
	xm := clampIndex(x-1, w)
	xp := clampIndex(x+1, w)
	ym := clampIndex(y-1, h)
	yp := clampIndex(y+1, h)

	center := luma[y*w+x]
	at := func(xx, yy int) float64 {
		return neighborLuma(src, luma, w, stride, xx, yy, center)
	}

	gx := -at(xm, ym) - 2*at(xm, y) - at(xm, yp) +
		at(xp, ym) + 2*at(xp, y) + at(xp, yp)
	gy := -at(xm, ym) - 2*at(x, ym) - at(xp, ym) +
		at(xm, yp) + 2*at(x, yp) + at(xp, yp)
	return math.Hypot(gx, gy)
}

// laplacian returns the 4-neighbor discrete Laplacian of the luma field at
// (x, y). Neighbors outside the image are edge-clamped; transparent neighbors
// contribute the center luma so the alpha edge is invisible.
func laplacian(src *image.NRGBA, luma []float64, w, stride, x, y int) float64 {
	h := len(luma) / w
	xm := clampIndex(x-1, w)
	xp := clampIndex(x+1, w)
	ym := clampIndex(y-1, h)
	yp := clampIndex(y+1, h)

	center := luma[y*w+x]
	return neighborLuma(src, luma, w, stride, xm, y, center) +
		neighborLuma(src, luma, w, stride, xp, y, center) +
		neighborLuma(src, luma, w, stride, x, ym, center) +
		neighborLuma(src, luma, w, stride, x, yp, center) - 4*center
}

// scoreWindow aggregates the per-pixel edge strengths of one window into a
// normalized sharpness score.
func scoreWindow(
	src *image.NRGBA,
	luma, grad, lapl []float64,
	w, stride, x0, y0, x1, y1 int,
	o SharpnessOptions,
) SharpnessRegion {
	area := (x1 - x0) * (y1 - y0)
	region := SharpnessRegion{
		Rect: image.Rect(x0, y0, x1, y1),
	}

	opaque := 0
	values := make([]float64, 0, area)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if src.Pix[y*stride+x*4+3] == 0 {
				continue
			}
			opaque++
			// Only the window interior is aggregated: the one-pixel border
			// relies on edge-clamped neighbors (image border, alpha boundary
			// or an adjacent window of different content) and would otherwise
			// manufacture artificial gradient energy inside flat regions.
			if x == x0 || y == y0 || x == x1-1 || y == y1-1 {
				continue
			}
			values = append(values, grad[y*w+x])
		}
	}
	if opaque == 0 {
		return region
	}
	region.Coverage = float64(opaque) / float64(area)
	if len(values) == 0 {
		// Window smaller than 3x3 has no interior; nothing reliable can be
		// measured, so report zero rather than a border artifact.
		return region
	}

	floor := o.NoiseThreshold
	if o.Robust {
		sigma := o.NoiseSigma
		if sigma == 0 {
			sigma = windowNoiseSigma(src, lapl, w, stride, x0, y0, x1, y1)
		}
		// A fixed baseline floor handles mild quantization noise; the
		// per-window MAD estimate raises it on genuinely noisy images.
		if adapted := 2.5 * sigma; adapted > floor {
			floor = adapted
		}
	} else {
		// Even when NoiseThreshold is disabled, plain mode keeps a small fixed
		// floor so that quantization and mild sensor noise on solid areas do
		// not read as sharpness. Robust mode is the tunable, adaptive path.
		if floor < plainModeMinFloor {
			floor = plainModeMinFloor
		}
	}

	// Soft suppression: sub-floor magnitudes vanish, the rest keep their
	// excess. This removes the noise contribution without creating a hard
	// discontinuity at the threshold.
	sum := 0.0
	for i, v := range values {
		if v <= floor {
			values[i] = 0
		} else {
			values[i] = v - floor
			sum += values[i]
		}
	}

	var score float64
	if o.Robust {
		// Median of the suppressed magnitudes measures the typical edge
		// strength rather than being driven by a few noisy pixels. Subtracting
		// the floor again makes windows whose median energy is at or below the
		// noise level — including pure-noise solid areas — score exactly zero.
		score = medianFloat64(values)
		if score > 0 {
			score -= floor
			if score < 0 {
				score = 0
			}
		}
	} else {
		score = sum / float64(len(values))
	}

	// Normalize by the Sobel magnitude of an ideal 0<->255 luma step, about
	// 901. A perfectly focused hard edge therefore saturates near 1.0 while
	// defocused gradients stay well below.
	score /= sharpStepReference
	if score > 1 {
		score = 1
	}
	region.Score = score
	return region
}

// windowNoiseSigma estimates the luma noise standard deviation inside a window
// from the median absolute deviation of the Laplacian residual. A Laplacian of
// i.i.d. noise with stddev s has stddev s*sqrt(20), and MAD of a zero-mean
// normal variable is 0.6745*stddev.
func windowNoiseSigma(
	src *image.NRGBA,
	lapl []float64,
	w, stride, x0, y0, x1, y1 int,
) float64 {
	residuals := make([]float64, 0, (x1-x0)*(y1-y0))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if src.Pix[y*stride+x*4+3] == 0 {
				continue
			}
			// Match the score aggregation: the one-pixel border depends on
			// edge-clamped neighbors and must not bias the noise estimate.
			if x == x0 || y == y0 || x == x1-1 || y == y1-1 {
				continue
			}
			residuals = append(residuals, lapl[y*w+x])
		}
	}
	if len(residuals) == 0 {
		return 0
	}
	mad := medianAbsoluteDeviation(residuals)
	return mad * 1.4826 / math.Sqrt(20)
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

func medianAbsoluteDeviation(values []float64) float64 {
	center := medianFloat64(values)
	dev := make([]float64, len(values))
	for i, v := range values {
		dev[i] = math.Abs(v - center)
	}
	return medianFloat64(dev)
}

// renderSharpnessHeatmaps paints every pixel with the score of the window it
// belongs to. The color ramp is blue (0) -> green (0.5) -> red (1). When the
// step is smaller than the window, overlapping regions are resolved in favor
// of the top-left-most window (floor division by step) so each pixel is
// painted exactly once and the whole source bounds are covered without gaps.
func renderSharpnessHeatmaps(
	regions []SharpnessRegion,
	cols, rows, step int,
	heat *image.NRGBA,
	gray *image.Gray16,
) {
	w := heat.Bounds().Dx()
	h := heat.Bounds().Dy()
	for y := 0; y < h; y++ {
		gy := y / step
		if gy >= rows {
			gy = rows - 1
		}
		for x := 0; x < w; x++ {
			gx := x / step
			if gx >= cols {
				gx = cols - 1
			}
			region := regions[gy*cols+gx]
			r32, g32, b32 := sharpnessColor(region.Score)
			grayVal := uint16(region.Score*65535 + 0.5)

			off := y*heat.Stride + x*4
			heat.Pix[off] = r32
			heat.Pix[off+1] = g32
			heat.Pix[off+2] = b32
			heat.Pix[off+3] = 0xff

			goff := y*gray.Stride + x*2
			gray.Pix[goff] = uint8(grayVal >> 8)
			gray.Pix[goff+1] = uint8(grayVal)
		}
	}
}

func sharpnessColor(score float64) (r, g, b uint8) {
	if score < 0 {
		score = 0
	} else if score > 1 {
		score = 1
	}
	// Piecewise linear ramp blue -> green -> red over the unit interval.
	var rf, gf, bf float64
	switch {
	case score < 0.5:
		t := score * 2
		rf = 0
		gf = t
		bf = 1 - t
	default:
		t := (score - 0.5) * 2
		rf = t
		gf = 1 - t
		bf = 0
	}
	return clamp(rf * 255), clamp(gf * 255), clamp(bf * 255)
}
