package imaging

import (
	"image"
	"math"
)

// ResizeCoverage works like Resize but additionally preserves the alpha coverage
// of the source image.
//
// The coverage is the fraction of pixels whose alpha channel is greater than or
// equal to the alphaThreshold value (a value in the range [0, 255]). It is
// measured on the source image, then the alpha channel of the resized image is
// adjusted with a single per-pixel gain so that the resized image has the same
// coverage. This compensates for the coverage loss that happens when sparse
// alpha-cutout textures (for example tree or foliage cutouts) are downscaled and
// the resampled alpha falls below the cutout threshold.
//
// The adjustment only scales the alpha channel. The output dimensions and the
// RGB channel values are exactly the ones produced by Resize, so the color edge
// semantics are unchanged.
//
// If the target coverage cannot be reached exactly, the gain whose resulting
// coverage is closest to the target is used. On an exact tie the gain that
// moves coverage towards the target is preferred, then the gain closest to
// 1.0, so an image that already has the target coverage is never modified.
// If the source has zero coverage at the threshold, the alpha channel is left
// untouched. An alphaThreshold of 0 or outside the uint8 alpha range disables
// the coverage adjustment and the function behaves exactly like Resize.
func ResizeCoverage(img image.Image, width, height int, filter ResampleFilter, alphaThreshold int) *image.NRGBA {
	dst := Resize(img, width, height, filter)
	if alphaThreshold <= 0 || alphaThreshold > 255 {
		// Threshold 0 makes every pixel count as covered, so there is
		// nothing to preserve; any other value is outside the uint8 alpha
		// range. In both cases the regular resize result is returned.
		return dst
	}

	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return dst
	}

	target := alphaCoverage(img, alphaThreshold)
	if target <= 0 {
		// Zero source coverage: there is nothing to preserve, keep the
		// regular resize result.
		return dst
	}

	preserveAlphaCoverage(dst, alphaThreshold, target)
	return dst
}

// FitCoverage works like Fit but preserves the source alpha coverage at the
// given threshold. See ResizeCoverage for the coverage adjustment details and
// the handling of invalid thresholds.
func FitCoverage(img image.Image, width, height int, filter ResampleFilter, alphaThreshold int) *image.NRGBA {
	maxW, maxH := width, height

	if maxW <= 0 || maxH <= 0 {
		return &image.NRGBA{}
	}

	srcBounds := img.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	if srcW <= 0 || srcH <= 0 {
		return &image.NRGBA{}
	}

	if srcW <= maxW && srcH <= maxH {
		return ResizeCoverage(img, srcW, srcH, filter, alphaThreshold)
	}

	srcAspectRatio := float64(srcW) / float64(srcH)
	maxAspectRatio := float64(maxW) / float64(maxH)

	var newW, newH int
	if srcAspectRatio > maxAspectRatio {
		newW = maxW
		newH = int(float64(newW) / srcAspectRatio)
	} else {
		newH = maxH
		newW = int(float64(newH) * srcAspectRatio)
	}

	return ResizeCoverage(img, newW, newH, filter, alphaThreshold)
}

// ThumbnailCoverage works like Thumbnail but preserves the source alpha
// coverage at the given threshold. The coverage is measured on the
// center-cropped region that fills the output dimensions, so it matches the
// image content that ends up in the thumbnail. See ResizeCoverage for the
// coverage adjustment details and the handling of invalid thresholds.
func ThumbnailCoverage(img image.Image, width, height int, filter ResampleFilter, alphaThreshold int) *image.NRGBA {
	dstW, dstH := width, height

	if dstW <= 0 || dstH <= 0 {
		return &image.NRGBA{}
	}

	srcBounds := img.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	if srcW <= 0 || srcH <= 0 {
		return &image.NRGBA{}
	}

	if srcW == dstW && srcH == dstH {
		return ResizeCoverage(img, dstW, dstH, filter, alphaThreshold)
	}

	if srcW >= 100 && srcH >= 100 {
		// Mirror cropAndResize: crop first, then scale with coverage
		// preservation measured on the cropped region.
		srcAspectRatio := float64(srcW) / float64(srcH)
		dstAspectRatio := float64(dstW) / float64(dstH)

		var tmp *image.NRGBA
		if srcAspectRatio < dstAspectRatio {
			cropH := float64(srcW) * float64(dstH) / float64(dstW)
			tmp = CropAnchor(img, srcW, int(math.Max(1, cropH)+0.5), Center)
		} else {
			cropW := float64(srcH) * float64(dstW) / float64(dstH)
			tmp = CropAnchor(img, int(math.Max(1, cropW)+0.5), srcH, Center)
		}

		return ResizeCoverage(tmp, dstW, dstH, filter, alphaThreshold)
	}

	// Mirror resizeAndCrop: scale first, then crop. The coverage is measured
	// on the full source and applied before the crop.
	var tmp *image.NRGBA
	srcAspectRatio := float64(srcW) / float64(srcH)
	dstAspectRatio := float64(dstW) / float64(dstH)
	if srcAspectRatio < dstAspectRatio {
		tmp = ResizeCoverage(img, dstW, 0, filter, alphaThreshold)
	} else {
		tmp = ResizeCoverage(img, 0, dstH, filter, alphaThreshold)
	}

	return CropAnchor(tmp, dstW, dstH, Center)
}

// alphaCoverage returns the fraction of pixels of img whose alpha channel is
// greater than or equal to threshold. It returns 0 for empty images.
func alphaCoverage(img image.Image, threshold int) float64 {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return 0
	}

	var count int
	if src, ok := img.(*image.NRGBA); ok {
		// Fast path without an extra full-image copy or per-row scans.
		base := bounds.Min.Y*src.Stride + bounds.Min.X*4
		for y := 0; y < h; y++ {
			off := base + y*src.Stride
			end := off + w*4
			for i := off + 3; i < end; i += 4 {
				if int(src.Pix[i]) >= threshold {
					count++
				}
			}
		}
	} else {
		src := newScanner(img)
		scanLine := make([]uint8, w*4)
		for y := 0; y < h; y++ {
			src.scan(0, y, w, y+1, scanLine)
			for i := 3; i < len(scanLine); i += 4 {
				if int(scanLine[i]) >= threshold {
					count++
				}
			}
		}
	}

	return float64(count) / float64(w*h)
}

// preserveAlphaCoverage scales the alpha channel of dst in place so that the
// fraction of pixels with alpha >= threshold gets as close as possible to the
// target coverage. The RGB channels are never modified.
func preserveAlphaCoverage(dst *image.NRGBA, threshold int, target float64) {
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	if w <= 0 || h <= 0 || target <= 0 || threshold <= 0 {
		return
	}

	// Histogram and suffix counts of the resized alpha values.
	var hist [256]int
	for y := 0; y < h; y++ {
		off := y * dst.Stride
		end := off + w*4
		for i := off + 3; i < end; i += 4 {
			hist[dst.Pix[i]]++
		}
	}
	hasNonZeroAlpha := false
	for a := 1; a < 256; a++ {
		if hist[a] > 0 {
			hasNonZeroAlpha = true
			break
		}
	}
	if !hasNonZeroAlpha {
		// Fully transparent result: nothing can cross a positive threshold.
		return
	}

	// countAt[k] is the number of pixels that survive the threshold after
	// applying a gain whose boundary alpha level is k (see the gain formula
	// below). An alpha-0 pixel can never cross a positive threshold. The
	// suffix count is constant across a run of absent alpha levels, so each
	// attainable survivor count is represented by a plateau of boundary
	// levels.
	var countAt [256]int
	countAt[255] = hist[255]
	for k := 254; k >= 1; k-- {
		countAt[k] = countAt[k+1] + hist[k]
	}

	total := w * h
	targetCount := target * float64(total)
	if targetCount > float64(total) {
		targetCount = float64(total)
	}

	// Pick the boundary level k (1..255) whose survivor count is closest to
	// the target. k == threshold is the no-op choice (gain 1, the unchanged
	// image). The tie-break is deterministic, in order of preference:
	//
	//  1. smallest difference to the target count;
	//  2. on a count difference tie, the direction that moves coverage
	//     towards the target: a larger survivor count when the unchanged
	//     image is undershot, a smaller one when it is overshot;
	//  3. on a survivor-count tie, the gain closest to 1.0, so an
	//     already-matching image is never modified and, within a plateau,
	//     the least aggressive gain is used;
	//  4. the gain with the smallest magnitude.
	unchangedCount := float64(countAt[threshold])
	undershot := unchangedCount < targetCount
	overshot := unchangedCount > targetCount

	gainFor := func(k int) float64 {
		if k == threshold {
			return 1.0
		}
		return (float64(threshold) - 0.5) / float64(k)
	}

	bestK := threshold
	bestDiff := math.Abs(unchangedCount - targetCount)
	bestCount := unchangedCount
	bestGain := 1.0
	for k := 1; k < 256; k++ {
		count := float64(countAt[k])
		diff := math.Abs(count - targetCount)
		gain := gainFor(k)

		gainDist := math.Abs(gain - 1.0)
		bestGainDist := math.Abs(bestGain - 1.0)

		take := false
		switch {
		case diff < bestDiff:
			take = true
		case diff == bestDiff && undershot && count > bestCount:
			take = true
		case diff == bestDiff && overshot && count < bestCount:
			take = true
		case diff == bestDiff && count == bestCount && gainDist < bestGainDist:
			take = true
		case diff == bestDiff && count == bestCount && gainDist == bestGainDist && gain < bestGain:
			take = true
		}
		if take {
			bestDiff = diff
			bestCount = count
			bestGain = gain
			bestK = k
		}
	}

	// A pixel with alpha a survives when clamp(a*gain) >= threshold, i.e.
	// a*gain >= threshold-0.5. Choosing gain = (threshold-0.5)/k makes the
	// boundary level k produce exactly the count countAt[k]. k == threshold
	// means gain 1, the unchanged image.
	var gain float64
	if bestK == threshold {
		gain = 1.0
	} else {
		// The small epsilon keeps the boundary pixel on the surviving side
		// despite floating-point rounding in the multiplication.
		gain = (float64(threshold)-0.5)/float64(bestK) + 1e-9
	}
	if gain <= 0 {
		return
	}

	for y := 0; y < h; y++ {
		off := y * dst.Stride
		end := off + w*4
		for i := off + 3; i < end; i += 4 {
			dst.Pix[i] = clamp(float64(dst.Pix[i]) * gain)
		}
	}
}
