package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io/ioutil"
	"path/filepath"
	"testing"
)

// testGIFPalette is the palette used to build animated test GIFs.
// Entry 0 is fully transparent, entries 1-4 are opaque colors.
var testGIFPalette = color.Palette{
	color.RGBA{0x00, 0x00, 0x00, 0x00}, // 0: transparent
	color.RGBA{0xff, 0x00, 0x00, 0xff}, // 1: red
	color.RGBA{0x00, 0xff, 0x00, 0xff}, // 2: green
	color.RGBA{0x00, 0x00, 0xff, 0xff}, // 3: blue
	color.RGBA{0xff, 0xff, 0x00, 0xff}, // 4: yellow (background)
}

const (
	testRed    = 1
	testGreen  = 2
	testBlue   = 3
	testYellow = 4
)

// testQuantizer appends a fixed set of colors, used to get deterministic
// palettes when encoding resized test animations.
type testQuantizer color.Palette

func (q testQuantizer) Quantize(p color.Palette, m image.Image) color.Palette {
	for _, c := range q {
		if len(p) == cap(p) {
			break
		}
		p = append(p, c)
	}
	return p
}

func fillPaletted(img *image.Paletted, r image.Rectangle, index uint8) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetColorIndex(x, y, index)
		}
	}
}

// makeTestGIF builds an 8x8 animated GIF that exercises partial frames,
// transparent pixels and background/previous disposal methods.
//
// Frame 0: full canvas, red. Delay 10, disposal none.
// Frame 1: rect (2,2)-(6,6), green with transparent holes at (2,2) and
// (3,3). Delay 20, disposal background.
// Frame 2: rect (4,4)-(8,8), blue. Delay 30, disposal previous.
// Frame 3: rect (1,1)-(3,3), blue. Delay 40, disposal none.
func makeTestGIF() *gif.GIF {
	const size = 8

	newFrame := func(r image.Rectangle, index uint8) *image.Paletted {
		pal := make(color.Palette, len(testGIFPalette))
		copy(pal, testGIFPalette)
		frame := image.NewPaletted(r, pal)
		fillPaletted(frame, r, index)
		return frame
	}

	f0 := newFrame(image.Rect(0, 0, size, size), testRed)

	f1 := newFrame(image.Rect(2, 2, 6, 6), testGreen)
	f1.SetColorIndex(2, 2, 0) // transparent hole
	f1.SetColorIndex(3, 3, 0) // transparent hole

	f2 := newFrame(image.Rect(4, 4, 8, 8), testBlue)

	f3 := newFrame(image.Rect(1, 1, 3, 3), testBlue)

	return &gif.GIF{
		Image:    []*image.Paletted{f0, f1, f2, f3},
		Delay:    []int{10, 20, 30, 40},
		Disposal: []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious, gif.DisposalNone},
		Config: image.Config{
			ColorModel: testGIFPalette,
			Width:      size,
			Height:     size,
		},
		BackgroundIndex: testYellow,
		LoopCount:       2,
	}
}

func encodeTestGIF(t *testing.T, g *gif.GIF) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := gif.EncodeAll(buf, g); err != nil {
		t.Fatalf("failed to encode test GIF: %v", err)
	}
	return buf.Bytes()
}

func nrgbaAt(img *image.NRGBA, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

func checkPixel(t *testing.T, img *image.NRGBA, x, y int, want color.NRGBA) {
	t.Helper()
	if got := nrgbaAt(img, x, y); got != want {
		t.Errorf("pixel (%d,%d): got %v, want %v", x, y, got, want)
	}
}

var (
	testColRed    = color.NRGBA{0xff, 0x00, 0x00, 0xff}
	testColGreen  = color.NRGBA{0x00, 0xff, 0x00, 0xff}
	testColBlue   = color.NRGBA{0x00, 0x00, 0xff, 0xff}
	testColYellow = color.NRGBA{0xff, 0xff, 0x00, 0xff}
)

func TestCompositeGIFFrames(t *testing.T) {
	data := encodeTestGIF(t, makeTestGIF())
	g, err := DecodeGIF(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeGIF: %v", err)
	}

	frames, err := compositeGIFFrames(g)
	if err != nil {
		t.Fatalf("compositeGIFFrames: %v", err)
	}
	if len(frames) != 4 {
		t.Fatalf("got %d frames, want 4", len(frames))
	}
	for i, f := range frames {
		if f.Bounds() != image.Rect(0, 0, 8, 8) {
			t.Errorf("frame %d bounds: got %v, want (0,0)-(8,8)", i, f.Bounds())
		}
	}

	// Frame 0: solid red.
	checkPixel(t, frames[0], 0, 0, testColRed)
	checkPixel(t, frames[0], 5, 5, testColRed)
	checkPixel(t, frames[0], 7, 7, testColRed)

	// Frame 1: green square (2,2)-(6,6) over red, holes stay red.
	checkPixel(t, frames[1], 1, 1, testColRed)
	checkPixel(t, frames[1], 4, 4, testColGreen)
	checkPixel(t, frames[1], 5, 5, testColGreen)
	checkPixel(t, frames[1], 2, 2, testColRed) // transparent hole
	checkPixel(t, frames[1], 3, 3, testColRed) // transparent hole
	checkPixel(t, frames[1], 6, 6, testColRed)

	// Frame 2: frame 1 area restored to the background color (yellow),
	// then blue square (4,4)-(8,8) drawn on top.
	checkPixel(t, frames[2], 0, 0, testColRed)
	checkPixel(t, frames[2], 2, 2, testColYellow)
	checkPixel(t, frames[2], 3, 3, testColYellow)
	checkPixel(t, frames[2], 4, 4, testColBlue)
	checkPixel(t, frames[2], 7, 7, testColBlue)

	// Frame 3: canvas restored to the state before frame 2 was drawn
	// (red + yellow square), then blue square (1,1)-(3,3) drawn on top.
	checkPixel(t, frames[3], 0, 0, testColRed)
	checkPixel(t, frames[3], 1, 1, testColBlue)
	checkPixel(t, frames[3], 2, 2, testColBlue)
	checkPixel(t, frames[3], 3, 3, testColYellow)
	checkPixel(t, frames[3], 5, 5, testColYellow)
	checkPixel(t, frames[3], 7, 7, testColRed)
}

func TestDecodeGIFMetadata(t *testing.T) {
	data := encodeTestGIF(t, makeTestGIF())
	g, err := DecodeGIF(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeGIF: %v", err)
	}

	if len(g.Image) != 4 {
		t.Fatalf("got %d frames, want 4", len(g.Image))
	}
	wantDelay := []int{10, 20, 30, 40}
	for i, d := range g.Delay {
		if d != wantDelay[i] {
			t.Errorf("delay[%d]: got %d, want %d", i, d, wantDelay[i])
		}
	}
	if g.LoopCount != 2 {
		t.Errorf("loop count: got %d, want 2", g.LoopCount)
	}
	if g.Config.Width != 8 || g.Config.Height != 8 {
		t.Errorf("canvas: got %dx%d, want 8x8", g.Config.Width, g.Config.Height)
	}
}

func TestDecodeGIFInvalidData(t *testing.T) {
	if _, err := DecodeGIF(bytes.NewReader(nil)); err == nil {
		t.Error("empty input: expected error, got nil")
	}
	if _, err := DecodeGIF(bytes.NewReader([]byte("not a gif"))); err == nil {
		t.Error("garbage input: expected error, got nil")
	}

	// Valid header declaring a zero-sized canvas.
	zero := []byte{'G', 'I', 'F', '8', '9', 'a', 0, 0, 0, 0, 0, 0, 0, 0x3b}
	if _, err := DecodeGIF(bytes.NewReader(zero)); err == nil {
		t.Error("zero canvas: expected error, got nil")
	}
}

func TestDecodeGIFMemoryLimits(t *testing.T) {
	// Valid header declaring a huge canvas: must be rejected before
	// any frame memory is allocated.
	huge := []byte{'G', 'I', 'F', '8', '9', 'a',
		0xff, 0xff, 0xff, 0xff, // 65535 x 65535 canvas
		0, 0, 0, 0x3b}
	_, err := DecodeGIF(bytes.NewReader(huge))
	if !errors.Is(err, ErrGIFTooLarge) {
		t.Errorf("huge canvas: got %v, want ErrGIFTooLarge", err)
	}

	data := encodeTestGIF(t, makeTestGIF())

	defer func(orig int) { MaxGIFCanvasPixels = orig }(MaxGIFCanvasPixels)
	MaxGIFCanvasPixels = 10
	if _, err := DecodeGIF(bytes.NewReader(data)); !errors.Is(err, ErrGIFTooLarge) {
		t.Errorf("canvas over limit: got %v, want ErrGIFTooLarge", err)
	}
	MaxGIFCanvasPixels = 1 << 24

	defer func(orig int) { MaxGIFTotalPixels = orig }(MaxGIFTotalPixels)
	MaxGIFTotalPixels = 8 * 8 * 3 // one frame short of the 4-frame animation
	if _, err := DecodeGIF(bytes.NewReader(data)); !errors.Is(err, ErrGIFTooLarge) {
		t.Errorf("total pixels over limit: got %v, want ErrGIFTooLarge", err)
	}
}

func TestResizeGIFInvalidInput(t *testing.T) {
	g := makeTestGIF()

	if _, err := ResizeGIF(nil, 4, 4, NearestNeighbor); !errors.Is(err, ErrInvalidGIFData) {
		t.Errorf("nil gif: got %v, want ErrInvalidGIFData", err)
	}
	if _, err := ResizeGIF(&gif.GIF{}, 4, 4, NearestNeighbor); !errors.Is(err, ErrInvalidGIFData) {
		t.Errorf("no frames: got %v, want ErrInvalidGIFData", err)
	}
	if _, err := ResizeGIF(g, 0, 4, NearestNeighbor); !errors.Is(err, ErrInvalidGIFSize) {
		t.Errorf("zero width: got %v, want ErrInvalidGIFSize", err)
	}
	if _, err := ResizeGIF(g, 4, 0, NearestNeighbor); !errors.Is(err, ErrInvalidGIFSize) {
		t.Errorf("zero height: got %v, want ErrInvalidGIFSize", err)
	}
	if _, err := ResizeGIF(g, -1, 4, NearestNeighbor); !errors.Is(err, ErrInvalidGIFSize) {
		t.Errorf("negative width: got %v, want ErrInvalidGIFSize", err)
	}
}

func TestResizeGIF(t *testing.T) {
	data := encodeTestGIF(t, makeTestGIF())
	src, err := DecodeGIF(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeGIF: %v", err)
	}

	// Use an exact-palette quantizer and nearest-neighbor filtering so the
	// resized animation can be compared pixel-by-pixel.
	out, err := ResizeGIF(src, 4, 4, NearestNeighbor,
		GIFQuantizer(testQuantizer(testGIFPalette[1:])))
	if err != nil {
		t.Fatalf("ResizeGIF: %v", err)
	}

	// Frame order, delays and loop count must be preserved.
	if len(out.Image) != 4 {
		t.Fatalf("got %d frames, want 4", len(out.Image))
	}
	wantDelay := []int{10, 20, 30, 40}
	for i, d := range out.Delay {
		if d != wantDelay[i] {
			t.Errorf("delay[%d]: got %d, want %d", i, d, wantDelay[i])
		}
	}
	if out.LoopCount != 2 {
		t.Errorf("loop count: got %d, want 2", out.LoopCount)
	}

	// Output frame rectangles and palettes must be valid.
	if out.Config.Width != 4 || out.Config.Height != 4 {
		t.Errorf("canvas: got %dx%d, want 4x4", out.Config.Width, out.Config.Height)
	}
	for i, frame := range out.Image {
		if frame.Bounds() != image.Rect(0, 0, 4, 4) {
			t.Errorf("frame %d bounds: got %v, want (0,0)-(4,4)", i, frame.Bounds())
		}
		if len(frame.Palette) == 0 || len(frame.Palette) > 256 {
			t.Errorf("frame %d palette: got %d colors", i, len(frame.Palette))
		}
	}

	// Encode and re-decode: every frame must keep its size and the visible
	// picture must match the resized source picture.
	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, out); err != nil {
		t.Fatalf("EncodeGIF: %v", err)
	}
	decoded, err := DecodeGIF(buf)
	if err != nil {
		t.Fatalf("DecodeGIF of resized animation: %v", err)
	}
	if len(decoded.Image) != 4 {
		t.Fatalf("re-decoded: got %d frames, want 4", len(decoded.Image))
	}
	for i, frame := range decoded.Image {
		if frame.Bounds() != image.Rect(0, 0, 4, 4) {
			t.Errorf("re-decoded frame %d bounds: got %v, want (0,0)-(4,4)", i, frame.Bounds())
		}
	}
	for i, d := range decoded.Delay {
		if d != wantDelay[i] {
			t.Errorf("re-decoded delay[%d]: got %d, want %d", i, d, wantDelay[i])
		}
	}
	if decoded.LoopCount != 2 {
		t.Errorf("re-decoded loop count: got %d, want 2", decoded.LoopCount)
	}

	srcVisible, err := compositeGIFFrames(src)
	if err != nil {
		t.Fatalf("compositeGIFFrames(src): %v", err)
	}
	outVisible, err := compositeGIFFrames(decoded)
	if err != nil {
		t.Fatalf("compositeGIFFrames(decoded): %v", err)
	}
	for i := range srcVisible {
		want := Resize(srcVisible[i], 4, 4, NearestNeighbor)
		got := outVisible[i]
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if nrgbaAt(got, x, y) != nrgbaAt(want, x, y) {
					t.Errorf("frame %d pixel (%d,%d): got %v, want %v",
						i, x, y, nrgbaAt(got, x, y), nrgbaAt(want, x, y))
				}
			}
		}
	}
}

func TestResizeGIFTransparencyPreserved(t *testing.T) {
	// A single-frame animation with transparent corners.
	pal := make(color.Palette, len(testGIFPalette))
	copy(pal, testGIFPalette)
	frame := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	fillPaletted(frame, frame.Bounds(), testRed)
	frame.SetColorIndex(0, 0, 0)
	frame.SetColorIndex(3, 3, 0)
	src := &gif.GIF{
		Image:  []*image.Paletted{frame},
		Delay:  []int{5},
		Config: image.Config{ColorModel: testGIFPalette, Width: 4, Height: 4},
	}

	out, err := ResizeGIF(src, 2, 2, NearestNeighbor,
		GIFQuantizer(testQuantizer(testGIFPalette[1:])))
	if err != nil {
		t.Fatalf("ResizeGIF: %v", err)
	}

	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, out); err != nil {
		t.Fatalf("EncodeGIF: %v", err)
	}
	decoded, err := DecodeGIF(buf)
	if err != nil {
		t.Fatalf("DecodeGIF: %v", err)
	}
	visible, err := compositeGIFFrames(decoded)
	if err != nil {
		t.Fatalf("compositeGIFFrames: %v", err)
	}
	// Nearest-neighbor 4x4 -> 2x2 samples source pixels (1,1), (3,1), (1,3)
	// and (3,3); only (3,3) is transparent in the source.
	checkPixel(t, visible[0], 0, 0, testColRed)
	checkPixel(t, visible[0], 1, 0, testColRed)
	checkPixel(t, visible[0], 0, 1, testColRed)
	checkPixel(t, visible[0], 1, 1, color.NRGBA{0, 0, 0, 0}) // transparent corner
}

func TestResizeGIFNoConfig(t *testing.T) {
	// A GIF value without a config: the canvas is derived from frame rects.
	pal := make(color.Palette, len(testGIFPalette))
	copy(pal, testGIFPalette)
	frame := image.NewPaletted(image.Rect(0, 0, 6, 6), pal)
	fillPaletted(frame, frame.Bounds(), testGreen)
	src := &gif.GIF{
		Image: []*image.Paletted{frame},
		Delay: []int{7},
	}

	out, err := ResizeGIF(src, 3, 3, NearestNeighbor,
		GIFQuantizer(testQuantizer(testGIFPalette[1:])))
	if err != nil {
		t.Fatalf("ResizeGIF: %v", err)
	}
	if out.Config.Width != 3 || out.Config.Height != 3 {
		t.Errorf("canvas: got %dx%d, want 3x3", out.Config.Width, out.Config.Height)
	}
	if out.Delay[0] != 7 {
		t.Errorf("delay: got %d, want 7", out.Delay[0])
	}
}

func TestEncodeGIFValidation(t *testing.T) {
	pal := make(color.Palette, len(testGIFPalette))
	copy(pal, testGIFPalette)
	validFrame := func() *image.Paletted {
		f := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
		fillPaletted(f, f.Bounds(), testRed)
		return f
	}

	cases := []struct {
		name string
		gif  *gif.GIF
	}{
		{"nil", nil},
		{"no frames", &gif.GIF{}},
		{"nil frame", &gif.GIF{
			Image:  []*image.Paletted{nil},
			Config: image.Config{Width: 4, Height: 4},
		}},
		{"empty palette", &gif.GIF{
			Image:  []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), nil)},
			Config: image.Config{Width: 4, Height: 4},
		}},
		{"empty frame rect", &gif.GIF{
			Image:  []*image.Paletted{image.NewPaletted(image.Rectangle{}, pal)},
			Config: image.Config{Width: 4, Height: 4},
		}},
		{"frame out of canvas", &gif.GIF{
			Image:  []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 8, 8), pal)},
			Config: image.Config{Width: 4, Height: 4},
		}},
	}
	for _, c := range cases {
		if err := EncodeGIF(ioutil.Discard, c.gif); !errors.Is(err, ErrInvalidGIFData) {
			t.Errorf("%s: got %v, want ErrInvalidGIFData", c.name, err)
		}
	}

	// Short delay/disposal slices are padded, not rejected.
	g := &gif.GIF{
		Image:    []*image.Paletted{validFrame(), validFrame()},
		Delay:    []int{11},
		Disposal: []byte{gif.DisposalNone},
		Config:   image.Config{Width: 4, Height: 4},
	}
	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, g); err != nil {
		t.Fatalf("EncodeGIF with short delay/disposal: %v", err)
	}
	decoded, err := gif.DecodeAll(buf)
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(decoded.Image) != 2 || len(decoded.Delay) != 2 {
		t.Fatalf("got %d frames and %d delays, want 2 and 2",
			len(decoded.Image), len(decoded.Delay))
	}
	if decoded.Delay[0] != 11 || decoded.Delay[1] != 0 {
		t.Errorf("delays: got %v, want [11 0]", decoded.Delay)
	}
}

func TestEncodeGIFRoundTrip(t *testing.T) {
	src := makeTestGIF()
	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, src); err != nil {
		t.Fatalf("EncodeGIF: %v", err)
	}

	g, err := DecodeGIF(buf)
	if err != nil {
		t.Fatalf("DecodeGIF: %v", err)
	}
	if len(g.Image) != 4 {
		t.Fatalf("got %d frames, want 4", len(g.Image))
	}
	for i, frame := range g.Image {
		if !frame.Bounds().In(image.Rect(0, 0, 8, 8)) {
			t.Errorf("frame %d bounds %v outside canvas", i, frame.Bounds())
		}
		if len(frame.Palette) == 0 {
			t.Errorf("frame %d: empty palette", i)
		}
	}
	if g.LoopCount != 2 {
		t.Errorf("loop count: got %d, want 2", g.LoopCount)
	}
}

func TestSaveOpenGIF(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging_gif_test")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	filename := filepath.Join(dir, "out.gif")

	src := makeTestGIF()
	if err := SaveGIF(src, filename); err != nil {
		t.Fatalf("SaveGIF: %v", err)
	}

	g, err := OpenGIF(filename)
	if err != nil {
		t.Fatalf("OpenGIF: %v", err)
	}
	if len(g.Image) != 4 {
		t.Fatalf("got %d frames, want 4", len(g.Image))
	}
	if g.LoopCount != 2 {
		t.Errorf("loop count: got %d, want 2", g.LoopCount)
	}

	if _, err := OpenGIF(filepath.Join(dir, "missing.gif")); err == nil {
		t.Error("missing file: expected error, got nil")
	}
}

func TestResizeGIFManyFrames(t *testing.T) {
	// Build a longer animation and verify all frames survive the
	// decode/resize/encode/decode round trip.
	const n = 16
	pal := make(color.Palette, len(testGIFPalette))
	copy(pal, testGIFPalette)

	src := &gif.GIF{
		Config:    image.Config{ColorModel: testGIFPalette, Width: 8, Height: 8},
		LoopCount: -1, // loop forever
	}
	for i := 0; i < n; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
		fillPaletted(frame, frame.Bounds(), uint8(1+i%4))
		src.Image = append(src.Image, frame)
		src.Delay = append(src.Delay, i+1)
	}

	data := encodeTestGIF(t, src)
	g, err := DecodeGIF(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeGIF: %v", err)
	}

	out, err := ResizeGIF(g, 2, 2, NearestNeighbor,
		GIFQuantizer(testQuantizer(testGIFPalette[1:])))
	if err != nil {
		t.Fatalf("ResizeGIF: %v", err)
	}
	if len(out.Image) != n {
		t.Fatalf("got %d frames, want %d", len(out.Image), n)
	}
	if out.LoopCount != -1 {
		t.Errorf("loop count: got %d, want -1", out.LoopCount)
	}
	for i, d := range out.Delay {
		if d != i+1 {
			t.Errorf("delay[%d]: got %d, want %d", i, d, i+1)
		}
	}

	buf := &bytes.Buffer{}
	if err := EncodeGIF(buf, out); err != nil {
		t.Fatalf("EncodeGIF: %v", err)
	}
	decoded, err := DecodeGIF(buf)
	if err != nil {
		t.Fatalf("DecodeGIF: %v", err)
	}
	if len(decoded.Image) != n {
		t.Fatalf("re-decoded: got %d frames, want %d", len(decoded.Image), n)
	}
	visible, err := compositeGIFFrames(decoded)
	if err != nil {
		t.Fatalf("compositeGIFFrames: %v", err)
	}
	wantColors := []color.NRGBA{testColRed, testColGreen, testColBlue, testColYellow}
	for i, frame := range visible {
		checkPixel(t, frame, 0, 0, wantColors[i%4])
		checkPixel(t, frame, 1, 1, wantColors[i%4])
	}
}
