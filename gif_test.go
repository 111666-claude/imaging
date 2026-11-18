package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newSolidNRGBA creates an NRGBA image of the given bounds filled with c.
func newSolidNRGBA(b image.Rectangle, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// compositeGIF plays a decoded GIF back following the GIF89a disposal rules.
// It returns the displayed canvas after every frame and the canvas state
// remaining after the last frame's disposal.
func compositeGIF(g *gif.GIF) (frames []*image.NRGBA, final *image.NRGBA) {
	canvas := image.NewNRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	frames = make([]*image.NRGBA, len(g.Image))
	// previous holds the canvas state before the current frame was rendered;
	// it is needed by the DisposalPrevious method.
	previous := Clone(canvas)
	for i, pm := range g.Image {
		disposal := byte(0)
		if g.Disposal != nil {
			disposal = g.Disposal[i]
		}

		draw.Draw(canvas, pm.Bounds(), pm, pm.Bounds().Min, draw.Over)
		frames[i] = Clone(canvas)

		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, pm.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = Clone(previous)
		}
		previous = Clone(canvas)
	}
	return frames, canvas
}

// TestEncodeGIFAnimation uses a three-frame sample with partial sub-rectangle
// overlays and a transparent "hole" to verify that a standard GIF decoder can
// read every frame back and that compositing the frames reproduces the input.
func TestEncodeGIFAnimation(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	green := color.NRGBA{G: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}
	transparent := color.NRGBA{A: 0}

	// Frame 1: 2x2 red block at the top-left corner.
	frame1 := newSolidNRGBA(image.Rect(0, 0, 2, 2), red)

	// Frame 2: full 4x4 green canvas with a transparent 2x2 hole.
	frame2 := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if (x == 1 || x == 2) && (y == 1 || y == 2) {
				frame2.SetNRGBA(x, y, transparent)
			} else {
				frame2.SetNRGBA(x, y, green)
			}
		}
	}

	// Frame 3: 2x2 blue block at (2,2), partly covering the hole.
	frame3 := newSolidNRGBA(image.Rect(2, 2, 4, 4), blue)

	// An exact palette containing every color used by the sample, so that the
	// round-trip and the composited playback are lossless.
	exactPalette := color.Palette{red, green, blue, color.Transparent}

	frames := []GIFFrame{
		{Image: frame1, Delay: 100 * time.Millisecond},
		{Image: frame2, Delay: 25 * time.Millisecond},
		{Image: frame3, Delay: 7 * time.Millisecond, Disposal: GIFDisposalPrevious},
	}

	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, frames,
		GIFLoopCount(0),
		GIFNumColors(len(exactPalette)),
		GIFQuantizer(quantizer{exactPalette}),
		GIFDrawer(draw.Src),
	); err != nil {
		t.Fatalf("EncodeGIF failed: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("GIF89a")) {
		t.Fatalf("output is not a GIF89a file: % x", buf.Bytes()[:6])
	}

	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("standard decoder failed to read the GIF: %v", err)
	}
	if len(decoded.Image) != 3 {
		t.Fatalf("got %d frames, want 3", len(decoded.Image))
	}
	if decoded.Config.Width != 4 || decoded.Config.Height != 4 {
		t.Fatalf("got canvas %dx%d, want 4x4", decoded.Config.Width, decoded.Config.Height)
	}
	if decoded.LoopCount != 0 {
		t.Fatalf("got loop count %d, want 0 (loop forever)", decoded.LoopCount)
	}

	// Read-back: frame rectangles, delays and disposal values.
	wantBounds := []image.Rectangle{
		image.Rect(0, 0, 2, 2),
		image.Rect(0, 0, 4, 4),
		image.Rect(2, 2, 4, 4),
	}
	// GIF delays are stored in 1/100s units: 100ms -> 10, 25ms -> 3 (rounded
	// from 2.5), 7ms -> 1 (rounded from 0.7).
	wantDelay := []int{10, 3, 1}
	wantDisposal := []byte{GIFDisposalNone, GIFDisposalNone, gif.DisposalPrevious}
	for i, pm := range decoded.Image {
		if pm.Bounds() != wantBounds[i] {
			t.Fatalf("frame %d bounds: got %v, want %v", i, pm.Bounds(), wantBounds[i])
		}
		if decoded.Delay[i] != wantDelay[i] {
			t.Fatalf("frame %d delay: got %d, want %d", i, decoded.Delay[i], wantDelay[i])
		}
		if decoded.Disposal[i] != wantDisposal[i] {
			t.Fatalf("frame %d disposal: got %d, want %d", i, decoded.Disposal[i], wantDisposal[i])
		}
	}

	// Frames containing transparent pixels keep a transparent palette entry.
	for i, pm := range decoded.Image {
		if isFullyOpaque(frames[i].Image) {
			continue
		}
		hasTransparent := false
		for _, c := range pm.Palette {
			if _, _, _, a := c.RGBA(); a == 0 {
				hasTransparent = true
			}
		}
		if !hasTransparent {
			t.Fatalf("frame %d palette has no transparent entry: %v", i, pm.Palette)
		}
	}

	// Read-back of the stored frame pixels, sampled in each frame's own
	// coordinate space (the decoded frames keep their sub-rectangle origins).
	pm2 := decoded.Image[1]
	for y := pm2.Bounds().Min.Y; y < pm2.Bounds().Max.Y; y++ {
		for x := pm2.Bounds().Min.X; x < pm2.Bounds().Max.X; x++ {
			want := frame2.NRGBAAt(x, y)
			got := color.NRGBAModel.Convert(pm2.At(x, y)).(color.NRGBA)
			if got != want {
				t.Fatalf("frame2 pixel (%d,%d): got %v, want %v", x, y, got, want)
			}
		}
	}
	pm3 := decoded.Image[2]
	for y := pm3.Bounds().Min.Y; y < pm3.Bounds().Max.Y; y++ {
		for x := pm3.Bounds().Min.X; x < pm3.Bounds().Max.X; x++ {
			got := color.NRGBAModel.Convert(pm3.At(x, y)).(color.NRGBA)
			if got != blue {
				t.Fatalf("frame3 pixel (%d,%d): got %v, want %v", x, y, got, blue)
			}
		}
	}

	// Playback: compositing the decoded frames must match the intended result.
	// Frame 2 paints the whole canvas green except for the 2x2 hole at
	// (1,1)-(2,2). Only (1,1) of the hole overlaps frame 1's red block, so
	// the other three hole pixels show the initially transparent canvas;
	// frame 3 then paints blue over (2,2)-(3,3).
	composited, _ := compositeGIF(decoded)
	clear := color.NRGBA{A: 0}
	wantColors := [4][4]color.NRGBA{
		{green, green, green, green},
		{green, red, clear, green},
		{green, clear, blue, blue},
		{green, green, blue, blue},
	}
	final := composited[2]
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if got := final.NRGBAAt(x, y); got != wantColors[y][x] {
				t.Fatalf("composited pixel (%d,%d): got %v, want %v", x, y, got, wantColors[y][x])
			}
		}
	}

	// The transparent hole in frame 2 must reveal whatever was on the canvas:
	// frame 1's red where the two frames overlap, and the transparent
	// background elsewhere.
	after2 := composited[1]
	if got := after2.NRGBAAt(1, 1); got != red {
		t.Fatalf("hole pixel (1,1) after frame 2: got %v, want %v (frame 1 should show through)", got, red)
	}
	if got := after2.NRGBAAt(2, 2); got != clear {
		t.Fatalf("hole pixel (2,2) after frame 2: got %v, want transparent %v", got, clear)
	}
	if got := after2.NRGBAAt(2, 1); got != clear {
		t.Fatalf("hole pixel (2,1) after frame 2: got %v, want transparent %v", got, clear)
	}
	if got := after2.NRGBAAt(0, 0); got != green {
		t.Fatalf("pixel (0,0) after frame 2: got %v, want %v", got, green)
	}
	if got := after2.NRGBAAt(3, 3); got != green {
		t.Fatalf("pixel (3,3) after frame 2: got %v, want %v", got, green)
	}
}

// TestEncodeGIFDisposalMethods exercises every supported disposal method and
// verifies the values survive the round-trip.
func TestEncodeGIFDisposalMethods(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	green := color.NRGBA{G: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}

	frames := []GIFFrame{
		{Image: newSolidNRGBA(image.Rect(0, 0, 2, 2), red), Delay: time.Millisecond, Disposal: GIFDisposalDoNotDispose},
		{Image: newSolidNRGBA(image.Rect(0, 0, 2, 2), green), Delay: time.Millisecond, Disposal: GIFDisposalBackground},
		{Image: newSolidNRGBA(image.Rect(0, 0, 2, 2), blue), Delay: time.Millisecond, Disposal: GIFDisposalPrevious},
	}
	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, frames, GIFLoopCount(-1)); err != nil {
		t.Fatalf("EncodeGIF failed: %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	want := []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious}
	for i := range want {
		if decoded.Disposal[i] != want[i] {
			t.Fatalf("frame %d disposal: got %d, want %d", i, decoded.Disposal[i], want[i])
		}
	}
	// LoopCount -1 means "play once": no NETSCAPE looping extension is written.
	if decoded.LoopCount != -1 {
		t.Fatalf("got loop count %d, want -1", decoded.LoopCount)
	}

	// Background disposal restores transparency (no global color table)
	// after the frame has been displayed.
	composited, afterAll := compositeGIF(decoded)
	displayedGreen := composited[1].NRGBAAt(0, 0)
	if displayedGreen.G != 255 || displayedGreen.A != 255 {
		t.Fatalf("green frame display state lost: %v", displayedGreen)
	}
	if _, _, _, a := afterAll.NRGBAAt(0, 0).RGBA(); a != 0 {
		t.Fatalf("DisposalBackground did not restore transparency: %v", afterAll.NRGBAAt(0, 0))
	}
}

// TestEncodeGIFDefaultDisposal verifies the GIFDisposalMethod option fills in
// the disposal of frames that do not specify their own.
func TestEncodeGIFDefaultDisposal(t *testing.T) {
	c := color.NRGBA{R: 1, G: 2, B: 3, A: 255}
	frames := []GIFFrame{
		{Image: newSolidNRGBA(image.Rect(0, 0, 1, 1), c), Delay: time.Millisecond},
		{Image: newSolidNRGBA(image.Rect(0, 0, 1, 1), c), Delay: time.Millisecond, Disposal: GIFDisposalPrevious},
	}
	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, frames, GIFDisposalMethod(GIFDisposalBackground)); err != nil {
		t.Fatalf("EncodeGIF failed: %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	if decoded.Disposal[0] != gif.DisposalBackground {
		t.Fatalf("frame 0 disposal: got %d, want %d", decoded.Disposal[0], gif.DisposalBackground)
	}
	if decoded.Disposal[1] != gif.DisposalPrevious {
		t.Fatalf("frame 1 disposal: got %d, want %d", decoded.Disposal[1], gif.DisposalPrevious)
	}
}

type failWriter struct{ err error }

func (w failWriter) Write(p []byte) (int, error) { return 0, w.err }

// oversizedQuantizer ignores the palette capacity contract and always returns
// more than 256 colors.
type oversizedQuantizer struct{}

func (oversizedQuantizer) Quantize(p color.Palette, m image.Image) color.Palette {
	for i := 0; i < 300; i++ {
		p = append(p, color.NRGBA{R: uint8(i), A: 255})
	}
	return p
}

// emptyQuantizer returns no colors, exercising the fallback that guarantees a
// usable palette.
type emptyQuantizer struct{}

func (emptyQuantizer) Quantize(p color.Palette, m image.Image) color.Palette { return p }

// TestEncodeGIFErrors checks that every invalid input and writer failure has
// a deterministic result.
func TestEncodeGIFErrors(t *testing.T) {
	ok := newSolidNRGBA(image.Rect(0, 0, 2, 2), color.NRGBA{R: 255, A: 255})
	big := newSolidNRGBA(image.Rect(0, 0, 1<<16, 1), color.NRGBA{G: 255, A: 255})
	negativeOrigin := newSolidNRGBA(image.Rect(-1, 0, 1, 1), color.NRGBA{B: 255, A: 255})
	empty := image.NewNRGBA(image.Rect(0, 0, 0, 2))

	testCases := []struct {
		name   string
		frames []GIFFrame
		opts   []EncodeOption
		want   error
	}{
		{
			name:   "nil frame list",
			frames: nil,
			want:   ErrGIFEmptyFrames,
		},
		{
			name:   "empty frame list",
			frames: []GIFFrame{},
			want:   ErrGIFEmptyFrames,
		},
		{
			name: "nil image",
			frames: []GIFFrame{
				{Image: ok, Delay: time.Millisecond},
				{Image: nil, Delay: time.Millisecond},
			},
			want: ErrGIFEmptyFrames,
		},
		{
			name: "negative frame origin",
			frames: []GIFFrame{
				{Image: negativeOrigin, Delay: time.Millisecond},
			},
			want: ErrGIFInvalidBounds,
		},
		{
			name: "empty frame rectangle",
			frames: []GIFFrame{
				{Image: empty, Delay: time.Millisecond},
			},
			want: ErrGIFInvalidBounds,
		},
		{
			name: "dimension overflow",
			frames: []GIFFrame{
				{Image: big, Delay: time.Millisecond},
			},
			want: ErrGIFInvalidBounds,
		},
		{
			name: "canvas union overflow",
			frames: []GIFFrame{
				{Image: newSolidNRGBA(image.Rect(0, 0, 40000, 1), color.NRGBA{R: 255, A: 255}), Delay: time.Millisecond},
				{Image: newSolidNRGBA(image.Rect(60000, 0, 60001, 1), color.NRGBA{G: 255, A: 255}), Delay: time.Millisecond},
				{Image: newSolidNRGBA(image.Rect(65535, 0, 65536, 1), color.NRGBA{B: 255, A: 255}), Delay: time.Millisecond},
			},
			want: ErrGIFInvalidBounds,
		},
		{
			name: "negative delay",
			frames: []GIFFrame{
				{Image: ok, Delay: -time.Millisecond},
			},
			want: ErrGIFInvalidDelay,
		},
		{
			name: "delay overflow",
			frames: []GIFFrame{
				{Image: ok, Delay: gifMaxDelay + gifCentisecond},
			},
			want: ErrGIFInvalidDelay,
		},
		{
			name: "illegal disposal",
			frames: []GIFFrame{
				{Image: ok, Delay: time.Millisecond, Disposal: 4},
			},
			want: ErrGIFInvalidDisposal,
		},
		{
			name: "illegal default disposal",
			frames: []GIFFrame{
				{Image: ok, Delay: time.Millisecond},
			},
			opts: []EncodeOption{GIFDisposalMethod(9)},
			want: ErrGIFInvalidDisposal,
		},
		{
			name: "too many colors from quantizer",
			frames: []GIFFrame{
				{Image: ok, Delay: time.Millisecond},
			},
			opts: []EncodeOption{GIFQuantizer(oversizedQuantizer{})},
			want: ErrGIFTooManyColors,
		},
		{
			name: "num colors below one",
			frames: []GIFFrame{
				{Image: ok, Delay: time.Millisecond},
			},
			opts: []EncodeOption{GIFNumColors(0)},
			want: ErrGIFTooManyColors,
		},
		{
			name: "num colors above 256",
			frames: []GIFFrame{
				{Image: ok, Delay: time.Millisecond},
			},
			opts: []EncodeOption{GIFNumColors(257)},
			want: ErrGIFTooManyColors,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := EncodeGIF(io.Discard, tc.frames, tc.opts...)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want error wrapping %v", err, tc.want)
			}
		})
	}

	// A failing writer must propagate the error instead of swallowing it.
	writeErr := errors.New("disk on fire")
	err := EncodeGIF(failWriter{err: writeErr}, []GIFFrame{
		{Image: ok, Delay: time.Millisecond},
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("writer error: got %v, want %v", err, writeErr)
	}
}

// TestEncodeGIFQuantization verifies that frames with more than 256 colors are
// quantized into a valid decodable palette instead of being rejected.
func TestEncodeGIFQuantization(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 8),
				G: uint8(y * 32),
				B: uint8((x + y) * 4),
				A: 255,
			})
		}
	}
	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, []GIFFrame{{Image: img, Delay: time.Millisecond}}); err != nil {
		t.Fatalf("EncodeGIF failed: %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	if len(decoded.Image[0].Palette) > 256 {
		t.Fatalf("palette has %d entries, want at most 256", len(decoded.Image[0].Palette))
	}

	// A custom small palette is honored through the existing options.
	buf.Reset()
	custom := color.Palette{
		color.Black,
		color.NRGBA{R: 255, A: 255},
		color.NRGBA{G: 255, A: 255},
		color.NRGBA{B: 255, A: 255},
		color.Transparent,
	}
	err = EncodeGIF(buf, []GIFFrame{{Image: img, Delay: time.Millisecond}},
		GIFNumColors(len(custom)), GIFQuantizer(quantizer{custom}), GIFDrawer(draw.Src))
	if err != nil {
		t.Fatalf("EncodeGIF with custom palette failed: %v", err)
	}
	decoded, err = gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	// GIF color tables are padded up to a power of two on read-back (5
	// requested colors become an 8-entry table), so the meaningful limit is
	// the GIF maximum of 256 rather than the unpadded request size.
	if got := len(decoded.Image[0].Palette); got > 256 {
		t.Fatalf("palette has %d entries, want at most 256", got)
	}
}

// plainImage wraps an NRGBA image without exposing the Opaque method, forcing
// EncodeGIF to fall back to per-pixel alpha scanning.
type plainImage struct{ img *image.NRGBA }

func (p plainImage) ColorModel() color.Model { return p.img.ColorModel() }
func (p plainImage) Bounds() image.Rectangle { return p.img.Bounds() }
func (p plainImage) At(x, y int) color.Color { return p.img.At(x, y) }

// TestEncodeGIFCustomDrawerAndOpaqueFallback exercises the GIFDrawer option
// and the per-pixel opacity scan used for image types without an Opaque method.
func TestEncodeGIFCustomDrawerAndOpaqueFallback(t *testing.T) {
	base := newSolidNRGBA(image.Rect(0, 0, 2, 2), color.NRGBA{R: 100, G: 150, B: 200, A: 255})

	// draw.Src as a custom drawer keeps the result decodable and lossless
	// when the source colors are all palette entries.
	pal := color.Palette{
		color.NRGBA{R: 100, G: 150, B: 200, A: 255},
		color.NRGBA{G: 255, A: 255},
	}
	buf := &bytes.Buffer{}
	err := EncodeGIF(buf, []GIFFrame{{Image: base, Delay: time.Millisecond}},
		GIFNumColors(len(pal)), GIFQuantizer(quantizer{pal}), GIFDrawer(draw.Src))
	if err != nil {
		t.Fatalf("EncodeGIF with custom drawer failed: %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	pm := decoded.Image[0]
	if got := color.NRGBAModel.Convert(pm.At(0, 0)).(color.NRGBA); got != pal[0] {
		t.Fatalf("custom drawer pixel: got %v, want %v", got, pal[0])
	}

	// An opaque image type without an Opaque method must still be detected
	// as opaque (no transparent palette entry added).
	buf.Reset()
	if err := EncodeGIF(buf, []GIFFrame{{Image: plainImage{base}, Delay: time.Millisecond}},
		GIFNumColors(2), GIFQuantizer(quantizer{pal}), GIFDrawer(draw.Src)); err != nil {
		t.Fatalf("EncodeGIF with plain image failed: %v", err)
	}
	decoded, err = gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	for _, c := range decoded.Image[0].Palette {
		if _, _, _, a := c.RGBA(); a == 0 {
			t.Fatalf("opaque image unexpectedly got a transparent palette entry: %v", decoded.Image[0].Palette)
		}
	}

	// The same fallback must detect real transparency and reserve a
	// transparent palette entry.
	withHole := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := range withHole.Pix {
		withHole.Pix[i] = 255
	}
	withHole.SetNRGBA(1, 1, color.NRGBA{A: 0})
	buf.Reset()
	if err := EncodeGIF(buf, []GIFFrame{{Image: plainImage{withHole}, Delay: time.Millisecond}},
		GIFNumColors(2), GIFDrawer(draw.Src)); err != nil {
		t.Fatalf("EncodeGIF with transparent plain image failed: %v", err)
	}
	decoded, err = gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	hasTransparent := false
	for _, c := range decoded.Image[0].Palette {
		if _, _, _, a := c.RGBA(); a == 0 {
			hasTransparent = true
		}
	}
	if !hasTransparent {
		t.Fatalf("transparent image got no transparent palette entry: %v", decoded.Image[0].Palette)
	}
}

// TestEncodeGIFPalettedInput verifies that an already-paletted frame is passed
// through without re-quantization and stays decodable.
func TestEncodeGIFPalettedInput(t *testing.T) {
	pal := color.Palette{
		color.NRGBA{R: 255, A: 255},
		color.NRGBA{G: 255, A: 255},
		color.Transparent,
	}
	pm := image.NewPaletted(image.Rect(0, 0, 3, 1), pal)
	pm.Pix[0] = 0
	pm.Pix[1] = 1
	pm.Pix[2] = 2

	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, []GIFFrame{{Image: pm, Delay: time.Millisecond}}); err != nil {
		t.Fatalf("EncodeGIF with paletted input failed: %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	got := decoded.Image[0]
	if got.Pix[0] != 0 || got.Pix[1] != 1 {
		t.Fatalf("palette indices changed: %v", got.Pix)
	}
	if c := color.NRGBAModel.Convert(got.At(2, 0)).(color.NRGBA); c.A != 0 {
		t.Fatalf("transparent index lost: %v", c)
	}
}

// TestEncodeGIFEmptyQuantizer verifies the fallback that guarantees a usable
// palette when a custom quantizer returns nothing.
func TestEncodeGIFEmptyQuantizer(t *testing.T) {
	ok := newSolidNRGBA(image.Rect(0, 0, 2, 2), color.NRGBA{R: 255, A: 255})
	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, []GIFFrame{{Image: ok, Delay: time.Millisecond}},
		GIFQuantizer(emptyQuantizer{})); err != nil {
		t.Fatalf("EncodeGIF with empty quantizer failed: %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	if len(decoded.Image[0].Palette) == 0 {
		t.Fatalf("expected a non-empty fallback palette")
	}
}

// TestEncodeGIFSingleFrame makes sure the animation writer also handles a
// single frame and preserves transparency.
func TestEncodeGIFSingleFrame(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	img.SetNRGBA(1, 1, color.NRGBA{A: 0})

	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, []GIFFrame{{Image: img}}); err != nil {
		t.Fatalf("EncodeGIF failed: %v", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	if len(decoded.Image) != 1 || decoded.Delay[0] != 0 {
		t.Fatalf("unexpected decode result: %d frames, delay %d", len(decoded.Image), decoded.Delay[0])
	}
	pm := decoded.Image[0]
	transparentIndex := -1
	for i, c := range pm.Palette {
		if _, _, _, a := c.RGBA(); a == 0 {
			transparentIndex = i
		}
	}
	if transparentIndex < 0 {
		t.Fatalf("palette has no transparent entry: %v", pm.Palette)
	}
	if pm.Pix[pm.PixOffset(1, 1)] != uint8(transparentIndex) {
		t.Fatalf("transparent pixel not mapped to the transparent index")
	}
}

// TestSaveGIF checks the file-based wrapper and that the written file can be
// opened again with the package's own Open function.
func TestSaveGIF(t *testing.T) {
	dir, err := os.MkdirTemp("", "imaging-gif")
	if err != nil {
		t.Fatalf("failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(dir)

	c := color.NRGBA{R: 10, G: 20, B: 30, A: 255}
	frames := []GIFFrame{
		{Image: newSolidNRGBA(image.Rect(0, 0, 3, 3), c), Delay: 50 * time.Millisecond},
		{Image: newSolidNRGBA(image.Rect(1, 1, 3, 3), color.NRGBA{G: 255, A: 255}), Delay: 50 * time.Millisecond},
	}
	filename := filepath.Join(dir, "animation.gif")
	if err := SaveGIF(frames, filename, GIFLoopCount(3)); err != nil {
		t.Fatalf("SaveGIF failed: %v", err)
	}

	decoded, err := gif.DecodeAll(openReader(t, filename))
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	if len(decoded.Image) != 2 || decoded.LoopCount != 3 {
		t.Fatalf("unexpected decode result: %d frames, loop count %d", len(decoded.Image), decoded.LoopCount)
	}

	// The generic Open entrypoint must read the first frame of the file too.
	img, err := Open(filename)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if img.Bounds() != image.Rect(0, 0, 3, 3) {
		t.Fatalf("opened bounds: got %v, want 3x3", img.Bounds())
	}
}

// TestSaveGIFErrors makes sure file creation and close errors are surfaced
// instead of producing a silent empty file.
func TestSaveGIFErrors(t *testing.T) {
	frames := []GIFFrame{
		{Image: newSolidNRGBA(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255})},
	}

	prevFS := fs
	fs = badFS{}
	defer func() { fs = prevFS }()

	if err := SaveGIF(frames, "unwritable.gif"); err != errCreate {
		t.Fatalf("create error: got %v, want %v", err, errCreate)
	}
	if err := SaveGIF(frames, "badFile.jpg"); err != errClose {
		t.Fatalf("close error: got %v, want %v", err, errClose)
	}
}

func openReader(t *testing.T, filename string) io.Reader {
	t.Helper()
	f, err := os.Open(filename)
	if err != nil {
		t.Fatalf("open %q: %v", filename, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
