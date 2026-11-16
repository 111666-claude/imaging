package imaging

import (
	"image"
	"image/color"
	"image/draw"
	"sort"
)

const (
	// maxQuantColors is the maximum palette size supported by GIF.
	maxQuantColors = 256

	// maxExactQuantColors bounds the number of distinct visible colors
	// that are collected exactly. When an image contains more colors, a
	// fixed 5-bit-per-channel histogram is used instead. This keeps the
	// working memory of the quantizer independent of the image size.
	maxExactQuantColors = 1 << 14

	// quantHistBits is the number of bits per color channel used by the
	// fallback histogram.
	quantHistBits = 5
	quantHistSize = 1 << (3 * quantHistBits)
)

// PaletteQuantizer is a deterministic adaptive palette quantizer that can be
// passed directly to GIFQuantizer.
//
// Quantize builds a palette from the colors that are actually present in the
// image, weighting each pixel equally. Visible pixels are collected in a
// histogram and median-cut into at most NumColors palette entries, so the
// dominant colors of the image are preserved even when the image contains
// more colors than the palette budget.
//
// Fully transparent pixels (alpha == 0) are excluded from the visible color
// statistics, so their RGB values cannot pollute the palette. If the image
// contains transparent pixels, a dedicated fully transparent color
// (color.NRGBA{}) is appended first, unless the palette already contains a
// transparent entry. GIF encoders use the first fully transparent palette
// entry as the transparent index, and transparent source pixels map to it
// exactly.
//
// The result is deterministic: the same image and the same input palette
// always produce the same colors in the same order. This holds for images
// with non-zero Bounds and for any image.Image implementation.
//
// Extra memory is bounded: the exact histogram stores at most
// maxExactQuantColors (16384) distinct colors; images with more colors use a
// fixed 32768-bin histogram, plus one scanline of the image width. The palette
// itself never grows beyond NumColors entries.
//
// A PaletteQuantizer is safe for concurrent use.
type PaletteQuantizer struct {
	// NumColors is the maximum number of colors Quantize appends to the
	// palette. Values outside the range [1, 256] are treated as 256, the
	// same rule image/gif applies to gif.Options.NumColors.
	NumColors int
}

// NewPaletteQuantizer returns a deterministic adaptive palette quantizer that
// produces at most numColors colors. Invalid values of numColors are treated
// as 256.
//
// Example:
//
//	// Encode a pixel-art texture as a GIF using at most 16 colors.
//	err := imaging.Save(srcImage, "texture.gif",
//		imaging.GIFNumColors(16),
//		imaging.GIFQuantizer(imaging.NewPaletteQuantizer(16)),
//	)
func NewPaletteQuantizer(numColors int) *PaletteQuantizer {
	return &PaletteQuantizer{NumColors: numColors}
}

// Quantize implements draw.Quantizer. It appends at most
// min(NumColors, cap(p)-len(p)) colors to p and returns the updated palette.
//
// The returned palette never contains duplicate colors that were added by this
// method, and every added index is a valid color. For an empty (zero-sized)
// image a single opaque black color is appended so that the palette remains
// usable for encoding; a fully transparent image yields a single transparent
// color.
func (q PaletteQuantizer) Quantize(p color.Palette, m image.Image) color.Palette {
	budget := cap(p) - len(p)
	if budget <= 0 {
		return p
	}

	// Drop nil entries: GIF encoders reject a palette that contains them, and
	// a nil entry has no usable color. Copy into a new slice so the caller's
	// palette is not modified, preserving the original capacity budget.
	if hasNilPaletteEntry(p) {
		dst := make(color.Palette, 0, cap(p))
		for _, c := range p {
			if c != nil {
				dst = append(dst, c)
			}
		}
		p = dst
		budget = cap(p) - len(p)
	}

	limit := q.NumColors
	if limit < 1 || limit > maxQuantColors {
		limit = maxQuantColors
	}
	if limit > budget {
		limit = budget
	}

	colors, hasTransparent := quantizeColors(m)
	hasVisible := len(colors) > 0

	// Reserve one slot for the transparent index so transparency is never
	// lost. If the caller's palette already has a transparent entry, reuse
	// it instead of adding a second one: GIF files support only a single
	// transparent index. When the budget is one color and the image also has
	// visible pixels, a visible color is preferred over a palette that would
	// make the whole picture transparent.
	if hasTransparent && !paletteHasTransparent(p) && (!hasVisible || limit >= 2) {
		p = append(p, color.NRGBA{})
		limit--
	}

	if limit > 0 {
		visible := buildQuantPalette(colors, limit)
		existing := paletteColorKeys(p)
		for _, c := range visible {
			key := colorKey16(uint16(c.r)*0x101, uint16(c.g)*0x101, uint16(c.b)*0x101, 0xffff)
			if _, ok := existing[key]; ok {
				continue
			}
			existing[key] = struct{}{}
			p = append(p, color.NRGBA{R: c.r, G: c.g, B: c.b, A: 0xff})
		}
	}

	// Keep the palette usable when the image has no pixels at all: GIF
	// encoders reject a frame with an empty palette.
	if len(p) == 0 && cap(p) > 0 {
		p = append(p, color.NRGBA{A: 0xff})
	}

	return p
}

// AdaptivePalette returns a deterministic adaptive palette for img with at
// most numColors colors. It is a convenience wrapper around
// PaletteQuantizer.Quantize for callers that need the palette itself rather
// than a draw.Quantizer.
//
// Invalid values of numColors are treated as 256. See PaletteQuantizer for the
// exact behavior, including transparency handling.
//
// Example:
//
//	pal := imaging.AdaptivePalette(srcImage, 16)
func AdaptivePalette(img image.Image, numColors int) color.Palette {
	n := numColors
	if n < 1 || n > maxQuantColors {
		n = maxQuantColors
	}
	return PaletteQuantizer{NumColors: n}.Quantize(make(color.Palette, 0, n), img)
}

// Ensure that the quantizer can be passed to image/gif's Options.Quantizer
// and imaging.GIFQuantizer.
var _ draw.Quantizer = PaletteQuantizer{}

// weightedColor is an opaque RGB color with a pixel count used by the
// median-cut palette builder.
type weightedColor struct {
	r, g, b uint8
	count   uint64
}

// quantBox is a half-open range of weightedColor samples that median-cut
// can split further.
type quantBox struct {
	lo, hi int
	count  uint64
}

// quantizeColors returns the visible colors of m together with a flag that
// reports whether m contains fully transparent pixels. It never returns
// duplicate RGB samples in the exact mode; the fallback histogram may return
// duplicate averaged colors, which buildQuantPalette deduplicates later.
func quantizeColors(m image.Image) ([]weightedColor, bool) {
	if m == nil {
		return nil, false
	}
	b := m.Bounds()
	if b.Empty() {
		return nil, false
	}

	src := newScanner(m)
	if src.w <= 0 || src.h <= 0 {
		return nil, false
	}

	counts, hasTransparent, overflow := scanExactColors(src)
	if !overflow {
		return exactWeightedColors(counts), hasTransparent
	}
	return scanQuantHistogram(src)
}

// scanExactColors counts the distinct visible colors of src exactly. It stops
// as soon as more than maxExactQuantColors colors are found and returns
// overflow=true, in which case the caller must rescan using the bounded
// histogram.
func scanExactColors(src *scanner) (counts map[uint32]uint64, hasTransparent, overflow bool) {
	counts = make(map[uint32]uint64, 1024)
	scanLine := make([]uint8, src.w*4)
	for y := 0; y < src.h; y++ {
		src.scan(0, y, src.w, y+1, scanLine)
		for i := 0; i+3 < len(scanLine); i += 4 {
			if scanLine[i+3] == 0 {
				hasTransparent = true
				continue
			}
			key := uint32(scanLine[i])<<16 | uint32(scanLine[i+1])<<8 | uint32(scanLine[i+2])
			counts[key]++
			if len(counts) > maxExactQuantColors {
				return nil, hasTransparent, true
			}
		}
	}
	return counts, hasTransparent, false
}

// exactWeightedColors converts the exact color map into a deterministically
// ordered sample slice.
func exactWeightedColors(counts map[uint32]uint64) []weightedColor {
	if len(counts) == 0 {
		return nil
	}
	keys := make([]uint32, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	colors := make([]weightedColor, len(keys))
	for i, k := range keys {
		colors[i] = weightedColor{
			r:     uint8(k >> 16),
			g:     uint8(k >> 8),
			b:     uint8(k),
			count: counts[k],
		}
	}
	return colors
}

// quantHistBin is one cell of the bounded fallback histogram.
type quantHistBin struct {
	count   uint64
	r, g, b uint64
}

// scanQuantHistogram collects the visible colors of src in a fixed-size
// 5-bit-per-channel histogram. Its memory use depends only on quantHistSize,
// not on the image size, and its result is deterministic.
func scanQuantHistogram(src *scanner) ([]weightedColor, bool) {
	bins := make([]quantHistBin, quantHistSize)
	hasTransparent := false
	scanLine := make([]uint8, src.w*4)
	for y := 0; y < src.h; y++ {
		src.scan(0, y, src.w, y+1, scanLine)
		for i := 0; i+3 < len(scanLine); i += 4 {
			if scanLine[i+3] == 0 {
				hasTransparent = true
				continue
			}
			r, g, b := scanLine[i], scanLine[i+1], scanLine[i+2]
			idx := int(r>>(8-quantHistBits))<<(2*quantHistBits) |
				int(g>>(8-quantHistBits))<<quantHistBits |
				int(b>>(8-quantHistBits))
			bin := &bins[idx]
			bin.count++
			bin.r += uint64(r)
			bin.g += uint64(g)
			bin.b += uint64(b)
		}
	}

	colors := make([]weightedColor, 0, quantHistSize)
	for i := range bins {
		bin := &bins[i]
		if bin.count == 0 {
			continue
		}
		colors = append(colors, weightedColor{
			r:     uint8((bin.r + bin.count/2) / bin.count),
			g:     uint8((bin.g + bin.count/2) / bin.count),
			b:     uint8((bin.b + bin.count/2) / bin.count),
			count: bin.count,
		})
	}
	return colors, hasTransparent
}

// buildQuantPalette reduces colors to at most maxColors representatives using
// a deterministic weighted median-cut. The returned colors are unique and
// sorted by descending pixel count, then by RGB value.
//
// Colors that occupy at least 1/maxColors of the visible pixels are reserved
// verbatim before the cut, so the dominant colors of the image are always
// preserved exactly instead of being averaged with nearby colors.
func buildQuantPalette(colors []weightedColor, maxColors int) []weightedColor {
	if len(colors) == 0 || maxColors <= 0 {
		return nil
	}
	// Merge duplicate RGB values first so the cut never spends a split on
	// two samples that represent the same color and so the returned palette
	// has exactly one entry per color.
	samples := dedupeWeightedColors(colors)
	if len(samples) <= maxColors {
		return samples
	}

	var total uint64
	for _, c := range samples {
		total += c.count
	}

	// Reserve the most frequent colors that are large enough to justify a
	// palette slot on their own. samples is sorted by descending count, so
	// the loop stops at the first color below the threshold.
	//
	// Because there are more distinct samples than maxColors, the reserved
	// prefix is always shorter than maxColors: if it filled every slot, the
	// reserved counts alone would already cover the total weight, leaving no
	// weight for the other distinct colors.
	reserved := 0
	for reserved < maxColors {
		if samples[reserved].count*uint64(maxColors) < total {
			break
		}
		reserved++
	}

	out := make([]weightedColor, 0, maxColors)
	out = append(out, samples[:reserved]...)

	// reserved is always strictly below maxColors here and every remaining
	// sample carries weight, so the rest outnumbers the remaining budget and
	// the cut is always performed.
	rest := samples[reserved:]
	out = append(out, medianCut(rest, maxColors-reserved)...)
	return dedupeWeightedColors(out)
}

// medianCut splits colors into at most maxColors boxes using weighted
// median-cut and returns the count-weighted average of every box.
func medianCut(colors []weightedColor, maxColors int) []weightedColor {
	samples := make([]weightedColor, len(colors))
	copy(samples, colors)

	var total uint64
	for _, c := range samples {
		total += c.count
	}

	boxes := make([]quantBox, 0, maxColors)
	boxes = append(boxes, quantBox{lo: 0, hi: len(samples), count: total})
	// deadBoxes mirrors boxes: a box is marked dead when it cannot be split
	// again. Boxes are never removed, so a simple parallel flag slice keeps
	// the box index stable and the split order deterministic.
	deadBoxes := make([]bool, 0, maxColors)
	deadBoxes = append(deadBoxes, false)

	for len(boxes) < maxColors {
		// Split the box that contains the most pixels first, so the most
		// frequent colors stay apart. Ties are resolved by box creation
		// order, which is deterministic.
		best := -1
		var bestCount uint64
		for i := range boxes {
			if deadBoxes[i] || boxes[i].hi-boxes[i].lo < 2 {
				continue
			}
			if best < 0 || boxes[i].count > bestCount {
				best = i
				bestCount = boxes[i].count
			}
		}
		if best < 0 {
			return medianCutBoxAverages(samples, boxes)
		}

		split, ok := splitQuantBox(samples, boxes[best])
		if !ok {
			// Samples are deduplicated before the cut, so a box with two or
			// more entries spans a color range and can be split. This guard
			// keeps the loop finite should that invariant change.
			deadBoxes[best] = true
			continue
		}

		box := &boxes[best]
		leftCount := countWeightedColors(samples[box.lo:split])
		rightCount := box.count - leftCount
		hi := box.hi
		box.hi = split
		box.count = leftCount
		boxes = append(boxes, quantBox{lo: split, hi: hi, count: rightCount})
		deadBoxes = append(deadBoxes, false)
	}

	return medianCutBoxAverages(samples, boxes)
}

// medianCutBoxAverages returns the count-weighted average color of every box.
func medianCutBoxAverages(samples []weightedColor, boxes []quantBox) []weightedColor {
	palette := make([]weightedColor, 0, len(boxes))
	for i := range boxes {
		r, g, b := weightedColorAverage(samples[boxes[i].lo:boxes[i].hi])
		palette = append(palette, weightedColor{r: r, g: g, b: b, count: boxes[i].count})
	}
	return palette
}

// splitQuantBox sorts the sample range of box along its widest channel and
// splits it at the weighted median. It reports false when the box contains
// only one distinct color and cannot be split any further (medianCut only
// calls it for boxes with at least two samples, all of which are distinct).
func splitQuantBox(samples []weightedColor, box quantBox) (int, bool) {
	lo, hi := box.lo, box.hi
	if hi-lo < 2 {
		// medianCut never asks for this; guard so the function is safe to
		// call on its own.
		return 0, false
	}
	minR, maxR := samples[lo].r, samples[lo].r
	minG, maxG := samples[lo].g, samples[lo].g
	minB, maxB := samples[lo].b, samples[lo].b
	for _, c := range samples[lo+1 : hi] {
		if c.r < minR {
			minR = c.r
		}
		if c.r > maxR {
			maxR = c.r
		}
		if c.g < minG {
			minG = c.g
		}
		if c.g > maxG {
			maxG = c.g
		}
		if c.b < minB {
			minB = c.b
		}
		if c.b > maxB {
			maxB = c.b
		}
	}

	channel := 0
	rng := int(maxR) - int(minR)
	if d := int(maxG) - int(minG); d > rng {
		channel = 1
		rng = d
	}
	if d := int(maxB) - int(minB); d > rng {
		channel = 2
		rng = d
	}
	if rng == 0 {
		return 0, false
	}

	sort.Slice(samples[lo:hi], func(i, j int) bool {
		a, b := samples[lo+i], samples[lo+j]
		switch channel {
		case 0:
			if a.r != b.r {
				return a.r < b.r
			}
			if a.g != b.g {
				return a.g < b.g
			}
			return a.b < b.b
		case 1:
			if a.g != b.g {
				return a.g < b.g
			}
			if a.r != b.r {
				return a.r < b.r
			}
			return a.b < b.b
		default:
			if a.b != b.b {
				return a.b < b.b
			}
			if a.r != b.r {
				return a.r < b.r
			}
			return a.g < b.g
		}
	})

	// Find the first position at which the accumulated weight reaches half
	// of the box weight. The loop always advances at least one sample and
	// stops before the last one, so lo < split < hi.
	var acc uint64
	split := lo
	for split < hi-1 {
		acc += samples[split].count
		split++
		if acc*2 >= box.count {
			break
		}
	}
	return split, true
}

// countWeightedColors returns the total pixel count of colors.
func countWeightedColors(colors []weightedColor) uint64 {
	var total uint64
	for _, c := range colors {
		total += c.count
	}
	return total
}

// weightedColorAverage returns the count-weighted average color of colors,
// which must be non-empty and have a non-zero total count.
func weightedColorAverage(colors []weightedColor) (r, g, b uint8) {
	var sr, sg, sb, count uint64
	for _, c := range colors {
		sr += uint64(c.r) * c.count
		sg += uint64(c.g) * c.count
		sb += uint64(c.b) * c.count
		count += c.count
	}
	return uint8((sr + count/2) / count),
		uint8((sg + count/2) / count),
		uint8((sb + count/2) / count)
}

// dedupeWeightedColors merges entries with the same RGB value (summing their
// counts) and sorts the result by descending pixel count, then by RGB. The
// output is therefore deterministic and contains no duplicate colors.
func dedupeWeightedColors(colors []weightedColor) []weightedColor {
	if len(colors) == 0 {
		return nil
	}
	counts := make(map[uint32]uint64, len(colors))
	for _, c := range colors {
		key := uint32(c.r)<<16 | uint32(c.g)<<8 | uint32(c.b)
		counts[key] += c.count
	}
	keys := make([]uint32, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := counts[keys[i]], counts[keys[j]]
		if ci != cj {
			return ci > cj
		}
		return keys[i] < keys[j]
	})
	out := make([]weightedColor, len(keys))
	for i, k := range keys {
		out[i] = weightedColor{
			r:     uint8(k >> 16),
			g:     uint8(k >> 8),
			b:     uint8(k),
			count: counts[k],
		}
	}
	return out
}

// paletteHasTransparent reports whether p contains a fully transparent color.
func paletteHasTransparent(p color.Palette) bool {
	for _, c := range p {
		if c == nil {
			continue
		}
		if _, _, _, a := c.RGBA(); a == 0 {
			return true
		}
	}
	return false
}

// hasNilPaletteEntry reports whether p contains a nil color entry.
func hasNilPaletteEntry(p color.Palette) bool {
	for _, c := range p {
		if c == nil {
			return true
		}
	}
	return false
}

// paletteColorKeys returns the 16-bit RGBA keys of all non-nil colors in p.
func paletteColorKeys(p color.Palette) map[uint64]struct{} {
	keys := make(map[uint64]struct{}, len(p))
	for _, c := range p {
		if c == nil {
			continue
		}
		r, g, b, a := c.RGBA()
		keys[colorKey16(uint16(r), uint16(g), uint16(b), uint16(a))] = struct{}{}
	}
	return keys
}

// colorKey16 packs a 16-bit RGBA color into a single comparable key.
func colorKey16(r, g, b, a uint16) uint64 {
	return uint64(r)<<48 | uint64(g)<<32 | uint64(b)<<16 | uint64(a)
}
