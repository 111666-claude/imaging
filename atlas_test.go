package imaging

import (
	"image"
	"image/color"
	"testing"
)

// makeFrame builds a w*h zero-origin NRGBA image filled with fill and
// paints an opaque marker at marker (or none when marker is negative).
func makeFrame(w, h int, fill color.NRGBA, marker int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := y*img.Stride + x*4
			img.Pix[off+0], img.Pix[off+1], img.Pix[off+2], img.Pix[off+3] = fill.R, fill.G, fill.B, fill.A
		}
	}
	if marker >= 0 {
		off := marker * 4
		img.Pix[off+0], img.Pix[off+1], img.Pix[off+2], img.Pix[off+3] = 0x01, 0x02, 0x03, 0xff
	}
	return img
}

func pixelAt(t *testing.T, img *image.NRGBA, x, y int) color.NRGBA {
	t.Helper()
	if !image.Pt(x, y).In(img.Rect) {
		t.Fatalf("point (%d,%d) outside atlas %v", x, y, img.Rect)
	}
	off := y*img.Stride + x*4
	return color.NRGBA{
		R: img.Pix[off],
		G: img.Pix[off+1],
		B: img.Pix[off+2],
		A: img.Pix[off+3],
	}
}

func TestAtlasBasicLayoutAndMetadata(t *testing.T) {
	// Two frames of different sizes, opaque everywhere, no trim.
	red := color.NRGBA{0xff, 0, 0, 0xff}
	blue := color.NRGBA{0, 0, 0xff, 0xff}
	frames := []NamedImage{
		{Name: "idle", Image: makeFrame(4, 2, red, -1)},
		{Name: "run", Image: makeFrame(3, 5, blue, -1)},
	}
	atlas, meta, err := Atlas(frames, AtlasOptions{MaxWidth: 64, MaxHeight: 64, Padding: 2, Extrude: 1})
	if err != nil {
		t.Fatalf("Atlas returned error: %v", err)
	}

	// Cell sizes: 4x2 -> 6x4, 3x5 -> 5x7. Sorted by height desc:
	// "run" first, then "idle".
	if meta[0].Name != "idle" || meta[1].Name != "run" {
		t.Fatalf("metadata must keep input order, got %q, %q", meta[0].Name, meta[1].Name)
	}

	runCell := image.Rect(0, 0, 5, 7)
	idleCell := image.Rect(7, 0, 13, 4) // 5 + padding 2 = 7
	if meta[1].Atlas != runCell.Inset(1) {
		t.Fatalf("run atlas rect = %v, want %v", meta[1].Atlas, runCell.Inset(1))
	}
	if meta[0].Atlas != idleCell.Inset(1) {
		t.Fatalf("idle atlas rect = %v, want %v", meta[0].Atlas, idleCell.Inset(1))
	}

	// Original sizes preserved in metadata.
	if meta[0].Frame != image.Rect(0, 0, 4, 2) || meta[0].Bounds != meta[0].Frame {
		t.Fatalf("idle frame/bounds mismatch: %v %v", meta[0].Frame, meta[0].Bounds)
	}
	if meta[1].Frame != image.Rect(0, 0, 3, 5) || meta[1].Bounds != meta[1].Frame {
		t.Fatalf("run frame/bounds mismatch: %v %v", meta[1].Frame, meta[1].Bounds)
	}

	// Atlas tightly bounds both cells.
	if atlas.Rect != image.Rect(0, 0, 13, 7) {
		t.Fatalf("atlas rect = %v, want (0,0)-(13,7)", atlas.Rect)
	}

	// No overlap between cells (including extrusion) and padding gap
	// stays transparent.
	if c := pixelAt(t, atlas, 5, 0); c.A != 0 {
		t.Fatalf("padding pixel must be transparent, got %v", c)
	}
	if c := pixelAt(t, atlas, 6, 3); c.A != 0 {
		t.Fatalf("padding pixel must be transparent, got %v", c)
	}

	// Core content and extrusion colors.
	if c := pixelAt(t, atlas, 0, 0); c != blue {
		t.Fatalf("extruded top-left of run = %v, want blue", c)
	}
	if c := pixelAt(t, atlas, 4, 6); c != blue {
		t.Fatalf("extruded bottom-right of run = %v, want blue", c)
	}
	if c := pixelAt(t, atlas, 12, 3); c != red {
		t.Fatalf("extruded right edge of idle = %v, want red", c)
	}
	if c := pixelAt(t, atlas, 8, 1); c != red {
		t.Fatalf("idle core = %v, want red", c)
	}
}

func TestAtlasDeterministic(t *testing.T) {
	frames := []NamedImage{
		{Name: "a", Image: makeFrame(7, 3, color.NRGBA{1, 0, 0, 255}, -1)},
		{Name: "b", Image: makeFrame(2, 9, color.NRGBA{0, 2, 0, 255}, -1)},
		{Name: "c", Image: makeFrame(5, 5, color.NRGBA{0, 0, 3, 255}, -1)},
		{Name: "d", Image: makeFrame(5, 5, color.NRGBA{4, 4, 4, 255}, -1)},
	}
	opts := AtlasOptions{MaxWidth: 32, MaxHeight: 32, Padding: 1, Extrude: 2, Trim: true}

	atlas1, meta1, err := Atlas(frames, opts)
	if err != nil {
		t.Fatalf("first Atlas call failed: %v", err)
	}
	// Shuffled-but-equal input must not change the result; same names
	// and same pixel data, different slice order.
	shuffled := []NamedImage{frames[2], frames[0], frames[3], frames[1]}
	atlas2, meta2, err := Atlas(shuffled, opts)
	if err != nil {
		t.Fatalf("second Atlas call failed: %v", err)
	}

	if !atlas1.Rect.Eq(atlas2.Rect) {
		t.Fatalf("atlas size differs: %v vs %v", atlas1.Rect, atlas2.Rect)
	}
	if len(atlas1.Pix) != len(atlas2.Pix) {
		t.Fatalf("atlas pixel buffer lengths differ")
	}
	for i := range atlas1.Pix {
		if atlas1.Pix[i] != atlas2.Pix[i] {
			t.Fatalf("atlas pixels differ at byte %d", i)
		}
	}
	byName := map[string]AtlasFrame{}
	for _, m := range meta2 {
		byName[m.Name] = m
	}
	for _, m1 := range meta1 {
		m2 := byName[m1.Name]
		if m1 != m2 {
			t.Fatalf("metadata for %q differs: %v vs %v", m1.Name, m1, m2)
		}
	}

	// Repeated call with identical input yields identical rectangles.
	atlas3, meta3, err := Atlas(frames, opts)
	if err != nil {
		t.Fatalf("third Atlas call failed: %v", err)
	}
	if !atlas3.Rect.Eq(atlas1.Rect) {
		t.Fatalf("repeat call produced different size")
	}
	for i := range meta1 {
		if meta1[i] != meta3[i] {
			t.Fatalf("repeat call metadata differs at %d: %v vs %v", i, meta1[i], meta3[i])
		}
	}
}

func TestAtlasTrimAndReconstruction(t *testing.T) {
	// 5x4 frame: transparent border 1px, opaque 3x2 interior, with a
	// semi-transparent pixel carrying a distinct straight-alpha color.
	src := image.NewNRGBA(image.Rect(0, 0, 5, 4))
	semi := color.NRGBA{0x80, 0x40, 0x20, 0x60}
	opaque := color.NRGBA{0xaa, 0xbb, 0xcc, 0xff}
	for y := 1; y <= 2; y++ {
		for x := 1; x <= 3; x++ {
			off := y*src.Stride + x*4
			c := opaque
			if x == 2 && y == 2 {
				c = semi
			}
			src.Pix[off+0], src.Pix[off+1], src.Pix[off+2], src.Pix[off+3] = c.R, c.G, c.B, c.A
		}
	}

	atlas, meta, err := Atlas(
		[]NamedImage{{Name: "sprite", Image: src}},
		AtlasOptions{MaxWidth: 32, MaxHeight: 32, Padding: 0, Extrude: 1, Trim: true},
	)
	if err != nil {
		t.Fatalf("Atlas failed: %v", err)
	}
	m := meta[0]

	wantBounds := image.Rect(1, 1, 4, 3)
	if m.Frame != src.Rect {
		t.Fatalf("Frame = %v, want %v", m.Frame, src.Rect)
	}
	if m.Bounds != wantBounds {
		t.Fatalf("Bounds = %v, want %v", m.Bounds, wantBounds)
	}
	// Core 3x2 plus 1px extrusion on every side: 5x4 cell, core at
	// (1,1)-(4,3).
	if m.Atlas != image.Rect(1, 1, 4, 3) {
		t.Fatalf("Atlas = %v, want (1,1)-(4,3)", m.Atlas)
	}
	if atlas.Rect != image.Rect(0, 0, 5, 4) {
		t.Fatalf("atlas rect = %v", atlas.Rect)
	}

	// Semi-transparent edge color preserved exactly (no premultiplied
	// blending, no color bleeding).
	got := pixelAt(t, atlas, 2, 2)
	if got != semi {
		t.Fatalf("semi-transparent pixel = %v, want %v", got, semi)
	}
	// Extrusion repeats the edge pixel colors.
	if c := pixelAt(t, atlas, 0, 0); c != opaque {
		t.Fatalf("corner extrusion = %v, want %v", c, opaque)
	}
	if c := pixelAt(t, atlas, 2, 0); c != opaque {
		t.Fatalf("top extrusion = %v, want %v", c, opaque)
	}
	if c := pixelAt(t, atlas, 4, 3); c != opaque {
		t.Fatalf("bottom-right extrusion = %v, want %v", c, opaque)
	}

	// Reconstruct the original image purely from metadata.
	recon := reconstructFrame(t, atlas, m)
	if recon.Rect != src.Rect {
		t.Fatalf("reconstructed rect = %v, want %v", recon.Rect, src.Rect)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 5; x++ {
			off := y*recon.Stride + x*4
			want := color.NRGBA{
				src.Pix[off], src.Pix[off+1], src.Pix[off+2], src.Pix[off+3],
			}
			got := color.NRGBA{
				recon.Pix[off], recon.Pix[off+1], recon.Pix[off+2], recon.Pix[off+3],
			}
			if got != want {
				t.Fatalf("reconstructed pixel (%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
}

// reconstructFrame rebuilds the original zero-origin frame from the
// atlas image and its metadata, honoring trim offsets.
func reconstructFrame(t *testing.T, atlas *image.NRGBA, m AtlasFrame) *image.NRGBA {
	t.Helper()
	out := image.NewNRGBA(image.Rect(0, 0, m.Frame.Dx(), m.Frame.Dy()))
	offset := m.Bounds.Min.Sub(m.Frame.Min)
	for y := 0; y < m.Bounds.Dy(); y++ {
		for x := 0; x < m.Bounds.Dx(); x++ {
			srcOff := (m.Atlas.Min.Y+y)*atlas.Stride + (m.Atlas.Min.X+x)*4
			dstOff := (offset.Y+y)*out.Stride + (offset.X+x)*4
			copy(out.Pix[dstOff:dstOff+4], atlas.Pix[srcOff:srcOff+4])
		}
	}
	return out
}

func TestAtlasNonZeroBounds(t *testing.T) {
	// Source image whose Bounds Min is not at the origin.
	src := image.NewNRGBA(image.Rect(10, 20, 13, 23))
	for i := range src.Pix {
		src.Pix[i] = 0
	}
	green := color.NRGBA{0, 0xff, 0, 0xff}
	for y := 0; y < src.Rect.Dy(); y++ {
		for x := 0; x < src.Rect.Dx(); x++ {
			off := y*src.Stride + x*4
			src.Pix[off+0], src.Pix[off+1], src.Pix[off+2], src.Pix[off+3] = green.R, green.G, green.B, green.A
		}
	}
	atlas, meta, err := Atlas(
		[]NamedImage{{Name: "offset", Image: src}},
		AtlasOptions{MaxWidth: 16, MaxHeight: 16, Extrude: 0},
	)
	if err != nil {
		t.Fatalf("Atlas failed: %v", err)
	}
	if meta[0].Frame != src.Rect {
		t.Fatalf("Frame = %v, want %v", meta[0].Frame, src.Rect)
	}
	if meta[0].Bounds != src.Rect {
		t.Fatalf("Bounds = %v, want %v", meta[0].Bounds, src.Rect)
	}
	if meta[0].Atlas != image.Rect(0, 0, 3, 3) {
		t.Fatalf("Atlas = %v, want (0,0)-(3,3)", meta[0].Atlas)
	}
	if c := pixelAt(t, atlas, 2, 2); c != green {
		t.Fatalf("atlas pixel = %v, want green", c)
	}
}

func TestAtlasNoOverlapAcrossShelves(t *testing.T) {
	// Narrow width forces shelf wrapping with frames of varied sizes.
	frames := []NamedImage{
		{Name: "a", Image: makeFrame(4, 4, color.NRGBA{1, 1, 1, 255}, -1)},
		{Name: "b", Image: makeFrame(4, 2, color.NRGBA{2, 2, 2, 255}, -1)},
		{Name: "c", Image: makeFrame(2, 6, color.NRGBA{3, 3, 3, 255}, -1)},
		{Name: "d", Image: makeFrame(3, 3, color.NRGBA{4, 4, 4, 255}, -1)},
	}
	opts := AtlasOptions{MaxWidth: 8, MaxHeight: 64, Padding: 1, Extrude: 0}
	atlas, meta, err := Atlas(frames, opts)
	if err != nil {
		t.Fatalf("Atlas failed: %v", err)
	}
	if atlas.Rect.Max.X > opts.MaxWidth || atlas.Rect.Max.Y > opts.MaxHeight {
		t.Fatalf("atlas %v exceeds limits %dx%d", atlas.Rect, opts.MaxWidth, opts.MaxHeight)
	}

	// Every cell must be disjoint and every reported pixel must match
	// the source frame color.
	seen := map[string]bool{}
	for i, mi := range meta {
		if seen[mi.Name] {
			t.Fatalf("duplicate metadata name %q", mi.Name)
		}
		seen[mi.Name] = true
		for j, mj := range meta {
			if i != j && mi.Atlas.Overlaps(mj.Atlas) {
				t.Fatalf("frames %q and %q overlap: %v vs %v", mi.Name, mj.Name, mi.Atlas, mj.Atlas)
			}
		}
		src := frames[i].Image.(*image.NRGBA)
		off := mi.Bounds.Min.Sub(mi.Frame.Min)
		for y := 0; y < mi.Bounds.Dy(); y++ {
			for x := 0; x < mi.Bounds.Dx(); x++ {
				so := (off.Y+y)*src.Stride + (off.X+x)*4
				do := (mi.Atlas.Min.Y+y)*atlas.Stride + (mi.Atlas.Min.X+x)*4
				for k := 0; k < 4; k++ {
					if atlas.Pix[do+k] != src.Pix[so+k] {
						t.Fatalf("frame %q pixel mismatch at (%d,%d) channel %d", mi.Name, x, y, k)
					}
				}
			}
		}
	}
}

func TestAtlasErrors(t *testing.T) {
	ok := []NamedImage{{Name: "a", Image: makeFrame(2, 2, color.NRGBA{1, 2, 3, 255}, -1)}}

	testCases := []struct {
		name   string
		frames []NamedImage
		opts   AtlasOptions
	}{
		{"zero max width", ok, AtlasOptions{MaxWidth: 0, MaxHeight: 8}},
		{"negative max height", ok, AtlasOptions{MaxWidth: 8, MaxHeight: -1}},
		{"negative padding", ok, AtlasOptions{MaxWidth: 8, MaxHeight: 8, Padding: -1}},
		{"negative extrude", ok, AtlasOptions{MaxWidth: 8, MaxHeight: 8, Extrude: -2}},
		{"no frames", nil, AtlasOptions{MaxWidth: 8, MaxHeight: 8}},
		{"empty name", []NamedImage{{Name: "", Image: makeFrame(1, 1, color.NRGBA{1, 2, 3, 255}, -1)}}, AtlasOptions{MaxWidth: 8, MaxHeight: 8}},
		{
			"duplicate names",
			[]NamedImage{
				{Name: "x", Image: makeFrame(1, 1, color.NRGBA{1, 2, 3, 255}, -1)},
				{Name: "x", Image: makeFrame(1, 1, color.NRGBA{4, 5, 6, 255}, -1)},
			},
			AtlasOptions{MaxWidth: 8, MaxHeight: 8},
		},
		{"nil image", []NamedImage{{Name: "nil", Image: nil}}, AtlasOptions{MaxWidth: 8, MaxHeight: 8}},
		{
			"empty bounds",
			[]NamedImage{{Name: "empty", Image: image.NewNRGBA(image.Rect(0, 0, 0, 0))}},
			AtlasOptions{MaxWidth: 8, MaxHeight: 8},
		},
		{
			"fully transparent with trim",
			[]NamedImage{{Name: "ghost", Image: image.NewNRGBA(image.Rect(0, 0, 3, 3))}},
			AtlasOptions{MaxWidth: 8, MaxHeight: 8, Trim: true},
		},
		{
			"frame wider than limit",
			[]NamedImage{{Name: "wide", Image: makeFrame(10, 1, color.NRGBA{1, 2, 3, 255}, -1)}},
			AtlasOptions{MaxWidth: 8, MaxHeight: 8},
		},
		{
			"cannot pack all frames",
			[]NamedImage{
				{Name: "a", Image: makeFrame(4, 4, color.NRGBA{1, 1, 1, 255}, -1)},
				{Name: "b", Image: makeFrame(4, 4, color.NRGBA{2, 2, 2, 255}, -1)},
				{Name: "c", Image: makeFrame(4, 4, color.NRGBA{3, 3, 3, 255}, -1)},
			},
			AtlasOptions{MaxWidth: 4, MaxHeight: 7, Padding: 1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			atlas, meta, err := Atlas(tc.frames, tc.opts)
			if err == nil {
				t.Fatalf("expected error, got atlas %v", atlas.Rect)
			}
			if atlas != nil || meta != nil {
				t.Fatalf("error path must not return partial results, got atlas=%v meta=%v", atlas, meta)
			}
		})
	}
}

func TestAtlasFullyTransparentKeptWithoutTrim(t *testing.T) {
	blank := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	atlas, meta, err := Atlas(
		[]NamedImage{{Name: "blank", Image: blank}},
		AtlasOptions{MaxWidth: 16, MaxHeight: 16, Extrude: 1},
	)
	if err != nil {
		t.Fatalf("Atlas failed: %v", err)
	}
	// No trim: full 3x3 frame kept, cell is 5x5 with extrusion.
	if meta[0].Bounds != image.Rect(0, 0, 3, 3) || meta[0].Atlas != image.Rect(1, 1, 4, 4) {
		t.Fatalf("unexpected metadata: %+v", meta[0])
	}
	if atlas.Rect != image.Rect(0, 0, 5, 5) {
		t.Fatalf("atlas rect = %v", atlas.Rect)
	}
	for i := range atlas.Pix {
		if atlas.Pix[i] != 0 {
			t.Fatalf("blank frame atlas must be fully transparent, byte %d = %d", i, atlas.Pix[i])
		}
	}
}

func TestAtlasMultiSizePixelRoundTrip(t *testing.T) {
	// Mixed sizes incl. 1x1 frames; verify every pixel round-trips and
	// extrusion zones contain only clamped edge pixels.
	sizes := [][2]int{{1, 1}, {1, 7}, {7, 1}, {3, 3}, {5, 2}, {2, 5}, {4, 4}}
	frames := make([]NamedImage, len(sizes))
	for i, sz := range sizes {
		fill := color.NRGBA{uint8(10 + i), uint8(20 + i), uint8(30 + i), uint8(128 + i)}
		frames[i] = NamedImage{
			Name:  string(rune('a' + i)),
			Image: makeFrame(sz[0], sz[1], fill, -1),
		}
	}
	opts := AtlasOptions{MaxWidth: 24, MaxHeight: 24, Padding: 1, Extrude: 1, Trim: true}
	atlas, meta, err := Atlas(frames, opts)
	if err != nil {
		t.Fatalf("Atlas failed: %v", err)
	}

	for i, m := range meta {
		src := frames[i].Image.(*image.NRGBA)
		recon := reconstructFrame(t, atlas, m)
		for y := 0; y < src.Rect.Dy(); y++ {
			for x := 0; x < src.Rect.Dx(); x++ {
				so := y*src.Stride + x*4
				ro := y*recon.Stride + x*4
				if src.Pix[so : so+4 : so+4][0] != recon.Pix[ro] ||
					src.Pix[so+1] != recon.Pix[ro+1] ||
					src.Pix[so+2] != recon.Pix[ro+2] ||
					src.Pix[so+3] != recon.Pix[ro+3] {
					t.Fatalf("frame %q pixel (%d,%d) round-trip mismatch", m.Name, x, y)
				}
			}
		}

		// Every pixel in the extrusion zone equals the nearest core
		// edge pixel.
		fill := color.NRGBA{uint8(10 + i), uint8(20 + i), uint8(30 + i), uint8(128 + i)}
		cell := m.Atlas.Inset(-opts.Extrude)
		for ay := cell.Min.Y; ay < cell.Max.Y; ay++ {
			for ax := cell.Min.X; ax < cell.Max.X; ax++ {
				if image.Pt(ax, ay).In(m.Atlas) {
					continue
				}
				if c := pixelAt(t, atlas, ax, ay); c != fill {
					t.Fatalf("frame %q extrusion pixel (%d,%d) = %v, want %v", m.Name, ax, ay, c, fill)
				}
			}
		}
	}
}
