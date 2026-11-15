package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"testing"
)

// makeScene builds an NRGBA image with transparent vertical edges, a flat
// gray background and a high-contrast red/yellow checkerboard subject
// in the center. The checkerboard gives the subject a high internal
// energy so seams never run through it while any background remains.
func makeScene(rect image.Rectangle) *image.NRGBA {
	img := image.NewNRGBA(rect)
	w := rect.Dx()
	h := rect.Dy()
	edge := w / 8      // transparent edge width
	subj0 := w/2 - w/8 // subject start (inclusive)
	subj1 := w/2 + w/8 // subject end (exclusive)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(rect.Min.X+x, rect.Min.Y+y)
			switch {
			case x < edge || x >= w-edge:
				// Fully transparent edge.
			case x >= subj0 && x < subj1:
				if (x+y)%2 == 0 {
					img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0xff, 0x00, 0x00, 0xff
				} else {
					img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0xff, 0xff, 0x00, 0xff
				}
			default:
				img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0x80, 0x80, 0x80, 0xff
			}
		}
	}
	return img
}

func countPixels(img *image.NRGBA, pred func(r, g, b, a uint8) bool) int {
	n := 0
	for i := 0; i+4 <= len(img.Pix); i += 4 {
		if pred(img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]) {
			n++
		}
	}
	return n
}

// isSubject matches both checkerboard colors of the subject.
func isSubject(r, g, b, a uint8) bool { return r == 0xff && b == 0 && a == 0xff }

func TestSeamCarveDimensions(t *testing.T) {
	src := makeScene(image.Rect(0, 0, 20, 12))
	testCases := []struct {
		name  string
		w, h  int
		wantW int
		wantH int
	}{
		{"width only", 14, 0, 14, 12},
		{"height only", 0, 7, 20, 7},
		{"both", 14, 7, 14, 7},
		{"keep both", 0, 0, 20, 12},
		{"same size", 20, 12, 20, 12},
		{"to 1x1", 1, 1, 1, 1},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dst, err := SeamCarve(src, tc.w, tc.h, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := dst.Bounds(); got != image.Rect(0, 0, tc.wantW, tc.wantH) {
				t.Fatalf("got bounds %v, want (0,0,%d,%d)", got, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestSeamCarveDeterministic(t *testing.T) {
	src := makeScene(image.Rect(0, 0, 24, 16))
	a, err := SeamCarve(src, 15, 9, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := SeamCarve(src, 15, 9, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("same input produced different results")
	}
}

func TestSeamCarveSubjectPreserved(t *testing.T) {
	// 24x16 scene: 3 transparent columns per side, red subject 6 columns wide.
	src := makeScene(image.Rect(0, 0, 24, 16))
	redBefore := countPixels(src, isSubject)
	if redBefore != 6*16 {
		t.Fatalf("bad test setup: got %d red pixels, want %d", redBefore, 6*16)
	}

	// Shrink width without a mask: seams must run through the transparent
	// edges and flat background, never through the high-contrast subject.
	dst, err := SeamCarve(src, 15, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := countPixels(dst, isSubject); got != redBefore {
		t.Fatalf("width carve lost subject pixels: got %d red, want %d", got, redBefore)
	}

	// Same for height using a transposed scene (subject is a horizontal band).
	srcT := Transpose(makeScene(image.Rect(0, 0, 16, 24)))
	redBeforeT := countPixels(srcT, isSubject)
	dstT, err := SeamCarve(srcT, 0, 12, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := countPixels(dstT, isSubject); got != redBeforeT {
		t.Fatalf("height carve lost subject pixels: got %d red, want %d", got, redBeforeT)
	}

	// Shrink both dimensions at once with the subject core protected by a
	// mask. Protected pixels must never be removed while a feasible seam
	// exists, so all 6*8 protected red pixels survive.
	mask := image.NewAlpha(image.Rect(0, 0, 24, 16))
	for y := 4; y < 12; y++ {
		for x := 9; x < 15; x++ {
			mask.SetAlpha(x, y, color.Alpha{A: 0xff})
		}
	}
	dstBoth, err := SeamCarve(src, 18, 13, mask)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dstBoth.Bounds() != image.Rect(0, 0, 18, 13) {
		t.Fatalf("got bounds %v, want (0,0,18,13)", dstBoth.Bounds())
	}
	if got := countPixels(dstBoth, isSubject); got < 6*8 {
		t.Fatalf("masked carve lost protected pixels: got %d red, want at least %d", got, 6*8)
	}
}

func TestSeamCarveTransparentEdgesRemovedFirst(t *testing.T) {
	// 8x6: four transparent columns on the left, high-frequency opaque
	// pattern on the right so any opaque column has higher energy.
	src := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 4; x < 8; x++ {
			v := uint8(0)
			if x%2 == 0 {
				v = 0xff
			}
			i := src.PixOffset(x, y)
			src.Pix[i+0], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = v, v, v, 0xff
		}
	}
	transparent := func(r, g, b, a uint8) bool { return a == 0 }
	if got := countPixels(src, transparent); got != 24 {
		t.Fatalf("bad test setup: got %d transparent pixels, want 24", got)
	}
	dst, err := SeamCarve(src, 6, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := countPixels(dst, transparent); got != 12 {
		t.Fatalf("seams did not prefer transparent pixels: got %d transparent, want 12", got)
	}
	opaque := func(r, g, b, a uint8) bool { return a == 0xff }
	if got := countPixels(dst, opaque); got != 24 {
		t.Fatalf("opaque pixels were carved: got %d opaque, want 24", got)
	}
}

func TestSeamCarveMaskProtection(t *testing.T) {
	// Columns with distinct gray values 31..38. Without a mask the lowest
	// energy seam is the leftmost edge column; protecting column 0 must
	// force the seam elsewhere.
	src := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			i := src.PixOffset(x, y)
			v := uint8(31 + x)
			src.Pix[i+0], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = v, v, v, 0xff
		}
	}

	mask := image.NewAlpha(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		mask.SetAlpha(0, y, color.Alpha{A: 0xff})
	}
	dst, err := SeamCarve(src, 7, 0, mask)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := dst.Pix[dst.PixOffset(0, 0)]; got != 31 {
		t.Fatalf("protected column 0 was carved: got value %d at (0,0), want 31", got)
	}

	// Without the mask the leftmost column is removed instead.
	dstNoMask, err := SeamCarve(src, 7, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := dstNoMask.Pix[dstNoMask.PixOffset(0, 0)]; got != 32 {
		t.Fatalf("unprotected carve kept column 0: got value %d at (0,0), want 32", got)
	}
}

func TestSeamCarveNoFeasibleSeam(t *testing.T) {
	src := makeScene(image.Rect(0, 0, 12, 8))

	// Fully protected image: no vertical seam can avoid the mask.
	full := image.NewAlpha(image.Rect(0, 0, 12, 8))
	for i := range full.Pix {
		full.Pix[i] = 0xff
	}
	if _, err := SeamCarve(src, 10, 0, full); !errors.Is(err, ErrSeamCarveNoSeam) {
		t.Fatalf("got %v, want ErrSeamCarveNoSeam", err)
	}

	// A protected row spanning the whole width blocks every vertical seam
	// (each vertical seam contains exactly one pixel of that row).
	row := image.NewAlpha(image.Rect(0, 0, 12, 8))
	for x := 0; x < 12; x++ {
		row.SetAlpha(x, 4, color.Alpha{A: 0xff})
	}
	if _, err := SeamCarve(src, 10, 0, row); !errors.Is(err, ErrSeamCarveNoSeam) {
		t.Fatalf("got %v, want ErrSeamCarveNoSeam", err)
	}

	// A protected column spanning the whole height blocks every horizontal seam.
	col := image.NewAlpha(image.Rect(0, 0, 12, 8))
	for y := 0; y < 8; y++ {
		col.SetAlpha(4, y, color.Alpha{A: 0xff})
	}
	if _, err := SeamCarve(src, 0, 6, col); !errors.Is(err, ErrSeamCarveNoSeam) {
		t.Fatalf("got %v, want ErrSeamCarveNoSeam", err)
	}
}

func TestSeamCarveErrors(t *testing.T) {
	src := makeScene(image.Rect(0, 0, 12, 8))
	maskOK := image.NewAlpha(image.Rect(0, 0, 12, 8))

	testCases := []struct {
		name string
		img  image.Image
		w, h int
		mask image.Image
		want error
	}{
		{"enlarge width", src, 13, 0, nil, ErrSeamCarveInvalidSize},
		{"enlarge height", src, 0, 9, nil, ErrSeamCarveInvalidSize},
		{"enlarge both", src, 20, 20, nil, ErrSeamCarveInvalidSize},
		{"negative width", src, -1, 0, nil, ErrSeamCarveInvalidSize},
		{"negative height", src, 0, -5, nil, ErrSeamCarveInvalidSize},
		{"empty image", image.NewNRGBA(image.Rectangle{}), 1, 1, nil, ErrSeamCarveInvalidSize},
		{"mask too narrow", src, 10, 0, image.NewAlpha(image.Rect(0, 0, 11, 8)), ErrSeamCarveMaskSize},
		{"mask too tall", src, 10, 0, image.NewAlpha(image.Rect(0, 0, 12, 9)), ErrSeamCarveMaskSize},
		{"mask valid", src, 10, 0, maskOK, nil},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SeamCarve(tc.img, tc.w, tc.h, tc.mask)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got error %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSeamCarveNonZeroOrigin(t *testing.T) {
	// Source and mask both live at non-zero origins; alignment is by
	// relative offset from each image's top-left corner.
	src := makeScene(image.Rect(3, 5, 27, 21))

	mask := image.NewAlpha(image.Rect(100, 200, 124, 216))
	for y := 4; y < 12; y++ {
		for x := 9; x < 15; x++ {
			mask.SetAlpha(100+x, 200+y, color.Alpha{A: 0xff})
		}
	}

	dst, err := SeamCarve(src, 18, 13, mask)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dst.Bounds() != image.Rect(0, 0, 18, 13) {
		t.Fatalf("got bounds %v, want (0,0,18,13)", dst.Bounds())
	}
	if got := countPixels(dst, isSubject); got < 6*8 {
		t.Fatalf("subject not preserved: got %d red pixels, want at least %d", got, 6*8)
	}
}
