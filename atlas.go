package imaging

import (
	"errors"
	"fmt"
	"image"
	"sort"
)

// NamedImage is an image paired with a frame name used to look it up
// in the generated sprite atlas.
type NamedImage struct {
	// Name is the unique frame name.
	Name string
	// Image is the source image. Its Bounds rectangle may have a
	// non-zero Min; the reported frame metadata keeps that coordinate
	// system so the frame can be restored at its original position.
	Image image.Image
}

// AtlasOptions controls the sprite atlas generation.
type AtlasOptions struct {
	// MaxWidth is the maximum allowed atlas image width. Must be > 0.
	MaxWidth int
	// MaxHeight is the maximum allowed atlas image height. Must be > 0.
	MaxHeight int
	// Padding is the number of fully transparent pixels kept between
	// the outer rectangles (including the extrusion zone) of any two
	// frames. Must be >= 0.
	Padding int
	// Extrude is the number of pixels the edge pixels of every frame
	// are repeated outwards. This avoids color bleeding when the atlas
	// is sampled with linear filtering. Must be >= 0.
	Extrude int
	// Trim enables cropping fully transparent borders off each frame
	// before placing it. The cropped rectangle is reported in the frame
	// metadata so the original image can still be reconstructed.
	Trim bool
}

// AtlasFrame describes a single frame placed in an atlas.
type AtlasFrame struct {
	// Name is the frame name, matching the NamedImage input.
	Name string
	// Frame is the rectangle of the original image, including the
	// transparent borders. Its size equals the original image size and
	// its Min matches the original image Bounds Min.
	Frame image.Rectangle
	// Bounds is the rectangle of the non-transparent content inside
	// the original image. It equals Frame when trimming is disabled.
	Bounds image.Rectangle
	// Atlas is the rectangle occupied by the frame content in the
	// atlas image. When Extrude > 0, the repeated edge pixels extend
	// Extrude pixels beyond this rectangle on every side.
	Atlas image.Rectangle
}

// Atlas packs the named input images into a single sprite atlas image
// and returns the atlas together with the per-frame metadata: the
// original frame rectangle, the trimmed content bounds inside the
// original image, and the content rectangle inside the atlas.
//
// The layout is deterministic: the same input order and options always
// produce the same atlas and metadata. The function never returns a
// partially built atlas; on any error the returned image and metadata
// are nil.
func Atlas(frames []NamedImage, opts AtlasOptions) (*image.NRGBA, []AtlasFrame, error) {
	if opts.MaxWidth <= 0 || opts.MaxHeight <= 0 {
		return nil, nil, errors.New("imaging: atlas: MaxWidth and MaxHeight must be positive")
	}
	if opts.Padding < 0 {
		return nil, nil, errors.New("imaging: atlas: Padding must not be negative")
	}
	if opts.Extrude < 0 {
		return nil, nil, errors.New("imaging: atlas: Extrude must not be negative")
	}
	if len(frames) == 0 {
		return nil, nil, errors.New("imaging: atlas: no frames to pack")
	}

	type prepared struct {
		name    string
		src     *image.NRGBA // zero-origin copy of the source image
		frame   image.Rectangle
		bounds  image.Rectangle // content bounds in original coordinates
		core    image.Rectangle // content bounds in zero-origin src coordinates
		metaIdx int             // position in the returned metadata slice
	}

	preparedFrames := make([]prepared, len(frames))
	names := make(map[string]struct{}, len(frames))

	for i, nf := range frames {
		if nf.Name == "" {
			return nil, nil, fmt.Errorf("imaging: atlas: frame at index %d has an empty name", i)
		}
		if _, ok := names[nf.Name]; ok {
			return nil, nil, fmt.Errorf("imaging: atlas: duplicate frame name %q", nf.Name)
		}
		names[nf.Name] = struct{}{}

		if nf.Image == nil {
			return nil, nil, fmt.Errorf("imaging: atlas: frame %q has a nil image", nf.Name)
		}
		origBounds := nf.Image.Bounds()
		if origBounds.Empty() {
			return nil, nil, fmt.Errorf("imaging: atlas: frame %q has empty bounds", nf.Name)
		}

		// Normalize to a zero-origin NRGBA image so pixel access and
		// trimming do not depend on the source Bounds Min.
		src := toNRGBA(nf.Image)

		core := image.Rect(0, 0, origBounds.Dx(), origBounds.Dy())
		if opts.Trim {
			core = transparentBounds(src)
			if core.Empty() {
				return nil, nil, fmt.Errorf("imaging: atlas: frame %q is fully transparent", nf.Name)
			}
		}

		// Guard against integer overflow when computing the cell size.
		if opts.Extrude > (int(^uint(0)>>1)-core.Dx())/2 ||
			opts.Extrude > (int(^uint(0)>>1)-core.Dy())/2 {
			return nil, nil, fmt.Errorf("imaging: atlas: frame %q cell size overflows", nf.Name)
		}

		preparedFrames[i] = prepared{
			name:    nf.Name,
			src:     src,
			frame:   origBounds,
			bounds:  core.Add(origBounds.Min),
			core:    core,
			metaIdx: i,
		}
	}

	// Deterministic shelf packing: sort by decreasing cell height,
	// then decreasing width, then by input index. Ties never depend on
	// map iteration or pointer values.
	order := make([]int, len(preparedFrames))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := &preparedFrames[order[a]], &preparedFrames[order[b]]
		ha := pa.core.Dy() + 2*opts.Extrude
		hb := pb.core.Dy() + 2*opts.Extrude
		if ha != hb {
			return ha > hb
		}
		wa := pa.core.Dx() + 2*opts.Extrude
		wb := pb.core.Dx() + 2*opts.Extrude
		if wa != wb {
			return wa > wb
		}
		return pa.metaIdx < pb.metaIdx
	})

	// First pass: compute shelf layout without touching pixel memory.
	type placement struct {
		cell image.Rectangle // full cell rect including extrusion, atlas coordinates
	}
	placements := make([]placement, len(preparedFrames))

	x, y := 0, 0
	shelfHeight := 0
	atlasWidth := 0
	for _, idx := range order {
		pf := &preparedFrames[idx]
		cellW := pf.core.Dx() + 2*opts.Extrude
		cellH := pf.core.Dy() + 2*opts.Extrude
		if cellW > opts.MaxWidth {
			return nil, nil, fmt.Errorf("imaging: atlas: frame %q of width %d does not fit in atlas width %d", pf.name, cellW, opts.MaxWidth)
		}

		if x > 0 && x+cellW > opts.MaxWidth {
			// Start a new shelf.
			x = 0
			y += shelfHeight + opts.Padding
			shelfHeight = 0
		}
		if y+cellH > opts.MaxHeight {
			return nil, nil, fmt.Errorf("imaging: atlas: frame %q does not fit: atlas height limit %d exceeded", pf.name, opts.MaxHeight)
		}

		cell := image.Rect(x, y, x+cellW, y+cellH)
		placements[idx] = placement{cell: cell}
		if r := cell.Max.X; r > atlasWidth {
			atlasWidth = r
		}
		if cellH > shelfHeight {
			shelfHeight = cellH
		}
		x += cellW + opts.Padding
	}
	atlasHeight := y + shelfHeight
	if atlasWidth <= 0 || atlasHeight <= 0 || atlasWidth > opts.MaxWidth || atlasHeight > opts.MaxHeight {
		return nil, nil, errors.New("imaging: atlas: packed frames exceed the atlas size limit")
	}

	// Second pass: allocate the atlas and blit the frames. Everything
	// above was validation and pure layout, so reaching this point
	// means all frames fit and the output is guaranteed complete.
	atlas := image.NewNRGBA(image.Rect(0, 0, atlasWidth, atlasHeight))
	meta := make([]AtlasFrame, len(preparedFrames))

	for i := range preparedFrames {
		pf := &preparedFrames[i]
		cell := placements[i].cell
		coreRect := cell.Inset(opts.Extrude)

		blitFrame(atlas, coreRect, pf.src, pf.core, opts.Extrude)

		meta[pf.metaIdx] = AtlasFrame{
			Name:   pf.name,
			Frame:  pf.frame,
			Bounds: pf.bounds,
			Atlas:  coreRect,
		}
	}

	return atlas, meta, nil
}

// transparentBounds returns the zero-origin bounding rectangle of the
// pixels with a non-zero alpha in src. It returns an empty rectangle
// when the image is fully transparent.
func transparentBounds(src *image.NRGBA) image.Rectangle {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	minX, minY := w, h
	maxX, maxY := -1, -1
	for y := 0; y < h; y++ {
		row := y * src.Stride
		for x := 0; x < w; x++ {
			if src.Pix[row+x*4+3] != 0 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

// blitFrame copies the core sub-rectangle of src into dst at coreRect
// (atlas coordinates) and repeats the edge pixels outwards by extrude
// pixels on every side. NRGBA bytes are copied unchanged, preserving
// straight (non-premultiplied) alpha and semi-transparent edge colors.
func blitFrame(dst *image.NRGBA, coreRect image.Rectangle, src *image.NRGBA, srcCore image.Rectangle, extrude int) {
	coreW, coreH := srcCore.Dx(), srcCore.Dy()

	// Core content: straight row copies between the two NRGBA buffers.
	for dy := 0; dy < coreH; dy++ {
		dstOff := (coreRect.Min.Y+dy)*dst.Stride + coreRect.Min.X*4
		srcOff := (srcCore.Min.Y+dy)*src.Stride + srcCore.Min.X*4
		copy(dst.Pix[dstOff:dstOff+coreW*4], src.Pix[srcOff:srcOff+coreW*4])
	}

	if extrude == 0 {
		return
	}

	// Helper reading a core pixel by clamped core-local coordinates.
	corePixel := func(cx, cy int) []uint8 {
		if cx < 0 {
			cx = 0
		} else if cx >= coreW {
			cx = coreW - 1
		}
		if cy < 0 {
			cy = 0
		} else if cy >= coreH {
			cy = coreH - 1
		}
		off := (srcCore.Min.Y+cy)*src.Stride + (srcCore.Min.X+cx)*4
		return src.Pix[off : off+4 : off+4]
	}
	putPixel := func(ax, ay int, px []uint8) {
		off := ay*dst.Stride + ax*4
		d := dst.Pix[off : off+4 : off+4]
		d[0], d[1], d[2], d[3] = px[0], px[1], px[2], px[3]
	}

	left := coreRect.Min.X - extrude
	right := coreRect.Max.X + extrude
	top := coreRect.Min.Y - extrude
	bottom := coreRect.Max.Y + extrude

	// Top and bottom bands, spanning the full width including the
	// corner zones: edge rows/columns are clamped to the core border.
	for ay := top; ay < bottom; ay++ {
		// cy relative to the core: negative above, >= coreH below.
		cy := ay - coreRect.Min.Y
		if ay >= coreRect.Min.Y && ay < coreRect.Max.Y {
			continue // core rows handled below via side bands only
		}
		for ax := left; ax < right; ax++ {
			cx := ax - coreRect.Min.X
			putPixel(ax, ay, corePixel(cx, cy))
		}
	}
	// Side bands next to the core rows (corners already filled above).
	for ay := coreRect.Min.Y; ay < coreRect.Max.Y; ay++ {
		cy := ay - coreRect.Min.Y
		for ax := left; ax < coreRect.Min.X; ax++ {
			putPixel(ax, ay, corePixel(ax-coreRect.Min.X, cy))
		}
		for ax := coreRect.Max.X; ax < right; ax++ {
			putPixel(ax, ay, corePixel(ax-coreRect.Min.X, cy))
		}
	}
}
