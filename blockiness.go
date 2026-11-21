package imaging

import (
	"errors"
	"fmt"
	"image"
	"math"
)

// BlockinessBlockSize is the side of the JPEG DCT block grid used by JPEGBlockiness.
const BlockinessBlockSize = 8

// DefaultBlockinessEdgeThreshold is the default luminance jump (0-255 scale) above
// which an adjacent pixel pair is treated as a true edge and excluded from the
// blockiness statistics.
const DefaultBlockinessEdgeThreshold = 32.0

// BlockinessOptions configures the JPEGBlockiness function.
// The zero value is valid and uses the defaults documented per field.
type BlockinessOptions struct {
	// OffsetX and OffsetY align the 8x8 block grid relative to the image origin
	// (Bounds().Min). JPEG crops can shift the grid; use the offset to match it.
	// Each must be in [0, 7]. Default is 0.
	OffsetX, OffsetY int

	// Stride subsamples the interior (non-boundary) pixel pairs: every
	// Stride-th interior pair is evaluated, while all block boundary pairs are
	// always evaluated. Must be >= 1. Values < 1 select the default of 1.
	Stride int

	// EdgeThreshold is the luminance jump (0-255 scale) above which an adjacent
	// pixel pair is considered a true contour and excluded from the statistics.
	// Must be >= 0 and not NaN/Inf. Zero selects the default
	// (DefaultBlockinessEdgeThreshold).
	EdgeThreshold float64
}

// BlockScore holds the blockiness score of a single 8x8 block of the grid.
type BlockScore struct {
	// Bounds is the block rectangle in the original image coordinates.
	Bounds image.Rectangle
	// Score is the block blockiness in [0, 1]: higher means stronger
	// discontinuities at the block edges relative to its interior.
	Score float64
}

// BlockinessResult is the output of JPEGBlockiness.
type BlockinessResult struct {
	// Score is the whole-image blockiness score in [0, 1].
	// It is 0 for flat images, images smaller than one block and images
	// without enough opaque pixels to measure.
	Score float64
	// Blocks lists every complete 8x8 block of the grid in row-major order,
	// with Bounds in the original image coordinates. Blocks partially outside
	// the image are omitted.
	Blocks []BlockScore

	rect image.Rectangle
}

// Heatmap renders the per-block scores as a grayscale image with the same
// bounds as the source image: each block is filled with Score*255, pixels not
// covered by a complete block are 0. It is meant to be overlaid on the source
// to locate the blocking artifacts.
func (r BlockinessResult) Heatmap() *image.Gray {
	dst := image.NewGray(r.rect)
	for _, b := range r.Blocks {
		v := uint8(b.Score*255 + 0.5)
		rc := b.Bounds.Intersect(r.rect)
		for y := rc.Min.Y; y < rc.Max.Y; y++ {
			i := dst.PixOffset(rc.Min.X, y)
			for x := rc.Min.X; x < rc.Max.X; x++ {
				dst.Pix[i] = v
				i++
			}
		}
	}
	return dst
}

// JPEGBlockiness estimates the strength of JPEG 8x8 blocking artifacts in img.
//
// It compares luminance jumps across the 8x8 block grid boundaries with jumps
// between adjacent pixels inside the blocks. The score is
// (boundary-interior)/(boundary+interior) clamped to [0, 1]: 0 means no
// detectable blocking, values near 1 mean discontinuities concentrated on the
// grid. Adjacent pairs with a luminance jump above the edge threshold are
// treated as true contours and excluded from both statistics, so sharp real
// edges (text, checkerboards) do not inflate the score. Fully transparent
// pixels are excluded; partially transparent pairs are weighted by the
// minimum alpha of the pair.
//
// The function works on any image.Image (it does not require JPEG-encoded
// input); uncompressed or clean images score near 0. Images smaller than one
// block yield a zero score and an empty block list. An error is returned for
// invalid options; the returned score is never NaN.
func JPEGBlockiness(img image.Image, opts *BlockinessOptions) (BlockinessResult, error) {
	o := BlockinessOptions{Stride: 1, EdgeThreshold: DefaultBlockinessEdgeThreshold}
	if opts != nil {
		o = *opts
		if o.Stride < 1 {
			if o.Stride == 0 {
				o.Stride = 1
			} else {
				return BlockinessResult{}, errors.New("imaging: BlockinessOptions.Stride must be >= 1")
			}
		}
		if o.OffsetX < 0 || o.OffsetX > 7 || o.OffsetY < 0 || o.OffsetY > 7 {
			return BlockinessResult{}, fmt.Errorf("imaging: BlockinessOptions offsets must be in [0, 7], got (%d, %d)", o.OffsetX, o.OffsetY)
		}
		if o.EdgeThreshold == 0 {
			o.EdgeThreshold = DefaultBlockinessEdgeThreshold
		}
		if o.EdgeThreshold < 0 || math.IsNaN(o.EdgeThreshold) || math.IsInf(o.EdgeThreshold, 0) {
			return BlockinessResult{}, fmt.Errorf("imaging: BlockinessOptions.EdgeThreshold must be >= 0 and finite, got %v", o.EdgeThreshold)
		}
	}

	rect := img.Bounds()
	res := BlockinessResult{rect: rect}
	w, h := rect.Dx(), rect.Dy()
	if w < 1 || h < 1 {
		return res, nil
	}

	src := toNRGBA(img)

	// Luminance and alpha-weight per pixel.
	lum := make([]float64, w*h)
	aw := make([]float64, w*h)
	for y := 0; y < h; y++ {
		i := y * src.Stride
		for x := 0; x < w; x++ {
			r := src.Pix[i]
			g := src.Pix[i+1]
			b := src.Pix[i+2]
			a := src.Pix[i+3]
			j := y*w + x
			lum[j] = 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
			aw[j] = float64(a) / 255
			i += 4
		}
	}

	blocksX := (w - o.OffsetX) / BlockinessBlockSize
	blocksY := (h - o.OffsetY) / BlockinessBlockSize
	type acc struct{ sum, wsum float64 }
	gB, gI := acc{}, acc{}
	var bB, bI []acc
	if blocksX > 0 && blocksY > 0 {
		bB = make([]acc, blocksX*blocksY)
		bI = make([]acc, blocksX*blocksY)
		res.Blocks = make([]BlockScore, 0, blocksX*blocksY)
	}

	s := o.Stride
	// addPair accumulates one adjacent-pixel pair into the given accumulators.
	addPair := func(bAcc, iAcc *acc, boundary bool, d, wgt float64) {
		if boundary {
			bAcc.sum += d * wgt
			bAcc.wsum += wgt
		} else {
			iAcc.sum += d * wgt
			iAcc.wsum += wgt
		}
	}
	// addBlockPair attributes a pair to the per-block accumulators. Boundary
	// pairs are added to the blocks on both sides of the grid line, interior
	// pairs to the block containing them. vertical selects the grid line
	// orientation (true: vertical line between columns).
	addBlockPair := func(boundary, vertical bool, d, wgt float64, rx, ry int) {
		if blocksX == 0 || blocksY == 0 {
			return
		}
		add := func(bx, by int) {
			if bx >= 0 && bx < blocksX && by >= 0 && by < blocksY {
				addPair(&bB[by*blocksX+bx], &bI[by*blocksX+bx], boundary, d, wgt)
			}
		}
		bx, by := rx/BlockinessBlockSize, ry/BlockinessBlockSize
		add(bx, by)
		if boundary && vertical {
			add(bx-1, by) // also the block on the left of the grid line
		}
		if boundary && !vertical {
			add(bx, by-1) // also the block above the grid line
		}
	}

	// Horizontal pairs: (x-1, y) and (x, y).
	for y := 0; y < h; y++ {
		for x := 1; x < w; x++ {
			gx := x - o.OffsetX
			boundary := gx >= 0 && gx%BlockinessBlockSize == 0
			if !boundary && (x-1)%s != 0 {
				continue
			}
			j := y*w + x
			wgt := math.Min(aw[j], aw[j-1])
			if wgt == 0 {
				continue
			}
			d := math.Abs(lum[j] - lum[j-1])
			if d > o.EdgeThreshold {
				continue // true edge, excluded from both statistics
			}
			addPair(&gB, &gI, boundary, d, wgt)
			px := x
			if !boundary {
				px = x - 1
			}
			rx, ry := px-o.OffsetX, y-o.OffsetY
			if rx >= 0 && ry >= 0 {
				addBlockPair(boundary, true, d, wgt, rx, ry)
			}
		}
	}
	// Vertical pairs: (x, y-1) and (x, y).
	for y := 1; y < h; y++ {
		for x := 0; x < w; x++ {
			gy := y - o.OffsetY
			boundary := gy >= 0 && gy%BlockinessBlockSize == 0
			if !boundary && (y-1)%s != 0 {
				continue
			}
			j := y*w + x
			wgt := math.Min(aw[j], aw[j-w])
			if wgt == 0 {
				continue
			}
			d := math.Abs(lum[j] - lum[j-w])
			if d > o.EdgeThreshold {
				continue
			}
			addPair(&gB, &gI, boundary, d, wgt)
			py := y
			if !boundary {
				py = y - 1
			}
			rx, ry := x-o.OffsetX, py-o.OffsetY
			if rx >= 0 && ry >= 0 {
				addBlockPair(boundary, false, d, wgt, rx, ry)
			}
		}
	}

	const eps = 1e-9
	scoreOf := func(b, i acc) float64 {
		if b.wsum == 0 || i.wsum == 0 {
			return 0
		}
		bm := b.sum / b.wsum
		im := i.sum / i.wsum
		v := (bm - im) / (bm + im + eps)
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}

	res.Score = scoreOf(gB, gI)
	for by := 0; by < blocksY; by++ {
		for bx := 0; bx < blocksX; bx++ {
			x0 := rect.Min.X + o.OffsetX + bx*BlockinessBlockSize
			y0 := rect.Min.Y + o.OffsetY + by*BlockinessBlockSize
			res.Blocks = append(res.Blocks, BlockScore{
				Bounds: image.Rect(x0, y0, x0+BlockinessBlockSize, y0+BlockinessBlockSize),
				Score:  scoreOf(bB[by*blocksX+bx], bI[by*blocksX+bx]),
			})
		}
	}
	return res, nil
}
