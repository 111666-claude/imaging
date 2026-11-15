package imaging

import (
	"errors"
	"image"
	"math"
)

// ErrSeamCarveInvalidSize means the requested target size is negative, zero for
// both dimensions, or larger than the source image (seam carving can only shrink).
var ErrSeamCarveInvalidSize = errors.New("imaging: seam carving target size must be positive and not exceed the source size")

// ErrSeamCarveMaskSize means the protection mask dimensions do not match the source image dimensions.
var ErrSeamCarveMaskSize = errors.New("imaging: seam carving mask size does not match the source image size")

// ErrSeamCarveNoSeam means no seam could be found that avoids the protected
// area, so the requested size cannot be reached.
var ErrSeamCarveNoSeam = errors.New("imaging: no feasible seam found, protected area blocks all paths")

// SeamCarve shrinks the image to the given width and height using content-aware
// seam carving: it repeatedly finds and removes the lowest-energy seam (a path
// of connected pixels crossing the image), re-evaluating the remaining image
// after every removal. Unlike Resize it does not distort the content, and unlike
// Crop it removes pixels from the least important areas instead of the edges.
//
// A zero width or height keeps the corresponding source dimension. Width and
// height can be reduced independently or together, but never increased.
//
// The optional mask marks important areas that seams must not cross when any
// other path exists. The mask is aligned with the source image by its top-left
// corner and must have the same width and height; any mask pixel with non-zero
// alpha protects the corresponding source pixel. Pass nil for no protection.
//
// Fully transparent pixels are treated as low-energy (premultiplied channels
// are used), so seams naturally run through transparent areas first.
//
// The function is deterministic: identical inputs always produce identical
// output. It returns ErrSeamCarveInvalidSize for invalid or enlarging target
// sizes, ErrSeamCarveMaskSize for a mask size mismatch and ErrSeamCarveNoSeam
// when the protected area blocks every possible seam.
//
// Example:
//
//	// Shrink a landscape photo to a portrait-friendly size, protecting the
//	// subject marked in maskImage.
//	dstImage, err := imaging.SeamCarve(srcImage, 600, 0, maskImage)
func SeamCarve(img image.Image, width, height int, mask image.Image) (*image.NRGBA, error) {
	srcBounds := img.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, ErrSeamCarveInvalidSize
	}

	dstW, dstH := width, height
	if dstW == 0 {
		dstW = srcW
	}
	if dstH == 0 {
		dstH = srcH
	}
	if dstW < 0 || dstH < 0 || dstW > srcW || dstH > srcH {
		return nil, ErrSeamCarveInvalidSize
	}

	var prot []bool
	if mask != nil {
		mb := mask.Bounds()
		if mb.Dx() != srcW || mb.Dy() != srcH {
			return nil, ErrSeamCarveMaskSize
		}
		prot = make([]bool, srcW*srcH)
		for y := 0; y < srcH; y++ {
			for x := 0; x < srcW; x++ {
				_, _, _, a := mask.At(mb.Min.X+x, mb.Min.Y+y).RGBA()
				if a != 0 {
					prot[y*srcW+x] = true
				}
			}
		}
	}

	if dstW == srcW && dstH == srcH {
		return Clone(img), nil
	}

	// Clone normalizes a non-zero origin and guarantees a packed stride.
	src := Clone(img)
	pix := src.Pix
	w, h := srcW, srcH

	for w > dstW {
		seam, ok := findVerticalSeam(seamEnergy(pix, w, h, prot), w, h)
		if !ok {
			return nil, ErrSeamCarveNoSeam
		}
		pix = removeVerticalSeam(pix, w, h, seam)
		prot = removeVerticalSeamFlags(prot, w, h, seam)
		w--
	}
	for h > dstH {
		seam, ok := findHorizontalSeam(seamEnergy(pix, w, h, prot), w, h)
		if !ok {
			return nil, ErrSeamCarveNoSeam
		}
		pix = removeHorizontalSeam(pix, w, h, seam)
		prot = removeHorizontalSeamFlags(prot, w, h, seam)
		h--
	}

	return &image.NRGBA{
		Pix:    pix,
		Stride: w * 4,
		Rect:   image.Rect(0, 0, w, h),
	}, nil
}

// pixDiff returns the sum of absolute differences of the alpha-premultiplied
// channels of the pixels at packed offsets i and j.
func pixDiff(pix []uint8, i, j int) float64 {
	i *= 4
	j *= 4
	ai := float64(pix[i+3])
	aj := float64(pix[j+3])
	d := math.Abs(ai - aj)
	d += math.Abs(float64(pix[i+0])*ai/255 - float64(pix[j+0])*aj/255)
	d += math.Abs(float64(pix[i+1])*ai/255 - float64(pix[j+1])*aj/255)
	d += math.Abs(float64(pix[i+2])*ai/255 - float64(pix[j+2])*aj/255)
	return d
}

// seamEnergy computes the per-pixel energy (gradient magnitude of the
// premultiplied channels) of a packed w*h NRGBA pixel buffer. Protected
// pixels get infinite energy so seams avoid them while any path exists.
func seamEnergy(pix []uint8, w, h int, prot []bool) []float64 {
	energy := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if prot != nil && prot[i] {
				energy[i] = math.Inf(1)
				continue
			}
			xm, xp := x, x
			if xm > 0 {
				xm--
			}
			if xp < w-1 {
				xp++
			}
			ym, yp := y, y
			if ym > 0 {
				ym--
			}
			if yp < h-1 {
				yp++
			}
			energy[i] = pixDiff(pix, y*w+xm, y*w+xp) + pixDiff(pix, ym*w+x, yp*w+x)
		}
	}
	return energy
}

// findVerticalSeam returns the x index of the lowest-cost vertical seam for
// every row. Ties are resolved towards lower x indexes, keeping the result
// deterministic. The second return value is false when every seam crosses
// infinite-energy (protected) pixels.
func findVerticalSeam(energy []float64, w, h int) ([]int, bool) {
	cost := make([]float64, w*h)
	copy(cost[:w], energy[:w])
	for y := 1; y < h; y++ {
		row, prev := y*w, (y-1)*w
		for x := 0; x < w; x++ {
			best := cost[prev+x]
			if x > 0 && cost[prev+x-1] < best {
				best = cost[prev+x-1]
			}
			if x < w-1 && cost[prev+x+1] < best {
				best = cost[prev+x+1]
			}
			cost[row+x] = energy[row+x] + best
		}
	}

	last := (h - 1) * w
	bx := 0
	for x := 1; x < w; x++ {
		if cost[last+x] < cost[last+bx] {
			bx = x
		}
	}
	if math.IsInf(cost[last+bx], 1) {
		return nil, false
	}

	seam := make([]int, h)
	seam[h-1] = bx
	for y := h - 1; y > 0; y-- {
		x := seam[y]
		prev := (y - 1) * w
		best := x
		if x > 0 && cost[prev+x-1] < cost[prev+best] {
			best = x - 1
		}
		if x < w-1 && cost[prev+x+1] < cost[prev+best] {
			best = x + 1
		}
		seam[y-1] = best
	}
	return seam, true
}

// findHorizontalSeam returns the y index of the lowest-cost horizontal seam
// for every column. Ties are resolved towards lower y indexes.
func findHorizontalSeam(energy []float64, w, h int) ([]int, bool) {
	cost := make([]float64, w*h)
	for y := 0; y < h; y++ {
		cost[y*w] = energy[y*w]
	}
	for x := 1; x < w; x++ {
		for y := 0; y < h; y++ {
			best := cost[y*w+x-1]
			if y > 0 && cost[(y-1)*w+x-1] < best {
				best = cost[(y-1)*w+x-1]
			}
			if y < h-1 && cost[(y+1)*w+x-1] < best {
				best = cost[(y+1)*w+x-1]
			}
			cost[y*w+x] = energy[y*w+x] + best
		}
	}

	by := 0
	for y := 1; y < h; y++ {
		if cost[y*w+w-1] < cost[by*w+w-1] {
			by = y
		}
	}
	if math.IsInf(cost[by*w+w-1], 1) {
		return nil, false
	}

	seam := make([]int, w)
	seam[w-1] = by
	for x := w - 1; x > 0; x-- {
		y := seam[x]
		best := y
		if y > 0 && cost[(y-1)*w+x-1] < cost[best*w+x-1] {
			best = y - 1
		}
		if y < h-1 && cost[(y+1)*w+x-1] < cost[best*w+x-1] {
			best = y + 1
		}
		seam[x-1] = best
	}
	return seam, true
}

// removeVerticalSeam deletes one pixel per row from a packed w*h NRGBA buffer.
func removeVerticalSeam(pix []uint8, w, h int, seam []int) []uint8 {
	out := make([]uint8, (w-1)*h*4)
	for y := 0; y < h; y++ {
		sx := seam[y]
		srcRow := pix[y*w*4 : (y+1)*w*4]
		dstRow := out[y*(w-1)*4 : (y+1)*(w-1)*4]
		copy(dstRow, srcRow[:sx*4])
		copy(dstRow[sx*4:], srcRow[sx*4+4:])
	}
	return out
}

// removeHorizontalSeam deletes one pixel per column from a packed w*h NRGBA buffer.
func removeHorizontalSeam(pix []uint8, w, h int, seam []int) []uint8 {
	out := make([]uint8, w*(h-1)*4)
	for x := 0; x < w; x++ {
		sy := seam[x]
		for y := 0; y < h-1; y++ {
			sy2 := y
			if y >= sy {
				sy2 = y + 1
			}
			copy(out[(y*w+x)*4:(y*w+x)*4+4], pix[(sy2*w+x)*4:(sy2*w+x)*4+4])
		}
	}
	return out
}

// removeVerticalSeamFlags deletes the seam entries from the protection flags.
func removeVerticalSeamFlags(prot []bool, w, h int, seam []int) []bool {
	if prot == nil {
		return nil
	}
	out := make([]bool, (w-1)*h)
	for y := 0; y < h; y++ {
		sx := seam[y]
		copy(out[y*(w-1):], prot[y*w:y*w+sx])
		copy(out[y*(w-1)+sx:], prot[y*w+sx+1:(y+1)*w])
	}
	return out
}

// removeHorizontalSeamFlags deletes the seam entries from the protection flags.
func removeHorizontalSeamFlags(prot []bool, w, h int, seam []int) []bool {
	if prot == nil {
		return nil
	}
	out := make([]bool, w*(h-1))
	for x := 0; x < w; x++ {
		sy := seam[x]
		for y := 0; y < h-1; y++ {
			sy2 := y
			if y >= sy {
				sy2 = y + 1
			}
			out[y*w+x] = prot[sy2*w+x]
		}
	}
	return out
}
