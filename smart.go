package imaging

import (
	"image"
	"math"
)

const (
	// smartAnalysisMaxSide is the maximum side of the saliency analysis
	// grid. Large images are analyzed on a downsampled grid so both the
	// number of inspected samples and the auxiliary memory stay bounded,
	// independently of the source image size.
	smartAnalysisMaxSide = 256

	// smartSamplesPerCell is the number of samples taken along each axis
	// of one analysis cell, i.e. at most 4x4 source pixels per cell.
	smartSamplesPerCell = 4

	// smartMaxWorkPixels bounds the number of pixels of the intermediate
	// image that is built before the final resampling (about 4 MiB of
	// NRGBA data). It is raised to the target area when the requested
	// thumbnail itself is larger.
	smartMaxWorkPixels = 1 << 20

	// smartMaxCellSamples is the maximum number of samples taken along
	// each axis of one box-averaging cell while downsampling a large crop
	// region. Cells up to this size are averaged exactly; larger cells
	// are sampled evenly, which bounds the total work for huge sources.
	smartMaxCellSamples = 4

	// smartMaxWorkSide bounds the side of that intermediate image. It is
	// raised to the target size when the requested thumbnail is larger,
	// so normal (small) thumbnails never get an unnecessarily blurred
	// intermediate.
	smartMaxWorkSide = 8192

	// smartStripPixels bounds the width of the row buffer used while
	// downsampling an oversized crop region.
	smartStripPixels = 8192

	// smartMaxTargetSide and smartMaxTargetPixels reject target sizes
	// that would make the output allocation unreasonable or overflow
	// the internal integer arithmetic. The area limit is about 16.8
	// million pixels (67 MiB of NRGBA data).
	smartMaxTargetSide   = 1 << 16
	smartMaxTargetPixels = 1 << 24
)

// smartValidTarget reports whether width and height describe a target size
// that SmartCropRect and SmartThumbnail are willing to allocate.
func smartValidTarget(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if width > smartMaxTargetSide || height > smartMaxTargetSide {
		return false
	}
	return int64(width)*int64(height) <= smartMaxTargetPixels
}

// SmartCropRect returns the crop rectangle that SmartThumbnail would use for
// the given target width and height. The rectangle is expressed in the
// coordinate system of the source image and can be passed directly to Crop.
//
// The rectangle has the target aspect ratio and the largest size that fits
// inside the image. Its position is chosen by scoring edge and local-contrast
// saliency inside the candidate windows: edges and locally contrasting areas
// count as important, while fully transparent areas are weighted down.
// Windows with equal scores are resolved in favor of the one closest to the
// image center, and an image without any saliency (for example a uniform
// image) uses a centered window explicitly.
//
// If width or height is not positive, larger than the supported maximum, or
// the image is empty, the returned rectangle is empty. If the requested
// aspect ratio already matches the source, the full source rectangle is
// returned. The source image is only read, never modified.
func SmartCropRect(img image.Image, width, height int) image.Rectangle {
	if img == nil || !smartValidTarget(width, height) {
		return image.Rectangle{}
	}

	b := img.Bounds()
	srcW := b.Dx()
	srcH := b.Dy()
	if srcW <= 0 || srcH <= 0 {
		return image.Rectangle{}
	}

	cropW, cropH := smartCropSize(srcW, srcH, width, height)
	if cropW >= srcW && cropH >= srcH {
		return b
	}

	x, y := smartCropOrigin(img, cropW, cropH)
	return image.Rect(x, y, x+cropW, y+cropH)
}

// SmartThumbnail scales the image to the specified width and height using the
// specified resampling filter, but unlike Thumbnail it does not always crop
// around the center. It first chooses the most salient crop window with the
// target aspect ratio (see SmartCropRect) and then resamples that window to
// the exact target size.
//
// The selection is deterministic: identical inputs always produce identical
// output. Edges and local contrast define saliency; transparent pixels are
// weighted down, so opaque subjects are preferred over transparent filler.
// Images with no saliency at all get a centered crop. Non-zero image bounds
// are supported and preserved by the returned rectangle.
//
// Invalid target sizes (not positive, or unreasonably large) and empty images
// return an empty image. A completely transparent or otherwise featureless
// image is not an error: it gets the centered crop and a uniformly transparent
// (or uniform) result. Very large source images are analyzed on a bounded grid
// and are downsampled to a bounded intermediate image before the final
// resampling, so the auxiliary memory does not grow with the full source
// resolution. The source image is never modified.
//
// Example:
//
//	dstImage := imaging.SmartThumbnail(srcImage, 800, 600, imaging.Lanczos)
func SmartThumbnail(img image.Image, width, height int, filter ResampleFilter) *image.NRGBA {
	if img == nil || !smartValidTarget(width, height) {
		return &image.NRGBA{}
	}

	srcBounds := img.Bounds()
	if srcBounds.Dx() <= 0 || srcBounds.Dy() <= 0 {
		return &image.NRGBA{}
	}

	rect := SmartCropRect(img, width, height)
	if rect.Empty() {
		return &image.NRGBA{}
	}

	return Resize(smartWorkImage(img, rect, width, height), width, height, filter)
}

// smartCropSize returns the largest rectangle with the target aspect ratio
// that fits inside a srcW x srcH image.
func smartCropSize(srcW, srcH, dstW, dstH int) (int, int) {
	srcAR := float64(srcW) / float64(srcH)
	dstAR := float64(dstW) / float64(dstH)

	// Matching aspect ratios need no crop at all; this also avoids a
	// spurious one-pixel crop caused by floating point rounding.
	if math.Abs(srcAR-dstAR) <= 1e-9*math.Max(srcAR, dstAR) {
		return srcW, srcH
	}

	if srcAR > dstAR {
		w := int(math.Floor(float64(srcH)*dstAR + 0.5))
		if w < 1 {
			w = 1
		}
		if w > srcW {
			w = srcW
		}
		return w, srcH
	}

	h := int(math.Floor(float64(srcW)/dstAR + 0.5))
	if h < 1 {
		h = 1
	}
	if h > srcH {
		h = srcH
	}
	return srcW, h
}

// smartCropOrigin computes the top-left corner (in the source image
// coordinate system) of the highest-scoring cropW x cropH window.
func smartCropOrigin(img image.Image, cropW, cropH int) (int, int) {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()

	scale := (maxInt(w, h) + smartAnalysisMaxSide - 1) / smartAnalysisMaxSide
	if scale < 1 {
		scale = 1
	}
	gridW := (w + scale - 1) / scale
	gridH := (h + scale - 1) / scale

	lum, cov := smartBuildGrid(img, scale, gridW, gridH)
	sal := smartSaliency(lum, cov, gridW, gridH)

	// Integral image of the saliency grid for O(1) window scoring.
	stride := gridW + 1
	integral := make([]float64, stride*(gridH+1))
	for y := 0; y < gridH; y++ {
		var rowSum float64
		for x := 0; x < gridW; x++ {
			rowSum += sal[y*gridW+x]
			integral[(y+1)*stride+(x+1)] = integral[y*stride+(x+1)] + rowSum
		}
	}

	winW := int(math.Ceil(float64(cropW) / float64(scale)))
	if winW < 1 {
		winW = 1
	}
	if winW > gridW {
		winW = gridW
	}
	winH := int(math.Ceil(float64(cropH) / float64(scale)))
	if winH < 1 {
		winH = 1
	}
	if winH > gridH {
		winH = gridH
	}

	const eps = 1e-9
	bestScore := math.Inf(-1)
	bestDist := math.Inf(1)
	bestX, bestY := 0, 0

	centerX := float64(w) / 2
	centerY := float64(h) / 2

	for gy := 0; gy <= gridH-winH; gy++ {
		for gx := 0; gx <= gridW-winW; gx++ {
			x1 := gx + winW
			y1 := gy + winH
			score := integral[y1*stride+x1] -
				integral[gy*stride+x1] -
				integral[y1*stride+gx] +
				integral[gy*stride+gx]

			// Distance of the candidate window center to the image center,
			// expressed in source pixels. Used only to break ties.
			dx := float64(gx)*float64(scale) + float64(cropW)/2 - centerX
			dy := float64(gy)*float64(scale) + float64(cropH)/2 - centerY
			dist := dx*dx + dy*dy

			if score > bestScore+eps ||
				(math.Abs(score-bestScore) <= eps && dist < bestDist-eps) {
				bestScore = score
				bestDist = dist
				bestX = gx
				bestY = gy
			}
		}
	}

	// A uniform image has no saliency at all. Use an explicit center
	// fallback instead of the first candidate window.
	if bestScore <= eps {
		pt := anchorPt(b, cropW, cropH, Center)
		return pt.X, pt.Y
	}

	// Map the winning grid window back to source pixels. The scored window
	// can be up to one grid cell larger than the crop window when a huge
	// image is analyzed on a coarse grid; center the crop inside the scored
	// span, which puts the crop within half a cell of the content that
	// produced the score. Then clamp it to the image.
	spanW := winW * scale
	spanH := winH * scale
	x := bestX*scale + (spanW-cropW)/2
	y := bestY*scale + (spanH-cropH)/2
	if x > w-cropW {
		x = w - cropW
	}
	if y > h-cropH {
		y = h - cropH
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return b.Min.X + x, b.Min.Y + y
}

// smartBuildGrid builds a bounded luminance/alpha coverage grid of the image.
// Each cell is represented by at most smartSamplesPerCell samples per axis, and
// the source is scanned in bounded horizontal strips, so the buffer size never
// depends on the image size and every source pixel is inspected at most a
// constant number of times.
func smartBuildGrid(img image.Image, scale, gridW, gridH int) ([]float64, []float64) {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()

	lum := make([]float64, gridW*gridH)
	cov := make([]float64, gridW*gridH)

	const maxSamples = smartSamplesPerCell

	// samplesPerCell returns how many samples an axis of a cell with the
	// given size uses. Small cells are sampled exactly.
	samplesPerCell := func(size int) int {
		n := maxSamples
		if n > size {
			n = size
		}
		return n
	}

	// A strip covers a whole number of grid cells and is limited to
	// smartStripPixels pixels per row.
	cellsPerStrip := smartStripPixels / scale
	if cellsPerStrip < 1 {
		cellsPerStrip = 1
	}

	parallel(0, gridH, func(gys <-chan int) {
		src := newScanner(img)
		var line []uint8

		rowCounts := make([]float64, gridW)
		alphaSum := make([]float64, gridW)
		lumAlphaSum := make([]float64, gridW)

		for gy := range gys {
			y0 := gy * scale
			y1 := y0 + scale
			if y1 > h {
				y1 = h
			}
			cellH := y1 - y0
			nsy := samplesPerCell(cellH)

			for i := range alphaSum {
				alphaSum[i] = 0
				lumAlphaSum[i] = 0
			}

			for gx0 := 0; gx0 < gridW; gx0 += cellsPerStrip {
				gx1 := gx0 + cellsPerStrip
				if gx1 > gridW {
					gx1 = gridW
				}
				sx0 := gx0 * scale
				sx1 := gx1 * scale
				if sx1 > w {
					sx1 = w
				}
				stripW := sx1 - sx0
				if cap(line) < stripW*4 {
					line = make([]uint8, stripW*4)
				}
				line = line[:stripW*4]

				for gx := gx0; gx < gx1; gx++ {
					x0 := gx * scale
					x1 := x0 + scale
					if x1 > w {
						x1 = w
					}
					rowCounts[gx] = float64(samplesPerCell(x1-x0) * nsy)
				}

				for sy := 0; sy < nsy; sy++ {
					py := y0 + (2*sy+1)*cellH/(2*nsy)
					if py >= y1 {
						py = y1 - 1
					}
					src.scan(sx0, py, sx1, py+1, line)

					for gx := gx0; gx < gx1; gx++ {
						x0 := gx*scale - sx0
						x1 := x0 + scale
						if x1 > stripW {
							x1 = stripW
						}
						cellW := x1 - x0
						nsx := samplesPerCell(cellW)
						for sx := 0; sx < nsx; sx++ {
							px := x0 + (2*sx+1)*cellW/(2*nsx)
							if px >= x1 {
								px = x1 - 1
							}
							i := px * 4
							a := float64(line[i+3])
							l := 0.299*float64(line[i]) + 0.587*float64(line[i+1]) + 0.114*float64(line[i+2])
							alphaSum[gx] += a
							lumAlphaSum[gx] += l * a
						}
					}
				}
			}

			for gx := 0; gx < gridW; gx++ {
				i := gy*gridW + gx
				if alphaSum[gx] > 0 {
					lum[i] = lumAlphaSum[gx] / alphaSum[gx] / 255
				}
				cov[i] = alphaSum[gx] / (rowCounts[gx] * 255)
			}
		}
	})

	return lum, cov
}

// smartSaliency converts the luminance/coverage grid into saliency weights.
// It combines edge magnitude (central differences) with local contrast
// (deviation from the 3x3 mean) of both the luminance and the alpha coverage.
// The luminance terms are scaled by alpha coverage, so transparent regions
// contribute little or nothing, while coverage edges still mark the outline of
// an opaque subject on a transparent background.
func smartSaliency(lum, cov []float64, gridW, gridH int) []float64 {
	sal := make([]float64, gridW*gridH)

	central := func(a, b float64) float64 { return (a - b) / 2 }
	hypot := func(x, y float64) float64 { return math.Hypot(x, y) }

	for y := 0; y < gridH; y++ {
		ym := y - 1
		if ym < 0 {
			ym = 0
		}
		yp := y + 1
		if yp >= gridH {
			yp = gridH - 1
		}
		for x := 0; x < gridW; x++ {
			xm := x - 1
			if xm < 0 {
				xm = 0
			}
			xp := x + 1
			if xp >= gridW {
				xp = gridW - 1
			}

			i := y*gridW + x
			l := lum[i]
			c := cov[i]

			lumEdge := hypot(
				central(lum[y*gridW+xp], lum[y*gridW+xm]),
				central(lum[yp*gridW+x], lum[ym*gridW+x]),
			)
			covEdge := hypot(
				central(cov[y*gridW+xp], cov[y*gridW+xm]),
				central(cov[yp*gridW+x], cov[ym*gridW+x]),
			)

			var lumMean, covMean, count float64
			for yy := ym; yy <= yp; yy++ {
				for xx := xm; xx <= xp; xx++ {
					lumMean += lum[yy*gridW+xx]
					covMean += cov[yy*gridW+xx]
					count++
				}
			}
			lumMean /= count
			covMean /= count

			lumContrast := math.Abs(l - lumMean)
			covContrast := math.Abs(c - covMean)

			sal[i] = (lumEdge+lumContrast)*c + covEdge + covContrast*c
		}
	}

	return sal
}

// smartWorkImage returns an image of the selected crop region that is small
// enough for the final Resize call. Large crop regions are downsampled with
// box averaging first, so neither the crop copy nor the resampling weights
// grow beyond a fixed bound. Each axis is downsampled by its own integer
// factor, and the factor is capped so the intermediate image is never smaller
// than the target; the final resampling therefore never upscales a lossy
// intermediate.
func smartWorkImage(img image.Image, rect image.Rectangle, dstW, dstH int) *image.NRGBA {
	cropW := rect.Dx()
	cropH := rect.Dy()

	sideLimit := smartMaxWorkSide
	if dstW > sideLimit {
		sideLimit = dstW
	}
	if dstH > sideLimit {
		sideLimit = dstH
	}
	areaLimit := int64(smartMaxWorkPixels)
	if area := int64(dstW) * int64(dstH); area > areaLimit {
		areaLimit = area
	}

	if int64(cropW)*int64(cropH) <= areaLimit && cropW <= sideLimit && cropH <= sideLimit {
		return Crop(img, rect)
	}

	fx, fy := smartWorkFactors(cropW, cropH, dstW, dstH, sideLimit, areaLimit)
	return smartDownscaleRect(img, rect, fx, fy)
}

// smartWorkFactors chooses the per-axis integer downscale factors for
// smartWorkImage. Each factor is capped so the intermediate image stays at
// least as large as the target on that axis, so the final resampling never has
// to enlarge a lossy intermediate. The area budget bounds the total size; when
// both axes have reached their cap, the intermediate can exceed the budget by
// up to the rounding of the integer factors, which is bounded by a small
// constant multiple of the target area.
func smartWorkFactors(cropW, cropH, dstW, dstH, sideLimit int, areaLimit int64) (int, int) {
	ceilDiv := func(a, b int) int { return (a + b - 1) / b }

	// The largest factor that keeps the downscaled axis at least as large
	// as the target: ceilDiv(cropW, cropW/dstW) >= dstW.
	maxFx := cropW / dstW
	if maxFx < 1 {
		maxFx = 1
	}
	maxFy := cropH / dstH
	if maxFy < 1 {
		maxFy = 1
	}

	fx := 1
	if want := ceilDiv(cropW, sideLimit); want > fx {
		fx = want
	}
	if fx > maxFx {
		fx = maxFx
	}
	fy := 1
	if want := ceilDiv(cropH, sideLimit); want > fy {
		fy = want
	}
	if fy > maxFy {
		fy = maxFy
	}

	// Increase the factors until the intermediate image fits the area
	// budget or both factors reach their cap. Every step strictly
	// increases one factor and the factors are bounded, so the loop always
	// terminates.
	for fx < maxFx || fy < maxFy {
		workW := ceilDiv(cropW, fx)
		workH := ceilDiv(cropH, fy)
		if int64(workW)*int64(workH) <= areaLimit {
			break
		}

		// Scale down the axis that is currently furthest above its target
		// resolution, so both axes keep a similar amount of detail.
		ratioX := float64(workW) / float64(dstW)
		ratioY := float64(workH) / float64(dstH)
		if (ratioX >= ratioY && fx < maxFx) || fy >= maxFy {
			wantW := int(areaLimit / int64(workH))
			if wantW < 1 {
				wantW = 1
			}
			next := ceilDiv(cropW, wantW)
			if next <= fx {
				next = fx + 1
			}
			if next > maxFx {
				next = maxFx
			}
			fx = next
		} else {
			wantH := int(areaLimit / int64(workW))
			if wantH < 1 {
				wantH = 1
			}
			next := ceilDiv(cropH, wantH)
			if next <= fy {
				next = fy + 1
			}
			if next > maxFy {
				next = maxFy
			}
			fy = next
		}
	}

	return fx, fy
}

// smartDownscaleRect shrinks the given source rectangle by separate integer
// factors per axis using alpha-weighted box averaging. Cells no larger than
// smartMaxCellSamples are averaged exactly; larger cells are sampled evenly so
// the work stays bounded for very large sources. Only one source strip is
// buffered at a time, so the memory use is bounded by the strip width instead
// of the full rectangle width.
func smartDownscaleRect(img image.Image, rect image.Rectangle, fx, fy int) *image.NRGBA {
	b := img.Bounds()
	cropW := rect.Dx()
	cropH := rect.Dy()
	relX := rect.Min.X - b.Min.X
	relY := rect.Min.Y - b.Min.Y

	dstW := (cropW + fx - 1) / fx
	dstH := (cropH + fy - 1) / fy
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	src := newScanner(img)

	// Number of samples per cell along each axis.
	nsx := fx
	if nsx > smartMaxCellSamples {
		nsx = smartMaxCellSamples
	}
	nsy := fy
	if nsy > smartMaxCellSamples {
		nsy = smartMaxCellSamples
	}

	// Keep one source row strip within the pixel budget.
	cols := smartStripPixels / fx
	if cols < 1 {
		cols = 1
	}

	// A single output cell wider than the strip budget cannot be scanned
	// in one row buffer; fall back to per-pixel sampling, which keeps the
	// memory bounded for arbitrarily large downscale factors.
	if fx > smartStripPixels {
		return smartDownscaleRectPixel(img, rect, fx, fy)
	}

	line := make([]uint8, 0, smartStripPixels*4)

	// sampleOffset returns the offset of sample i (0-based) inside a
	// cell of the given size, clamped so it never leaves the cell.
	sampleOffset := func(i, size, n int) int {
		off := (2*i + 1) * size / (2 * n)
		if off >= size {
			off = size - 1
		}
		return off
	}

	for x0 := 0; x0 < dstW; x0 += cols {
		x1 := x0 + cols
		if x1 > dstW {
			x1 = dstW
		}
		nx := x1 - x0

		sx0 := x0 * fx
		sx1 := x1 * fx
		if sx1 > cropW {
			sx1 = cropW
		}
		stripW := sx1 - sx0
		if cap(line) < stripW*4 {
			line = make([]uint8, stripW*4)
		}
		line = line[:stripW*4]

		rSum := make([]float64, nx)
		gSum := make([]float64, nx)
		bSum := make([]float64, nx)
		aSum := make([]float64, nx)
		count := make([]float64, nx)

		for y0 := 0; y0 < dstH; y0++ {
			for i := 0; i < nx; i++ {
				rSum[i] = 0
				gSum[i] = 0
				bSum[i] = 0
				aSum[i] = 0
				count[i] = 0
			}

			sy0 := y0 * fy
			sy1 := sy0 + fy
			if sy1 > cropH {
				sy1 = cropH
			}
			cellH := sy1 - sy0

			for iy := 0; iy < nsy; iy++ {
				sy := sy0 + sampleOffset(iy, cellH, nsy)
				src.scan(relX+sx0, relY+sy, relX+sx1, relY+sy+1, line)
				for dx := 0; dx < nx; dx++ {
					cellX0 := dx * fx
					cellX1 := cellX0 + fx
					if cellX1 > stripW {
						cellX1 = stripW
					}
					cellW := cellX1 - cellX0
					for ix := 0; ix < nsx; ix++ {
						x := cellX0 + sampleOffset(ix, cellW, nsx)
						i := x * 4
						a := float64(line[i+3])
						rSum[dx] += float64(line[i]) * a
						gSum[dx] += float64(line[i+1]) * a
						bSum[dx] += float64(line[i+2]) * a
						aSum[dx] += a
						count[dx]++
					}
				}
			}

			for dx := 0; dx < nx; dx++ {
				if count[dx] == 0 {
					continue
				}
				j := y0*dst.Stride + (x0+dx)*4
				d := dst.Pix[j : j+4 : j+4]
				if aSum[dx] > 0 {
					inv := 1 / aSum[dx]
					d[0] = clamp(rSum[dx] * inv)
					d[1] = clamp(gSum[dx] * inv)
					d[2] = clamp(bSum[dx] * inv)
				}
				d[3] = clamp(aSum[dx] / count[dx])
			}
		}
	}

	return dst
}

// smartDownscaleRectPixel is the memory-constrained variant of
// smartDownscaleRect used when a single output cell spans more source columns
// than the strip budget. It scans one source pixel at a time.
func smartDownscaleRectPixel(img image.Image, rect image.Rectangle, fx, fy int) *image.NRGBA {
	b := img.Bounds()
	cropW := rect.Dx()
	cropH := rect.Dy()
	relX := rect.Min.X - b.Min.X
	relY := rect.Min.Y - b.Min.Y

	dstW := (cropW + fx - 1) / fx
	dstH := (cropH + fy - 1) / fy
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	nsx := fx
	if nsx > smartMaxCellSamples {
		nsx = smartMaxCellSamples
	}
	nsy := fy
	if nsy > smartMaxCellSamples {
		nsy = smartMaxCellSamples
	}
	sampleOffset := func(i, size, n int) int {
		off := (2*i + 1) * size / (2 * n)
		if off >= size {
			off = size - 1
		}
		return off
	}

	src := newScanner(img)
	var pix [4]uint8

	for y0 := 0; y0 < dstH; y0++ {
		sy0 := y0 * fy
		sy1 := sy0 + fy
		if sy1 > cropH {
			sy1 = cropH
		}
		cellH := sy1 - sy0

		for x0 := 0; x0 < dstW; x0++ {
			sx0 := x0 * fx
			sx1 := sx0 + fx
			if sx1 > cropW {
				sx1 = cropW
			}
			cellW := sx1 - sx0

			var rSum, gSum, bSum, aSum, count float64
			for iy := 0; iy < nsy; iy++ {
				py := sy0 + sampleOffset(iy, cellH, nsy)
				for ix := 0; ix < nsx; ix++ {
					px := sx0 + sampleOffset(ix, cellW, nsx)
					src.scan(relX+px, relY+py, relX+px+1, relY+py+1, pix[:])
					a := float64(pix[3])
					rSum += float64(pix[0]) * a
					gSum += float64(pix[1]) * a
					bSum += float64(pix[2]) * a
					aSum += a
					count++
				}
			}

			if count == 0 {
				continue
			}
			j := y0*dst.Stride + x0*4
			d := dst.Pix[j : j+4 : j+4]
			if aSum > 0 {
				inv := 1 / aSum
				d[0] = clamp(rSum * inv)
				d[1] = clamp(gSum * inv)
				d[2] = clamp(bSum * inv)
			}
			d[3] = clamp(aSum / count)
		}
	}

	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
