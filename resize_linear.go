package imaging

import (
	"image"
	"math"
)

// ResizeOption sets an optional parameter for the Resize, Fit, Fill and
// Thumbnail functions.
type ResizeOption func(*resizeConfig)

type resizeConfig struct {
	linearLight bool
}

// LinearLight returns a ResizeOption that controls whether the image is
// resampled in linear RGB (linear light) instead of the sRGB-encoded values
// stored in the image.
//
// sRGB values are not proportional to the physical light they represent, so
// averaging them directly (the default behavior, kept for compatibility)
// darkens high-contrast details. A typical symptom is a black-and-white UI
// texture that looks too dark after it is scaled down: an equal mix of black
// and white averages to sRGB 128 instead of the physically correct 188.
// With LinearLight(true) the sRGB channels are converted to linear light
// before filtering and encoded back to sRGB afterwards.
//
// Filtering always uses premultiplied alpha, so fully transparent pixels
// cannot bleed their color into visible pixels and transparent edges do not
// get dark fringes. Alpha itself is always averaged linearly.
//
// Linear light resampling has no effect on the NearestNeighbor filter (it
// does not mix pixels) or when the source and destination sizes are equal.
// The default (LinearLight(false)) leaves the results of Resize, Fit, Fill
// and Thumbnail unchanged.
//
// Example:
//
//	dstImage := imaging.Resize(srcImage, 800, 600, imaging.Lanczos, imaging.LinearLight(true))
func LinearLight(enabled bool) ResizeOption {
	return func(c *resizeConfig) {
		c.linearLight = enabled
	}
}

func newResizeConfig(opts []ResizeOption) resizeConfig {
	var cfg resizeConfig
	for _, option := range opts {
		if option != nil {
			option(&cfg)
		}
	}
	return cfg
}

// ResizeLinear resizes the image like Resize, but performs the resampling in
// linear light. It is equivalent to calling Resize with the LinearLight(true)
// option.
//
// Example:
//
//	dstImage := imaging.ResizeLinear(srcImage, 800, 600, imaging.Lanczos)
func ResizeLinear(img image.Image, width, height int, filter ResampleFilter) *image.NRGBA {
	return Resize(img, width, height, filter, LinearLight(true))
}

// FitLinear scales the image down like Fit, but performs the resampling in
// linear light. It is equivalent to calling Fit with the LinearLight(true)
// option.
func FitLinear(img image.Image, width, height int, filter ResampleFilter) *image.NRGBA {
	return Fit(img, width, height, filter, LinearLight(true))
}

// FillLinear scales and crops the image like Fill, but performs the
// resampling in linear light. It is equivalent to calling Fill with the
// LinearLight(true) option.
func FillLinear(img image.Image, width, height int, anchor Anchor, filter ResampleFilter) *image.NRGBA {
	return Fill(img, width, height, anchor, filter, LinearLight(true))
}

// ThumbnailLinear scales and crops the image like Thumbnail, but performs the
// resampling in linear light. It is equivalent to calling Thumbnail with the
// LinearLight(true) option.
func ThumbnailLinear(img image.Image, width, height int, filter ResampleFilter) *image.NRGBA {
	return Thumbnail(img, width, height, filter, LinearLight(true))
}

// srgbToLinearLUT maps each 8-bit sRGB encoded value to linear light in the
// [0, 1] range.
var srgbToLinearLUT = func() (lut [256]float64) {
	for i := range lut {
		lut[i] = srgbToLinear(float64(i) / 255.0)
	}
	return lut
}()

// srgbToLinear converts one normalized sRGB encoded channel value to linear
// light.
func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// linearToSRGB converts one linear light channel value to the normalized sRGB
// encoding. Values outside [0, 1] are clamped, which also makes the function
// safe for the slight over- and undershoot produced by filters with negative
// lobes.
func linearToSRGB(c float64) float64 {
	if !(c > 0) { // also catches NaN
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

// DefaultResizeLinearMaxPixels is the maximum number of pixels allowed in a
// linear-light resize result or in its intermediate buffer. Resampling in
// linear light needs four float64 values (32 bytes) per intermediate pixel in
// addition to the 8-bit output, so an unchecked extreme scale ratio could
// allocate many gigabytes for a tiny source image. Requests above this limit
// return an empty image instead of allocating.
//
// At four bytes per output pixel this is about 256 MiB; the intermediate
// buffer of a two-pass resize is at most 32 bytes per pixel, i.e. about
// 2 GiB in the worst case. A full-size 8K (7680x4320) image is well below the
// limit.
const DefaultResizeLinearMaxPixels int64 = 1 << 26

// linearResizeFits reports whether a width x height intermediate or output
// image stays within DefaultResizeLinearMaxPixels.
func linearResizeFits(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	return int64(width) <= DefaultResizeLinearMaxPixels/int64(height)
}

// DefaultResizeLinearMaxWeightBytes caps the resampling weight table that a
// single axis of a linear-light resize may allocate. precomputeWeights
// reserves capacity for (dstSize * (ceil(scale*Support) + 2) * 2) entries of
// 16 bytes each up front, so an extreme upscale of a tiny image (for example
// 1x1 to millions of pixels wide) can allocate gigabytes of weights even
// though the resulting image itself is small. Requests above this limit
// return an empty image instead of allocating.
const DefaultResizeLinearMaxWeightBytes int64 = 1 << 28

// linearAxisWeightsFit reports whether the resampling weight table needed to
// scale one axis from srcSize to dstSize stays within
// DefaultResizeLinearMaxWeightBytes. The capacity formula mirrors
// precomputeWeights.
func linearAxisWeightsFit(dstSize, srcSize int, filter ResampleFilter) bool {
	if dstSize <= 0 || srcSize <= 0 {
		return false
	}
	du := float64(srcSize) / float64(dstSize)
	scale := du
	if scale < 1.0 {
		scale = 1.0
	}
	ru := math.Ceil(scale * filter.Support)
	// Capacity reserved by precomputeWeights, per destination pixel.
	perPixel := (int(ru) + 2) * 2
	if perPixel <= 0 {
		return false
	}
	const bytesPerEntry = 16 // indexWeight: int + float64 on 64-bit platforms
	budget := DefaultResizeLinearMaxWeightBytes / bytesPerEntry
	return int64(dstSize) <= budget/int64(perPixel)
}

// maxIntValue is the largest value representable by an int on this platform.
const maxIntValue = int(^uint(0) >> 1)

// mulFits reports whether a*b can be represented as an int.
func mulFits(a, b int) bool {
	if a < 0 || b < 0 {
		return false
	}
	if a == 0 || b == 0 {
		return true
	}
	return a <= maxIntValue/b
}

// validResizeSize reports whether a width x height NRGBA image can be
// allocated without overflowing the platform integer type.
func validResizeSize(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if !mulFits(width, height) {
		return false
	}
	return mulFits(width*height, 4)
}

// validLinearBufferSize reports whether an intermediate linear light buffer of
// width x height pixels (4 float64 components per pixel) can be allocated
// without overflowing the platform integer type.
func validLinearBufferSize(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if !mulFits(width, height) {
		return false
	}
	return mulFits(width*height, 32)
}

// resizeLinear resizes img to dstW x dstH using the given filter and performs
// all color filtering in linear light with premultiplied alpha.
//
// The two passes of a separable filter are done on floating point linear
// light values, so an intermediate image is never rounded back to 8 bit sRGB
// between the passes. At most one intermediate buffer is allocated, and the
// axis order is chosen so that it has the smallest possible size. When only
// one dimension changes no intermediate buffer is needed at all.
func resizeLinear(img image.Image, dstW, dstH int, filter ResampleFilter) *image.NRGBA {
	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()

	if srcW <= 0 || srcH <= 0 {
		return &image.NRGBA{}
	}
	if !validResizeSize(dstW, dstH) || !linearResizeFits(dstW, dstH) {
		return &image.NRGBA{}
	}
	if srcW != dstW && !linearAxisWeightsFit(dstW, srcW, filter) {
		return &image.NRGBA{}
	}
	if srcH != dstH && !linearAxisWeightsFit(dstH, srcH, filter) {
		return &image.NRGBA{}
	}
	if srcW == dstW && srcH == dstH {
		return Clone(img)
	}
	if filter.Support <= 0 {
		// Nearest-neighbor sampling does not mix pixels, so there is
		// nothing to linearize.
		return resizeNearest(img, dstW, dstH)
	}

	if srcW != dstW && srcH != dstH {
		// Both dimensions change: pick the pass order with the smaller
		// intermediate buffer. The products are compared as float64 to
		// avoid overflowing int; the choice only affects memory use and
		// floating point rounding.
		if float64(dstW)*float64(srcH) <= float64(srcW)*float64(dstH) {
			if !validLinearBufferSize(dstW, srcH) || !linearResizeFits(dstW, srcH) {
				return &image.NRGBA{}
			}
			buf := linearHorizontalToBuffer(img, dstW, filter)
			return linearVerticalFromBuffer(buf, dstW, srcH, dstH, filter)
		}
		if !validLinearBufferSize(srcW, dstH) || !linearResizeFits(srcW, dstH) {
			return &image.NRGBA{}
		}
		buf := linearVerticalToBuffer(img, dstH, filter)
		return linearHorizontalFromBuffer(buf, srcW, dstH, dstW, filter)
	}

	if srcW != dstW {
		if !linearResizeFits(dstW, srcH) {
			return &image.NRGBA{}
		}
		return linearHorizontalDirect(img, dstW, filter)
	}
	if !linearResizeFits(srcW, dstH) {
		return &image.NRGBA{}
	}
	return linearVerticalDirect(img, dstH, filter)
}

// linearPixel writes one accumulated premultiplied linear light pixel to dst,
// unpremultiplying it, encoding the color back to sRGB and clamping the
// result. A pixel whose accumulated alpha is not positive, or is so small that
// the 8-bit output alpha would round to zero, is left fully transparent
// black. That keeps filters with negative lobes and near-zero alpha
// numerically stable: the color of an invisible pixel cannot overflow into an
// arbitrary value.
func linearPixel(dst []uint8, r, g, b, a float64) {
	if !(a > 0.5/255.0) {
		return
	}
	inv := 1 / a
	dst[0] = clamp(linearToSRGB(r*inv) * 255)
	dst[1] = clamp(linearToSRGB(g*inv) * 255)
	dst[2] = clamp(linearToSRGB(b*inv) * 255)
	dst[3] = clamp(a * 255)
}

// linearHorizontalDirect resamples img horizontally to dstW columns and
// writes the result straight into the output image. No intermediate buffer is
// allocated.
func linearHorizontalDirect(img image.Image, dstW int, filter ResampleFilter) *image.NRGBA {
	src := newScanner(img)
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, src.h))
	weights := precomputeWeights(dstW, src.w, filter)

	parallel(0, src.h, func(ys <-chan int) {
		scanLine := make([]uint8, src.w*4)
		for y := range ys {
			src.scan(0, y, src.w, y+1, scanLine)
			row := dst.Pix[y*dst.Stride : y*dst.Stride+dstW*4]
			for x := range weights {
				var r, g, b, a float64
				for _, w := range weights[x] {
					i := w.index * 4
					aw := float64(scanLine[i+3]) * (1.0 / 255.0) * w.weight
					if aw == 0 {
						continue
					}
					r += srgbToLinearLUT[scanLine[i+0]] * aw
					g += srgbToLinearLUT[scanLine[i+1]] * aw
					b += srgbToLinearLUT[scanLine[i+2]] * aw
					a += aw
				}
				linearPixel(row[x*4:x*4+4:x*4+4], r, g, b, a)
			}
		}
	})
	return dst
}

// linearVerticalDirect resamples img vertically to dstH rows and writes the
// result straight into the output image. No intermediate buffer is allocated
// besides one source column.
func linearVerticalDirect(img image.Image, dstH int, filter ResampleFilter) *image.NRGBA {
	src := newScanner(img)
	dst := image.NewNRGBA(image.Rect(0, 0, src.w, dstH))
	weights := precomputeWeights(dstH, src.h, filter)

	parallel(0, src.w, func(xs <-chan int) {
		scanLine := make([]uint8, src.h*4)
		for x := range xs {
			src.scan(x, 0, x+1, src.h, scanLine)
			for y := range weights {
				var r, g, b, a float64
				for _, w := range weights[y] {
					i := w.index * 4
					aw := float64(scanLine[i+3]) * (1.0 / 255.0) * w.weight
					if aw == 0 {
						continue
					}
					r += srgbToLinearLUT[scanLine[i+0]] * aw
					g += srgbToLinearLUT[scanLine[i+1]] * aw
					b += srgbToLinearLUT[scanLine[i+2]] * aw
					a += aw
				}
				j := y*dst.Stride + x*4
				linearPixel(dst.Pix[j:j+4:j+4], r, g, b, a)
			}
		}
	})
	return dst
}

// linearHorizontalToBuffer resamples img horizontally to dstW columns into a
// linear light premultiplied buffer with srcH rows. The buffer is row-major
// RGBA with alpha normalized to [0, 1].
func linearHorizontalToBuffer(img image.Image, dstW int, filter ResampleFilter) []float64 {
	src := newScanner(img)
	weights := precomputeWeights(dstW, src.w, filter)
	buf := make([]float64, dstW*src.h*4)

	parallel(0, src.h, func(ys <-chan int) {
		scanLine := make([]uint8, src.w*4)
		for y := range ys {
			src.scan(0, y, src.w, y+1, scanLine)
			out := buf[y*dstW*4 : (y+1)*dstW*4]
			for x := range weights {
				var r, g, b, a float64
				for _, w := range weights[x] {
					i := w.index * 4
					aw := float64(scanLine[i+3]) * (1.0 / 255.0) * w.weight
					if aw == 0 {
						continue
					}
					r += srgbToLinearLUT[scanLine[i+0]] * aw
					g += srgbToLinearLUT[scanLine[i+1]] * aw
					b += srgbToLinearLUT[scanLine[i+2]] * aw
					a += aw
				}
				o := x * 4
				out[o+0] = r
				out[o+1] = g
				out[o+2] = b
				out[o+3] = a
			}
		}
	})
	return buf
}

// linearVerticalToBuffer resamples img vertically to dstH rows into a linear
// light premultiplied buffer with srcW columns.
func linearVerticalToBuffer(img image.Image, dstH int, filter ResampleFilter) []float64 {
	src := newScanner(img)
	weights := precomputeWeights(dstH, src.h, filter)
	buf := make([]float64, src.w*dstH*4)

	parallel(0, src.w, func(xs <-chan int) {
		scanLine := make([]uint8, src.h*4)
		for x := range xs {
			src.scan(x, 0, x+1, src.h, scanLine)
			for y := range weights {
				var r, g, b, a float64
				for _, w := range weights[y] {
					i := w.index * 4
					aw := float64(scanLine[i+3]) * (1.0 / 255.0) * w.weight
					if aw == 0 {
						continue
					}
					r += srgbToLinearLUT[scanLine[i+0]] * aw
					g += srgbToLinearLUT[scanLine[i+1]] * aw
					b += srgbToLinearLUT[scanLine[i+2]] * aw
					a += aw
				}
				o := (y*src.w + x) * 4
				buf[o+0] = r
				buf[o+1] = g
				buf[o+2] = b
				buf[o+3] = a
			}
		}
	})
	return buf
}

// linearHorizontalFromBuffer resamples a linear premultiplied buffer of size
// srcW x srcH horizontally to dstW columns and returns the encoded output.
// The values in src are already premultiplied, so only the filter weights are
// applied here.
func linearHorizontalFromBuffer(src []float64, srcW, srcH, dstW int, filter ResampleFilter) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, srcH))
	weights := precomputeWeights(dstW, srcW, filter)

	parallel(0, srcH, func(ys <-chan int) {
		for y := range ys {
			in := src[y*srcW*4 : (y+1)*srcW*4]
			row := dst.Pix[y*dst.Stride : y*dst.Stride+dstW*4]
			for x := range weights {
				var r, g, b, a float64
				for _, w := range weights[x] {
					i := w.index * 4
					r += in[i+0] * w.weight
					g += in[i+1] * w.weight
					b += in[i+2] * w.weight
					a += in[i+3] * w.weight
				}
				linearPixel(row[x*4:x*4+4:x*4+4], r, g, b, a)
			}
		}
	})
	return dst
}

// linearVerticalFromBuffer resamples a linear premultiplied buffer of size
// srcW x srcH vertically to dstH rows and returns the encoded output. The
// values in src are already premultiplied, so only the filter weights are
// applied here.
func linearVerticalFromBuffer(src []float64, srcW, srcH, dstH int, filter ResampleFilter) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, srcW, dstH))
	weights := precomputeWeights(dstH, srcH, filter)

	parallel(0, srcW, func(xs <-chan int) {
		for x := range xs {
			for y := range weights {
				var r, g, b, a float64
				for _, w := range weights[y] {
					i := (w.index*srcW + x) * 4
					r += src[i+0] * w.weight
					g += src[i+1] * w.weight
					b += src[i+2] * w.weight
					a += src[i+3] * w.weight
				}
				j := y*dst.Stride + x*4
				linearPixel(dst.Pix[j:j+4:j+4], r, g, b, a)
			}
		}
	})
	return dst
}
