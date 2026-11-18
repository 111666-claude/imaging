package imaging

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"io"
	"math"
	"time"
)

// GIF frame disposal methods. They define how the area used by a frame is
// treated before the next frame is rendered and match the disposal methods
// of the image/gif package.
const (
	// GIFDisposalNone means that no disposal is specified. The frame is
	// left in place and the next frame is drawn over it.
	GIFDisposalNone = 0

	// GIFDisposalDoNotDispose means that the frame is left in place when
	// the next frame is rendered (GIF disposal method 1). It has the same
	// visible effect as GIFDisposalNone but writes the explicit method 1.
	GIFDisposalDoNotDispose = gif.DisposalNone

	// GIFDisposalBackground means that the area used by the frame is
	// restored to the background (transparent when no opaque background
	// index is defined) before the next frame is rendered.
	GIFDisposalBackground = gif.DisposalBackground

	// GIFDisposalPrevious means that the area used by the frame is
	// restored to what was displayed before the frame was rendered.
	GIFDisposalPrevious = gif.DisposalPrevious
)

// Maximum representable GIF delay and canvas dimension. GIF stores both
// values as unsigned 16-bit integers.
const (
	gifMaxDelay     = time.Duration(math.MaxUint16) * 10 * time.Millisecond
	gifMaxDimension = 1<<16 - 1
	gifCentisecond  = 10 * time.Millisecond
)

// Errors returned by EncodeGIF and SaveGIF.
var (
	// ErrGIFEmptyFrames is returned when the frame list is empty or
	// contains a nil image.
	ErrGIFEmptyFrames = errors.New("imaging: gif: empty frame list")

	// ErrGIFInvalidBounds is returned when a frame has negative bounds or
	// when the combined canvas is empty or exceeds the GIF dimension limit.
	ErrGIFInvalidBounds = errors.New("imaging: gif: invalid frame bounds")

	// ErrGIFInvalidDelay is returned when a frame delay is negative or too
	// large to be represented by the GIF format (more than 65535
	// centiseconds, about 10 minutes 55 seconds).
	ErrGIFInvalidDelay = errors.New("imaging: gif: invalid frame delay")

	// ErrGIFInvalidDisposal is returned when a frame uses a disposal method
	// outside the supported range (0 to 3).
	ErrGIFInvalidDisposal = errors.New("imaging: gif: invalid disposal method")

	// ErrGIFTooManyColors is returned when a frame cannot be reduced to at
	// most 256 palette entries, for example when a custom quantizer produces
	// an oversized palette.
	ErrGIFTooManyColors = errors.New("imaging: gif: frame requires more than 256 colors")
)

// GIFFrame is a single animation frame: the image, the time it is displayed
// and the disposal method applied when the next frame is rendered.
//
// The image bounds do not have to cover the whole animation canvas and do not
// have to be equal between frames. A frame is drawn at the position of its
// bounds' Min point on the common canvas; transparent pixels let the
// previously rendered content show through.
//
// Delay is rounded to the nearest GIF time unit (1/100 of a second); a zero
// delay is stored as zero. Disposal may be set to GIFDisposalNone (or 0) to
// use the disposal given to EncodeGIF.
type GIFFrame struct {
	Image    image.Image
	Delay    time.Duration
	Disposal byte
}

// GIFLoopCount returns an EncodeOption that sets the number of times an
// animated GIF restarts after its first display.
//
// A loopCount of 0 (the default) means that the animation loops forever.
// A negative value means that the animation is displayed only once.
// Otherwise the animation is played loopCount+1 times, matching the
// semantics of image/gif.GIF.LoopCount.
func GIFLoopCount(loopCount int) EncodeOption {
	return func(c *encodeConfig) {
		c.gifLoopCount = loopCount
	}
}

// GIFDisposalMethod returns an EncodeOption that sets the default disposal
// method of animated GIF frames that do not specify their own one.
// It accepts GIFDisposalNone (0), GIFDisposalDoNotDispose (1),
// GIFDisposalBackground (2) or GIFDisposalPrevious (3).
func GIFDisposalMethod(disposal byte) EncodeOption {
	return func(c *encodeConfig) {
		c.gifDisposal = disposal
	}
}

// EncodeGIF writes the given frames to w as an animated GIF that can be read
// back frame by frame by any standard GIF decoder.
//
// The logical canvas is the union of all frame rectangles, so frames may use
// different sizes and positions and may contain transparent pixels. Each
// frame is reduced to its own local palette of at most 256 colors, so frames
// with more than 256 colors are quantized rather than rejected. The GIFNumColors,
// GIFQuantizer and GIFDrawer EncodeOptions control this conversion the same way
// they do for the single-image Encode function. Transparent source pixels are
// preserved through a dedicated transparent palette entry.
//
// Every frame carries its own Delay (rounded to 1/100 of a second) and
// disposal method; the GIFDisposalMethod option supplies the disposal for
// frames whose Disposal field is zero. The GIFLoopCount option controls
// looping.
//
// It returns ErrGIFEmptyFrames for an empty or nil frame list, ErrGIFInvalidBounds
// for negative, empty or oversized frame rectangles, ErrGIFInvalidDelay for
// negative or unrepresentable delays, ErrGIFInvalidDisposal for unsupported
// disposal methods and ErrGIFTooManyColors when a frame cannot fit into the
// 256-color GIF palette. Errors returned by w while writing are passed
// through unchanged.
func EncodeGIF(w io.Writer, frames []GIFFrame, opts ...EncodeOption) error {
	cfg := defaultEncodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	if len(frames) == 0 {
		return ErrGIFEmptyFrames
	}

	if cfg.gifNumColors < 1 || cfg.gifNumColors > 256 {
		return fmt.Errorf("%w: NumColors must be in [1, 256], got %d", ErrGIFTooManyColors, cfg.gifNumColors)
	}

	// Determine the common canvas as the union of the frame rectangles and
	// validate per-frame parameters up front so that no partial output is
	// produced for an invalid frame list.
	canvas := image.Rectangle{}
	for i := range frames {
		frame := &frames[i]
		if frame.Image == nil {
			return fmt.Errorf("%w: frame %d", ErrGIFEmptyFrames, i)
		}
		b := frame.Image.Bounds()
		if b.Min.X < 0 || b.Min.Y < 0 {
			return fmt.Errorf("%w: frame %d has negative origin %v", ErrGIFInvalidBounds, i, b.Min)
		}
		if b.Empty() {
			return fmt.Errorf("%w: frame %d has empty bounds %v", ErrGIFInvalidBounds, i, b)
		}
		if b.Dx() > gifMaxDimension || b.Dy() > gifMaxDimension {
			return fmt.Errorf("%w: frame %d size %dx%d exceeds %d", ErrGIFInvalidBounds, i, b.Dx(), b.Dy(), gifMaxDimension)
		}
		if i == 0 {
			canvas = b
		} else {
			canvas = canvas.Union(b)
		}

		if frame.Delay < 0 {
			return fmt.Errorf("%w: frame %d has negative delay %s", ErrGIFInvalidDelay, i, frame.Delay)
		}
		if frame.Delay > gifMaxDelay {
			return fmt.Errorf("%w: frame %d delay %s exceeds %s", ErrGIFInvalidDelay, i, frame.Delay, gifMaxDelay)
		}

		disposal := frame.Disposal
		if disposal == GIFDisposalNone {
			disposal = cfg.gifDisposal
		}
		if disposal > GIFDisposalPrevious {
			return fmt.Errorf("%w: frame %d uses disposal %d", ErrGIFInvalidDisposal, i, disposal)
		}
	}
	if canvas.Min.X < 0 || canvas.Min.Y < 0 || canvas.Dx() > gifMaxDimension || canvas.Dy() > gifMaxDimension {
		return fmt.Errorf("%w: canvas %v exceeds the GIF limits", ErrGIFInvalidBounds, canvas)
	}

	g := &gif.GIF{
		Image:     make([]*image.Paletted, len(frames)),
		Delay:     make([]int, len(frames)),
		Disposal:  make([]byte, len(frames)),
		LoopCount: cfg.gifLoopCount,
		Config: image.Config{
			Width:  canvas.Dx(),
			Height: canvas.Dy(),
		},
	}

	for i := range frames {
		frame := &frames[i]

		paletted, err := palettize(frame.Image, cfg.gifNumColors, cfg.gifQuantizer, cfg.gifDrawer)
		if err != nil {
			return fmt.Errorf("frame %d: %w", i, err)
		}
		g.Image[i] = paletted
		g.Delay[i] = delayToCentiseconds(frame.Delay)

		disposal := frame.Disposal
		if disposal == GIFDisposalNone {
			disposal = cfg.gifDisposal
		}
		g.Disposal[i] = disposal
	}

	return gif.EncodeAll(w, g)
}

// SaveGIF encodes the frames as an animated GIF and writes the result to the
// named file. The format is not inferred from the filename; the file is always
// written in GIF format.
func SaveGIF(frames []GIFFrame, filename string, opts ...EncodeOption) error {
	file, err := fs.Create(filename)
	if err != nil {
		return err
	}
	err = EncodeGIF(file, frames, opts...)
	errc := file.Close()
	if err == nil {
		err = errc
	}
	return err
}

// delayToCentiseconds rounds d to the nearest GIF delay unit (10ms). The
// caller guarantees that d is non-negative and representable.
func delayToCentiseconds(d time.Duration) int {
	return int(math.Round(float64(d) / float64(gifCentisecond)))
}

// isFullyOpaque reports whether every pixel of img is fully opaque.
func isFullyOpaque(img image.Image) bool {
	if op, ok := img.(interface{ Opaque() bool }); ok {
		return op.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// palettize converts img into a *image.Paletted with at most numColors
// entries, keeping img's bounds (including a non-zero origin) intact.
// Images with transparent pixels get a dedicated transparent palette entry so
// that full transparency is preserved; partial transparency is quantized
// against the palette by drawer.
func palettize(img image.Image, numColors int, quantizer draw.Quantizer, drawer_ draw.Drawer) (*image.Paletted, error) {
	if pm, ok := img.(*image.Paletted); ok && len(pm.Palette) > 0 && len(pm.Palette) <= 256 {
		return pm, nil
	}

	b := img.Bounds()
	opaque := isFullyOpaque(img)

	// One palette slot is reserved for transparency when the image contains
	// transparent pixels.
	paletteCapacity := numColors
	if !opaque && paletteCapacity > 1 {
		paletteCapacity--
	}

	var pal color.Palette
	if quantizer != nil {
		pal = quantizer.Quantize(make(color.Palette, 0, paletteCapacity), img)
	} else {
		if paletteCapacity >= len(palette.Plan9) {
			pal = append(color.Palette(nil), palette.Plan9...)
		} else {
			pal = append(color.Palette(nil), palette.Plan9[:paletteCapacity]...)
		}
	}
	if !opaque {
		pal = append(pal, color.Transparent)
	}
	if len(pal) == 0 {
		pal = color.Palette{color.Black}
	}
	if len(pal) > 256 {
		return nil, fmt.Errorf("%w: palette has %d entries", ErrGIFTooManyColors, len(pal))
	}

	pm := image.NewPaletted(b, pal)
	if drawer_ == nil {
		drawer_ = draw.FloydSteinberg
	}
	drawer_.Draw(pm, b, img, b.Min)
	return pm, nil
}
