package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"io"
	"io/ioutil"
)

// Limits used by DecodeGIF and OpenGIF to guard against malformed or
// hostile GIF files that declare unreasonable dimensions. They are
// package-level variables so that applications can tune them.
var (
	// MaxGIFCanvasPixels bounds the number of pixels of the GIF logical
	// screen (canvas width * height). Decoding fails before any frame
	// memory is allocated if the declared canvas exceeds this limit.
	MaxGIFCanvasPixels = 1 << 24 // 16777216 pixels (~64 MiB as NRGBA)

	// MaxGIFTotalPixels bounds the total number of pixels of all frames
	// combined (canvas pixels * frame count). Decoding fails if the
	// decoded animation exceeds this limit.
	MaxGIFTotalPixels = 1 << 28 // 268435456 pixels
)

// ErrInvalidGIFData means the GIF data is missing, malformed or describes
// an empty animation.
var ErrInvalidGIFData = errors.New("imaging: invalid GIF data")

// ErrGIFTooLarge means the GIF exceeds the MaxGIFCanvasPixels or
// MaxGIFTotalPixels limit.
var ErrGIFTooLarge = errors.New("imaging: GIF dimensions exceed the allowed limit")

// ErrInvalidGIFSize means a zero or negative target size was requested for
// an animated GIF operation.
var ErrInvalidGIFSize = errors.New("imaging: GIF width and height must be positive")

// DecodeGIF reads an animated GIF from r and returns it with all frames,
// per-frame delays, disposal methods and the loop count intact.
//
// It returns ErrInvalidGIFData for malformed data or animations with no
// frames, and ErrGIFTooLarge if the declared canvas or the total number of
// pixels exceeds the MaxGIFCanvasPixels / MaxGIFTotalPixels limits.
func DecodeGIF(r io.Reader) (*gif.GIF, error) {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}

	cfg, err := gif.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrInvalidGIFData
	}
	canvasPixels := int64(cfg.Width) * int64(cfg.Height)
	if canvasPixels > int64(MaxGIFCanvasPixels) {
		return nil, ErrGIFTooLarge
	}

	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(g.Image) == 0 {
		return nil, ErrInvalidGIFData
	}
	if canvasPixels*int64(len(g.Image)) > int64(MaxGIFTotalPixels) {
		return nil, ErrGIFTooLarge
	}
	return g, nil
}

// OpenGIF loads an animated GIF from file. See DecodeGIF for details.
//
// Example:
//
//	g, err := imaging.OpenGIF("animation.gif")
func OpenGIF(filename string) (*gif.GIF, error) {
	file, err := fs.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return DecodeGIF(file)
}

// gifCanvasSize returns the logical screen size of g, falling back to the
// union of the frame rectangles when the config does not declare it.
func gifCanvasSize(g *gif.GIF) (w, h int, err error) {
	w, h = g.Config.Width, g.Config.Height
	if w > 0 && h > 0 {
		return w, h, nil
	}
	w, h = 0, 0
	for _, frame := range g.Image {
		if frame == nil {
			continue
		}
		if max := frame.Bounds().Max; max.X > w || max.Y > h {
			if max.X > w {
				w = max.X
			}
			if max.Y > h {
				h = max.Y
			}
		}
	}
	if w <= 0 || h <= 0 {
		return 0, 0, ErrInvalidGIFData
	}
	return w, h, nil
}

func gifDelayAt(g *gif.GIF, i int) int {
	if i < len(g.Delay) {
		return g.Delay[i]
	}
	return 0
}

func gifDisposalAt(g *gif.GIF, i int) byte {
	if i < len(g.Disposal) {
		return g.Disposal[i]
	}
	return gif.DisposalNone
}

// gifBackgroundColor returns the background color of g as declared by the
// global palette background index, or transparent if none is available.
func gifBackgroundColor(g *gif.GIF) color.NRGBA {
	if pal, ok := g.Config.ColorModel.(color.Palette); ok {
		if int(g.BackgroundIndex) < len(pal) {
			return color.NRGBAModel.Convert(pal[g.BackgroundIndex]).(color.NRGBA)
		}
	}
	return color.NRGBA{}
}

// compositeGIFFrames renders the visible full-canvas image of every frame
// of g, honoring partial frame rectangles, transparent pixels and the
// background/previous disposal methods.
func compositeGIFFrames(g *gif.GIF) ([]*image.NRGBA, error) {
	w, h, err := gifCanvasSize(g)
	if err != nil {
		return nil, err
	}

	bg := gifBackgroundColor(g)
	canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
	var backup *image.NRGBA

	frames := make([]*image.NRGBA, len(g.Image))
	for i, frame := range g.Image {
		if frame == nil {
			return nil, ErrInvalidGIFData
		}

		// Apply the disposal method of the previous frame.
		if i > 0 {
			switch gifDisposalAt(g, i-1) {
			case gif.DisposalBackground:
				r := g.Image[i-1].Bounds().Intersect(canvas.Bounds())
				if !r.Empty() {
					draw.Draw(canvas, r, &image.Uniform{C: bg}, image.Point{}, draw.Src)
				}
			case gif.DisposalPrevious:
				if backup != nil {
					copy(canvas.Pix, backup.Pix)
				}
			}
		}

		// Snapshot the canvas if the current frame restores it afterwards.
		if gifDisposalAt(g, i) == gif.DisposalPrevious {
			if backup == nil {
				backup = image.NewNRGBA(canvas.Bounds())
			}
			copy(backup.Pix, canvas.Pix)
		}

		if r := frame.Bounds(); !r.Empty() {
			draw.Draw(canvas, r, frame, r.Min, draw.Over)
		}
		frames[i] = Clone(canvas)
	}
	return frames, nil
}

// palettizeGIFFrame converts an NRGBA frame to a paletted image suitable
// for GIF encoding. If the frame contains transparent pixels, the first
// palette entry is reserved for a fully transparent color.
func palettizeGIFFrame(src *image.NRGBA, cfg encodeConfig) *image.Paletted {
	b := src.Bounds()

	numColors := cfg.gifNumColors
	if numColors < 1 || numColors > 256 {
		numColors = 256
	}

	hasTransparent := false
	for i := 3; i < len(src.Pix); i += 4 {
		if src.Pix[i] < 0x80 {
			hasTransparent = true
			break
		}
	}

	pal := make(color.Palette, 0, numColors)
	if hasTransparent {
		pal = append(pal, color.Transparent)
	}
	if cfg.gifQuantizer != nil {
		pal = cfg.gifQuantizer.Quantize(pal, src)
	} else {
		n := numColors - len(pal)
		if n > len(palette.Plan9) {
			n = len(palette.Plan9)
		}
		pal = append(pal, palette.Plan9[:n]...)
	}

	dst := image.NewPaletted(b, pal)
	d := cfg.gifDrawer
	if d == nil {
		d = draw.FloydSteinberg
	}
	d.Draw(dst, b, src, image.Point{})

	if hasTransparent {
		// Map transparent pixels to the reserved transparent palette entry.
		for y := 0; y < b.Dy(); y++ {
			row := y * src.Stride
			for x := 0; x < b.Dx(); x++ {
				if src.Pix[row+x*4+3] < 0x80 {
					dst.Pix[y*dst.Stride+x] = 0
				}
			}
		}
	}
	return dst
}

// ResizeGIF resizes an animated GIF to the specified width and height using
// the specified resampling filter and returns a new animation that plays
// the same as the source: frame order, per-frame delays and the loop count
// are preserved, and the visible picture of every frame (including partial
// frames, transparent pixels and background/previous disposal methods)
// matches the source picture scaled to the target size.
//
// Both width and height must be positive, otherwise ErrInvalidGIFSize is
// returned. The optional encode options control the palette generation of
// the output frames (GIFNumColors, GIFQuantizer and GIFDrawer).
//
// Example:
//
//	dst, err := imaging.ResizeGIF(src, 128, 128, imaging.Lanczos)
func ResizeGIF(g *gif.GIF, width, height int, filter ResampleFilter, opts ...EncodeOption) (*gif.GIF, error) {
	if g == nil || len(g.Image) == 0 {
		return nil, ErrInvalidGIFData
	}
	if width <= 0 || height <= 0 {
		return nil, ErrInvalidGIFSize
	}

	cfg := defaultEncodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	visible, err := compositeGIFFrames(g)
	if err != nil {
		return nil, err
	}

	n := len(visible)
	out := &gif.GIF{
		Image:     make([]*image.Paletted, n),
		Delay:     make([]int, n),
		Disposal:  make([]byte, n), // Full-canvas frames need no disposal.
		LoopCount: g.LoopCount,
	}
	for i, frame := range visible {
		resized := Resize(frame, width, height, filter)
		out.Image[i] = palettizeGIFFrame(resized, cfg)
		out.Delay[i] = gifDelayAt(g, i)
	}
	out.Config = image.Config{
		ColorModel: out.Image[0].Palette,
		Width:      width,
		Height:     height,
	}
	return out, nil
}

// EncodeGIF writes the animated GIF g to w. The animation is validated
// before encoding: it must contain at least one frame, every frame must
// have a non-empty rectangle inside the logical screen and a non-empty
// palette. Delay and disposal slices shorter than the frame count are
// padded with zeros.
func EncodeGIF(w io.Writer, g *gif.GIF) error {
	if g == nil || len(g.Image) == 0 {
		return ErrInvalidGIFData
	}

	w0, h0, err := gifCanvasSize(g)
	if err != nil {
		return err
	}
	canvas := image.Rect(0, 0, w0, h0)

	for _, frame := range g.Image {
		if frame == nil {
			return ErrInvalidGIFData
		}
		if frame.Bounds().Empty() {
			return ErrInvalidGIFData
		}
		if len(frame.Palette) == 0 {
			return ErrInvalidGIFData
		}
		if !frame.Bounds().In(canvas) {
			return ErrInvalidGIFData
		}
	}

	// Encode a normalized copy so the caller's value is left untouched.
	out := *g
	out.Config.Width = w0
	out.Config.Height = h0
	if len(out.Delay) < len(out.Image) {
		delay := make([]int, len(out.Image))
		copy(delay, out.Delay)
		out.Delay = delay
	}
	if out.Disposal != nil && len(out.Disposal) < len(out.Image) {
		disposal := make([]byte, len(out.Image))
		copy(disposal, out.Disposal)
		out.Disposal = disposal
	}
	return gif.EncodeAll(w, &out)
}

// SaveGIF saves the animated GIF g to file. The file is always written in
// GIF format regardless of the filename extension.
//
// Example:
//
//	err := imaging.SaveGIF(g, "out.gif")
func SaveGIF(g *gif.GIF, filename string) (err error) {
	file, err := fs.Create(filename)
	if err != nil {
		return err
	}
	err = EncodeGIF(file, g)
	errc := file.Close()
	if err == nil {
		err = errc
	}
	return err
}
