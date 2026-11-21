package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"testing"
)

// makePhotoLikeImage generates a smooth image with gradients, texture and a
// sharp diagonal edge, resembling natural content.
func makePhotoLikeImage(w, h int) *image.NRGBA {
	rnd := rand.New(rand.NewSource(42))
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := dst.PixOffset(x, y)
			v := uint8((x*3 + y*2) % 256)
			n := uint8(rnd.Intn(24))
			dst.Pix[i+0] = v/2 + n
			dst.Pix[i+1] = uint8((int(v) + int(n)) / 2)
			dst.Pix[i+2] = 255 - v/2 - n/4
			dst.Pix[i+3] = 0xff
			if x > y+w/4 { // sharp true edge, not grid aligned
				dst.Pix[i+0] = 240
				dst.Pix[i+1] = 240
				dst.Pix[i+2] = 240
			}
		}
	}
	return dst
}

func encodeJPEG(t *testing.T, img image.Image, quality int) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	out, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatalf("jpeg.Decode: %v", err)
	}
	return out
}

func blockinessOf(t *testing.T, img image.Image, opts *BlockinessOptions) BlockinessResult {
	t.Helper()
	res, err := JPEGBlockiness(img, opts)
	if err != nil {
		t.Fatalf("JPEGBlockiness: %v", err)
	}
	if math.IsNaN(res.Score) || res.Score < 0 || res.Score > 1 {
		t.Fatalf("score %v out of range [0, 1]", res.Score)
	}
	for _, b := range res.Blocks {
		if math.IsNaN(b.Score) || b.Score < 0 || b.Score > 1 {
			t.Fatalf("block score %v out of range [0, 1]", b.Score)
		}
	}
	return res
}

func TestJPEGBlockinessQualityOrdering(t *testing.T) {
	src := makePhotoLikeImage(128, 128)
	low := encodeJPEG(t, src, 10)
	high := encodeJPEG(t, src, 95)

	scoreSrc := blockinessOf(t, src, nil).Score
	scoreHigh := blockinessOf(t, high, nil).Score
	scoreLow := blockinessOf(t, low, nil).Score

	if !(scoreLow > scoreHigh) || !(scoreLow > scoreSrc) {
		t.Errorf("expected low quality to score highest, got low=%.4f high=%.4f src=%.4f", scoreLow, scoreHigh, scoreSrc)
	}
	if scoreHigh > 0.2 || scoreSrc > 0.2 {
		t.Errorf("clean images scored too high: high=%.4f src=%.4f", scoreHigh, scoreSrc)
	}
}

func TestJPEGBlockinessCheckerboardNoFalsePositive(t *testing.T) {
	// 8x8 black/white checkerboard aligned to the grid: the strongest possible
	// grid-aligned edges, but they are true contours and must be excluded.
	dst := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			i := dst.PixOffset(x, y)
			v := uint8(0)
			if (x/8+y/8)%2 == 0 {
				v = 255
			}
			dst.Pix[i+0] = v
			dst.Pix[i+1] = v
			dst.Pix[i+2] = v
			dst.Pix[i+3] = 0xff
		}
	}
	res := blockinessOf(t, dst, nil)
	if res.Score > 0.05 {
		t.Errorf("checkerboard false positive: score %.4f", res.Score)
	}
}

// makeBlockedImage creates a flat image with small per-block luminance
// offsets, imitating JPEG blocking without true edges.
func makeBlockedImage(w, h, ox, oy int, delta uint8) *image.NRGBA {
	rnd := rand.New(rand.NewSource(7))
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			_ = rnd.Int63 // keep per-block sequence independent of position
			i := dst.PixOffset(x, y)
			bx, by := (x-ox)/8, (y-oy)/8
			if x < ox || y < oy {
				bx, by = 0, 0
			}
			v := 128
			if (bx+by)%2 == 0 {
				v += int(delta)
			} else {
				v -= int(delta)
			}
			dst.Pix[i+0] = uint8(v)
			dst.Pix[i+1] = uint8(v)
			dst.Pix[i+2] = uint8(v)
			dst.Pix[i+3] = 0xff
		}
	}
	return dst
}

func TestJPEGBlockinessArtificialBlocks(t *testing.T) {
	img := makeBlockedImage(64, 64, 0, 0, 6)
	res := blockinessOf(t, img, nil)
	if res.Score < 0.9 {
		t.Errorf("artificial block edges scored too low: %.4f", res.Score)
	}
	if len(res.Blocks) != 8*8 {
		t.Fatalf("got %d blocks, want 64", len(res.Blocks))
	}
	for i, b := range res.Blocks {
		want := image.Rect((i%8)*8, (i/8)*8, (i%8)*8+8, (i/8)*8+8)
		if b.Bounds != want {
			t.Errorf("block %d bounds %v, want %v", i, b.Bounds, want)
		}
	}

	// The same content shifted by 3px must be detected only with the
	// matching grid offset.
	shifted := image.NewNRGBA(image.Rect(0, 0, 67, 67))
	for y := 0; y < 67; y++ {
		for x := 0; x < 67; x++ {
			shifted.SetNRGBA(x, y, color.NRGBA{0x80, 0x80, 0x80, 0xff})
		}
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			i := img.PixOffset(x, y)
			j := shifted.PixOffset(x+3, y+3)
			copy(shifted.Pix[j:j+4], img.Pix[i:i+4])
		}
	}
	aligned := blockinessOf(t, shifted, &BlockinessOptions{OffsetX: 3, OffsetY: 3})
	misaligned := blockinessOf(t, shifted, nil)
	if aligned.Score < 0.9 {
		t.Errorf("shifted blocks with matching offset scored too low: %.4f", aligned.Score)
	}
	if aligned.Score <= misaligned.Score {
		t.Errorf("offset semantics broken: aligned=%.4f misaligned=%.4f", aligned.Score, misaligned.Score)
	}
}

func TestJPEGBlockinessHeatmap(t *testing.T) {
	img := makeBlockedImage(32, 32, 0, 0, 6)
	res := blockinessOf(t, img, nil)
	hm := res.Heatmap()
	if hm.Bounds() != img.Bounds() {
		t.Fatalf("heatmap bounds %v, want %v", hm.Bounds(), img.Bounds())
	}
	if hm.GrayAt(4, 4).Y == 0 {
		t.Errorf("heatmap is zero inside a blocky region")
	}
}

func TestJPEGBlockinessNonZeroBounds(t *testing.T) {
	base := makeBlockedImage(64, 64, 0, 0, 6)
	// Same pixels in an image with non-zero Min.
	shifted := image.NewNRGBA(image.Rect(11, 7, 75, 71))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			i := base.PixOffset(x, y)
			j := shifted.PixOffset(x+11, y+7)
			copy(shifted.Pix[j:j+4], base.Pix[i:i+4])
		}
	}
	want := blockinessOf(t, base, nil)
	got := blockinessOf(t, shifted, nil)
	if got.Score != want.Score {
		t.Errorf("non-zero bounds changed score: got %.6f want %.6f", got.Score, want.Score)
	}
	if got.Blocks[0].Bounds.Min != image.Pt(11, 7) {
		t.Errorf("block bounds not in image coordinates: %v", got.Blocks[0].Bounds)
	}
	hm := got.Heatmap()
	if hm.Bounds() != shifted.Bounds() {
		t.Errorf("heatmap bounds %v, want %v", hm.Bounds(), shifted.Bounds())
	}
}

func TestJPEGBlockinessTransparency(t *testing.T) {
	// Fully transparent image: no opaque pixels, must score 0 without NaN.
	transparent := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	res := blockinessOf(t, transparent, nil)
	if res.Score != 0 {
		t.Errorf("transparent image scored %.4f, want 0", res.Score)
	}

	// Blocky content visible only through half of the image; the transparent
	// half must not contribute edges.
	half := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	blocked := makeBlockedImage(64, 64, 0, 0, 6)
	for y := 0; y < 64; y++ {
		for x := 0; x < 32; x++ {
			i := blocked.PixOffset(x, y)
			j := half.PixOffset(x, y)
			copy(half.Pix[j:j+4], blocked.Pix[i:i+4])
		}
	}
	res = blockinessOf(t, half, nil)
	if res.Score < 0.9 {
		t.Errorf("blocky content behind opaque half scored too low: %.4f", res.Score)
	}
}

func TestJPEGBlockinessSmallAndDegenerate(t *testing.T) {
	for _, r := range []image.Rectangle{
		image.Rect(0, 0, 0, 0),
		image.Rect(0, 0, 7, 7),
		image.Rect(0, 0, 8, 1),
		image.Rect(0, 0, 1, 8),
	} {
		res := blockinessOf(t, image.NewNRGBA(r), nil)
		if res.Score != 0 {
			t.Errorf("image %v scored %.4f, want 0", r, res.Score)
		}
		if len(res.Blocks) != 0 {
			t.Errorf("image %v produced %d blocks, want 0", r, len(res.Blocks))
		}
	}
	// Exactly one block.
	res := blockinessOf(t, makeBlockedImage(8, 8, 0, 0, 6), nil)
	if len(res.Blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(res.Blocks))
	}
}

func TestJPEGBlockinessNonNRGBAInput(t *testing.T) {
	// Opaque uniform RGBA (non-NRGBA, no JPEG involved): flat, score 0.
	uniform := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			uniform.SetRGBA(x, y, color.RGBA{0x10, 0x80, 0xf0, 0xff})
		}
	}
	if res := blockinessOf(t, uniform, nil); res.Score != 0 {
		t.Errorf("uniform RGBA scored %.4f, want 0", res.Score)
	}
	// Paletted input must work too.
	pal := image.NewPaletted(image.Rect(0, 0, 16, 16), color.Palette{
		color.NRGBA{0, 0, 0, 0xff}, color.NRGBA{0xff, 0xff, 0xff, 0xff},
	})
	if res := blockinessOf(t, pal, nil); math.IsNaN(res.Score) {
		t.Errorf("paletted image produced NaN")
	}
}

func TestJPEGBlockinessInvalidOptions(t *testing.T) {
	img := makeBlockedImage(16, 16, 0, 0, 6)
	bad := []BlockinessOptions{
		{OffsetX: -1},
		{OffsetX: 8},
		{OffsetY: -1},
		{OffsetY: 8},
		{Stride: -2},
		{EdgeThreshold: -1},
		{EdgeThreshold: math.NaN()},
		{EdgeThreshold: math.Inf(1)},
	}
	for i, opts := range bad {
		res, err := JPEGBlockiness(img, &opts)
		if err == nil {
			t.Errorf("case %d: expected error, got score %.4f", i, res.Score)
		}
		if math.IsNaN(res.Score) {
			t.Errorf("case %d: NaN score on invalid options", i)
		}
	}
	// Stride 0 means default, not an error.
	if _, err := JPEGBlockiness(img, &BlockinessOptions{Stride: 0}); err != nil {
		t.Errorf("Stride 0 should select the default: %v", err)
	}
	// Subsampling must still produce a valid score.
	res := blockinessOf(t, img, &BlockinessOptions{Stride: 2})
	if res.Score < 0.9 {
		t.Errorf("strided evaluation scored too low: %.4f", res.Score)
	}
}

func BenchmarkJPEGBlockiness(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := JPEGBlockiness(testdataBranchesJPG, nil); err != nil {
			b.Fatal(err)
		}
	}
}
