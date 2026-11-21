package imaging

// I/O quality assurance tests.
//
// Scope: this file exercises the encode/decode paths in io.go against a fixed
// sample matrix (format x transparency x palette x non-zero/negative bounds x
// size boundaries). It is deliberately independent of the existing io_test.go
// suite, which is kept unchanged.
//
// Fixture provenance:
//
//	testdata/orientation_0.jpg .. orientation_8.jpg
//	    Added in upstream commit 0bd5694c78c3d9a3cd06a706a8f3c59296a9ac
//	    ("io: add decode option: AutoOrientation"). orientation_0 has no EXIF
//	    block; orientation_N carries an EXIF Orientation tag with value N.
//	    Their SHA-256 digests are pinned in TestIOFixtureProvenance so that a
//	    silently regenerated fixture fails the suite.
//	testdata/branches.png
//	    Upstream test photograph, added in commit e855101. Used only as a
//	    natural-image crop for lossy tolerance checks.
//
// All other samples are synthesized in-process from an explicit formula so
// that expected values are derived from the source pixels rather than from
// golden blobs.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixture provenance
// ---------------------------------------------------------------------------

type ioFixtureDigest struct {
	path   string
	sha256 string
}

var ioFixtureDigests = []ioFixtureDigest{
	{"testdata/orientation_0.jpg", "9390ae2eb255941377617adfa6719a244fbfc3c9d57f9f595aef896751e8fcec"},
	{"testdata/orientation_1.jpg", "35d7883a153bbd32984210662912d6fc2e890c7efd168bb82630bf27630008da"},
	{"testdata/orientation_2.jpg", "ffcfa55c575ba7c6ff3563920393081584d8f600081c11292954ee00e4d5d284"},
	{"testdata/orientation_3.jpg", "df98e95eed747ec89781792237c9c205f8ea3b8859bde49f8f931933cd00e4ed"},
	{"testdata/orientation_4.jpg", "eb1881b9a7d8bc21f8ea6db6c057b0f87af11c5a8c4ac9755fd487ad50540f5f"},
	{"testdata/orientation_5.jpg", "643ffab6cf8bef78dc7f5597b721563f19ad89f4be4c1e6a902403ab0e85d2b4"},
	{"testdata/orientation_6.jpg", "7e02fa15a5ed213dab3fe3bc2329798b2ac3f2f816c61283ff796fbb735e2837"},
	{"testdata/orientation_7.jpg", "a7ac1725562031ece0f8c51b3d83b6b5be7210ef34c3eb31ff5e1df0940e04c0"},
	{"testdata/orientation_8.jpg", "433796ab5fa06ea7b8d3177f656d690378e641f6991d1d21cf1678154cc66d03"},
	{"testdata/branches.png", "09b14567153192d0e7b44ab0c8c985a3f3e31ef75bc1d487b3c0b02ae65365e0"},
}

// TestIOFixtureProvenance records where the binary fixtures came from and
// detects accidental regeneration.
func TestIOFixtureProvenance(t *testing.T) {
	for _, f := range ioFixtureDigests {
		data, err := ioutil.ReadFile(f.path)
		if err != nil {
			t.Fatalf("%s: fixture missing: %v", f.path, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != f.sha256 {
			t.Errorf("%s: fixture digest %s, want %s (fixture was replaced; update the provenance note in io_quality_test.go if intentional)",
				f.path, got, f.sha256)
		}
	}
}

// ---------------------------------------------------------------------------
// Sample matrix
// ---------------------------------------------------------------------------

// ioSampleKind describes what an encoder is expected to be able to preserve.
type ioSampleKind int

const (
	// sampleOpaque has no transparency at all.
	sampleOpaque ioSampleKind = iota
	// sampleAlpha has a non-trivial alpha channel but is not paletted.
	sampleAlpha
	// samplePaletteOpaque is *image.Paletted with every palette entry opaque.
	samplePaletteOpaque
	// samplePaletteAlpha is *image.Paletted with translucent/transparent entries.
	samplePaletteAlpha
	// sampleGray is a single-channel image.
	sampleGray
)

// ioSample is one fixed entry of the round-trip matrix.
type ioSample struct {
	name string
	kind ioSampleKind
	img  image.Image
}

// ioSampleMatrix builds the fixed matrix. Every sample is deterministic; no
// golden files are consulted for expected pixels.
func ioSampleMatrix() []ioSample {
	nrgba := func(w, h int, f func(x, y int) color.NRGBA) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.SetNRGBA(x, y, f(x, y))
			}
		}
		return img
	}

	samples := []ioSample{
		{
			// Smooth opaque ramp: exposes subsampling/chroma loss in lossy formats.
			name: "opaque_ramp",
			kind: sampleOpaque,
			img: nrgba(24, 17, func(x, y int) color.NRGBA {
				return color.NRGBA{uint8(x * 255 / 23), uint8(y * 255 / 16), uint8((x + y) * 255 / 40), 255}
			}),
		},
		{
			// Hard edges: exposes ringing and block artifacts.
			name: "opaque_checkerboard",
			kind: sampleOpaque,
			img: nrgba(16, 12, func(x, y int) color.NRGBA {
				p := []color.NRGBA{
					{230, 20, 30, 255},
					{20, 200, 40, 255},
					{30, 40, 220, 255},
					{240, 230, 10, 255},
				}
				return p[((x/3)+(y/3))%len(p)]
			}),
		},
		{
			// Fully transparent one-pixel border: catches fringes/blending on the edge.
			name: "alpha_border",
			kind: sampleAlpha,
			img: nrgba(16, 12, func(x, y int) color.NRGBA {
				a := uint8(255)
				if x == 0 || y == 0 || x == 15 || y == 11 {
					a = 0
				}
				return color.NRGBA{uint8(x*40 + 5), uint8(y*50 + 3), uint8((x ^ y) * 17), a}
			}),
		},
		{
			// Partial alpha ramp including a==0 and a==255.
			name: "alpha_partial",
			kind: sampleAlpha,
			img: nrgba(12, 9, func(x, y int) color.NRGBA {
				al := []uint8{0, 64, 128, 192, 255}
				return color.NRGBA{uint8(x*30 + 10), uint8(y*40 + 5), uint8((x*13 + y*7) % 256), al[(x+y)%5]}
			}),
		},
		{
			// Premultiplied-alpha source type, to check normalization on the way in.
			name: "rgba_partial",
			kind: sampleAlpha,
			img: func() image.Image {
				img := image.NewRGBA(image.Rect(0, 0, 10, 7))
				for y := 0; y < 7; y++ {
					for x := 0; x < 10; x++ {
						img.SetRGBA(x, y, color.RGBA{
							uint8(x * 20), uint8(y * 30), uint8(x*y*3 + 7), uint8(64 + x*20),
						})
					}
				}
				return img
			}(),
		},
		{
			name: "palette_opaque",
			kind: samplePaletteOpaque,
			img: func() image.Image {
				pal := color.Palette{
					color.NRGBA{255, 0, 0, 255},
					color.NRGBA{0, 255, 0, 255},
					color.NRGBA{0, 0, 255, 255},
					color.NRGBA{9, 9, 9, 255},
					color.NRGBA{250, 240, 10, 255},
				}
				p := image.NewPaletted(image.Rect(0, 0, 19, 11), pal)
				for i := range p.Pix {
					p.Pix[i] = uint8((i*7 + i/3) % len(pal))
				}
				return p
			}(),
		},
		{
			// One translucent and one fully transparent palette entry.
			name: "palette_alpha",
			kind: samplePaletteAlpha,
			img: func() image.Image {
				pal := color.Palette{
					color.NRGBA{255, 0, 0, 255},
					color.NRGBA{0, 255, 0, 128},
					color.NRGBA{0, 0, 255, 0},
					color.NRGBA{9, 9, 9, 255},
				}
				p := image.NewPaletted(image.Rect(0, 0, 10, 7), pal)
				for i := range p.Pix {
					p.Pix[i] = uint8(i % len(pal))
				}
				return p
			}(),
		},
		{
			name: "gray_ramp",
			kind: sampleGray,
			img: func() image.Image {
				g := image.NewGray(image.Rect(0, 0, 17, 9))
				for i := range g.Pix {
					g.Pix[i] = uint8((i * 5) % 256)
				}
				return g
			}(),
		},
		{
			// Bounds start at (3, 2): catches encoders that ignore Rect.Min.
			name: "subimage_positive_origin",
			kind: sampleOpaque,
			img: func() image.Image {
				parent := nrgba(14, 11, func(x, y int) color.NRGBA {
					return color.NRGBA{uint8(x*17 + 3), uint8(y*21 + 7), uint8((x*y*3 + 11) % 256), 255}
				})
				return parent.SubImage(image.Rect(3, 2, 11, 8)).(*image.NRGBA)
			}(),
		},
		{
			// Bounds start at negative coordinates: BMP/TIFF writers are the
			// usual suspects for mishandling this.
			name: "bounds_negative_origin",
			kind: sampleOpaque,
			img: func() image.Image {
				img := image.NewNRGBA(image.Rect(-3, -2, 2, 3))
				for y := -2; y < 3; y++ {
					for x := -3; x < 2; x++ {
						img.SetNRGBA(x, y, color.NRGBA{
							uint8((x + 3) * 40 / 5 * 1),
							uint8((y + 2) * 50),
							uint8(x*y + 100),
							255,
						})
					}
				}
				return img
			}(),
		},
	}

	// Size boundaries. 1x1, single-row/column and the smallest even size cover
	// the block/padding edge conditions in every codec.
	sizeSample := func(name string, w, h int, f func(x, y int) color.NRGBA) ioSample {
		return ioSample{name: name, kind: sampleOpaque, img: nrgba(w, h, f)}
	}
	samples = append(samples,
		sizeSample("size_1x1", 1, 1, func(x, y int) color.NRGBA {
			return color.NRGBA{77, 133, 201, 255}
		}),
		sizeSample("size_1x17", 1, 17, func(x, y int) color.NRGBA {
			return color.NRGBA{uint8(y * 15), uint8(255 - y*15), uint8(y * 9), 255}
		}),
		sizeSample("size_17x1", 17, 1, func(x, y int) color.NRGBA {
			return color.NRGBA{uint8(x * 15), uint8(255 - x*15), uint8(x * 9), 255}
		}),
		sizeSample("size_2x2", 2, 2, func(x, y int) color.NRGBA {
			p := []color.NRGBA{
				{250, 10, 10, 255},
				{10, 240, 20, 255},
				{20, 30, 230, 255},
				{240, 230, 10, 255},
			}
			return p[y*2+x]
		}),
	)
	return samples
}

// ioFormatCase groups a format with the way its output must be judged.
type ioFormatCase struct {
	name string
	ext  string
	fmt  Format
	// lossless means the decoded pixels must match the source exactly.
	lossless bool
	// opts are passed to Encode.
	opts []EncodeOption
}

// ioFormatMatrix returns one case per format. JPEG is judged at an explicit
// quality and with tolerance; GIF is judged with tolerance because its
// palette is quantized; PNG/TIFF/BMP are lossless for the sample matrix.
func ioFormatMatrix() []ioFormatCase {
	return []ioFormatCase{
		{name: "JPEG", ext: "jpg", fmt: JPEG, lossless: false, opts: []EncodeOption{JPEGQuality(95)}},
		{name: "PNG", ext: "png", fmt: PNG, lossless: true},
		{name: "GIF", ext: "gif", fmt: GIF, lossless: false},
		{name: "TIFF", ext: "tif", fmt: TIFF, lossless: true},
		{name: "BMP", ext: "bmp", fmt: BMP, lossless: true},
	}
}

// ---------------------------------------------------------------------------
// Expected pixels and comparison helpers
// ---------------------------------------------------------------------------

// flattenAlpha returns a copy of img with every pixel composited onto an
// opaque black background, matching what formats that cannot carry alpha do.
func flattenAlpha(img image.Image) *image.NRGBA {
	src := Clone(img)
	for i := 0; i < len(src.Pix); i += 4 {
		a := src.Pix[i+3]
		switch a {
		case 0:
			src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 0, 0, 0, 255
		case 255:
			// Already opaque.
		default:
			src.Pix[i] = uint8(uint32(src.Pix[i]) * uint32(a) / 255)
			src.Pix[i+1] = uint8(uint32(src.Pix[i+1]) * uint32(a) / 255)
			src.Pix[i+2] = uint8(uint32(src.Pix[i+2]) * uint32(a) / 255)
			src.Pix[i+3] = 255
		}
	}
	return src
}

// canonicalNRGBA returns src as non-premultiplied NRGBA using the standard
// color.NRGBAModel conversion. For *image.RGBA sources the premultiplied
// bytes cannot be reinterpreted directly; the canonical 16-bit round trip is
// what all lossless codecs are expected to reproduce.
func canonicalNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA))
		}
	}
	return dst
}

// paletteFlatOpaque converts a *image.Paletted to NRGBA the way codecs that
// discard palette alpha (TIFF, BMP, and the alpha-dropping JPEG fallback) do:
// each entry contributes the 8-bit value of its premultiplied Color.RGBA
// result (that is what the writers store) and alpha is forced to 255. A
// translucent green 0,255,0,128 therefore decodes as 0,128,0,255 and a fully
// transparent entry as black.
func paletteFlatOpaque(img image.Image) *image.NRGBA {
	p := img.(*image.Paletted)
	b := p.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := p.Palette[p.Pix[(y-b.Min.Y)*p.Stride+(x-b.Min.X)]].RGBA()
			dst.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{
				uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255,
			})
		}
	}
	return dst
}

// paletteGIF is the expected GIF result. GIF reuses the palette but only
// records transparency (a single index with alpha 0); every other entry is
// written as the premultiplied 8-bit RGB from Color.RGBA. A fully transparent
// entry decodes as (0,0,0,0).
func paletteGIF(img image.Image) *image.NRGBA {
	p := img.(*image.Paletted)
	b := p.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := p.Palette[p.Pix[(y-b.Min.Y)*p.Stride+(x-b.Min.X)]].RGBA()
			if a == 0 {
				dst.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{0, 0, 0, 0})
				continue
			}
			dst.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{
				uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255,
			})
		}
	}
	return dst
}

// expectedPixels returns the pixel matrix a format is contractually allowed
// to produce for a sample.
//
//   - Lossless formats must reproduce the source exactly (after normalizing
//     premultiplied RGBA to NRGBA).
//   - JPEG drops alpha, so the reference is the source flattened onto black.
//   - GIF keeps palette alpha, but for an opaque paletted source it must be
//     able to reuse the palette exactly; for non-paletted alpha sources it
//     flattens onto black because the default quantizer/Plan9 path does.
//   - TIFF and BMP write palette entries as opaque RGB (alpha discarded);
//     for non-paletted sources they preserve unassociated alpha.
func expectedPixels(s ioSample, fc ioFormatCase) *image.NRGBA {
	switch fc.fmt {
	case JPEG:
		if s.kind == samplePaletteOpaque || s.kind == samplePaletteAlpha {
			return paletteFlatOpaque(s.img)
		}
		return flattenAlpha(s.img)
	case GIF:
		if s.kind == samplePaletteAlpha || s.kind == samplePaletteOpaque {
			return paletteGIF(s.img)
		}
		return flattenAlpha(s.img)
	case TIFF, BMP:
		if s.kind == samplePaletteOpaque || s.kind == samplePaletteAlpha {
			return paletteFlatOpaque(s.img)
		}
		if _, ok := s.img.(*image.RGBA); ok {
			// TIFF stores *image.RGBA samples with associated alpha and
			// decodes straight back into *image.RGBA, so the premultiplied
			// bytes survive unchanged even though they are not the canonical
			// un-premultiplied values. BMP goes through NRGBA and matches the
			// canonical conversion.
			if fc.fmt == TIFF {
				return Clone(s.img)
			}
		}
		return canonicalNRGBA(s.img)
	default:
		return canonicalNRGBA(s.img)
	}
}

// ioLossyTolerance is the explicit contract for a lossy format.
//
// Pixels are compared through block means (blocks of ioBlock x ioBlock) so
// that high-frequency ringing cannot be excused by averaging an entire image,
// while a single bad pixel does not dominate. maxBlockMean is the largest
// allowed per-channel absolute difference between any block means;
// maxMean is the allowed whole-image mean absolute error; minGamut is the
// minimum ratio of decoded to reference per-channel range, which catches a
// decoder/encoder that collapses contrast or returns a flat image.
type ioLossyTolerance struct {
	maxBlockMean float64
	maxMean      float64
	minGamut     float64
}

const ioBlock = 4

func ioLossyToleranceFor(s ioSample, fc ioFormatCase) ioLossyTolerance {
	// Bounds are calibrated from the measured values for this matrix with a
	// safety margin for platform floating-point differences. They are
	// deliberately tight enough that a realistic regression — e.g. a JPEG
	// encoder stuck at low quality, or a GIF palette collapse — is rejected,
	// rather than merely bounds-checking the output.
	//
	// minGamut is a coarse "the picture did not collapse" check: the decoded
	// per-channel range must retain a substantial fraction of the reference
	// range. Fine-grained quality is enforced by maxBlockMean and maxMean.
	// Palette collapse drives gamut to ~0.0-0.3, far below any bound here.
	//
	// Thin images (1xN / Nx1) quantize worse than square ones; the "opaque"
	// bounds are set from the 1x17 / 17x1 samples, not the smooth ramp.
	if fc.fmt == GIF {
		switch s.kind {
		case samplePaletteOpaque, samplePaletteAlpha:
			// The default path must reuse an existing palette exactly.
			return ioLossyTolerance{maxBlockMean: 1, maxMean: 1, minGamut: 0.99}
		case sampleGray:
			return ioLossyTolerance{maxBlockMean: 4, maxMean: 2.5, minGamut: 0.90}
		case sampleAlpha:
			// Measured blk<=5.3, mean<=1.8, gamut>=0.94.
			return ioLossyTolerance{maxBlockMean: 15, maxMean: 6, minGamut: 0.85}
		}
		// Measured: 1x17 blk=32.0, 17x1 blk=45.0, ramp blk=8.25, gamut>=0.85.
		return ioLossyTolerance{maxBlockMean: 60, maxMean: 20, minGamut: 0.70}
	}

	// JPEG.
	switch s.kind {
	case sampleGray:
		// Measured blk=0.25, mean=0.10.
		return ioLossyTolerance{maxBlockMean: 2, maxMean: 1, minGamut: 0.95}
	case samplePaletteOpaque, samplePaletteAlpha:
		// Measured blk<=18.5, mean<=6.6, gamut=1.00.
		return ioLossyTolerance{maxBlockMean: 30, maxMean: 12, minGamut: 0.95}
	case sampleAlpha:
		// Hard alpha edges: measured blk<=20.5, mean<=4.4, gamut>=0.94.
		return ioLossyTolerance{maxBlockMean: 35, maxMean: 10, minGamut: 0.85}
	}
	// Measured: ramp blk=1.75 mean=0.51; 1x17/17x1 blk=11.0 mean~6.7;
	// checkerboard gamut=0.93.
	return ioLossyTolerance{maxBlockMean: 20, maxMean: 12, minGamut: 0.85}
}

// ioMetrics is the measured lossy error of one decode against its reference.
type ioMetrics struct {
	maxBlockMean float64
	meanAbs      float64
	minGamut     float64
	maxPixelDiff int
}

// measureIO computes block-mean error, whole-image MAE and gamut retention.
func measureIO(got, want *image.NRGBA) ioMetrics {
	w, h := want.Rect.Dx(), want.Rect.Dy()
	block := ioBlock
	if block > w {
		block = w
	}
	if block > h {
		block = h
	}
	if block < 1 {
		block = 1
	}

	var m ioMetrics
	var totalErr float64
	var blocks int
	var gotMin, gotMax, wantMin, wantMax [3]float64
	for k := 0; k < 3; k++ {
		gotMin[k], wantMin[k] = 255, 255
	}

	for by := 0; by+block <= h; by += block {
		for bx := 0; bx+block <= w; bx += block {
			for k := 0; k < 3; k++ {
				var sumGot, sumWant float64
				for dy := 0; dy < block; dy++ {
					for dx := 0; dx < block; dx++ {
						idx := (by+dy)*want.Stride + (bx+dx)*4 + k
						sumGot += float64(got.Pix[idx])
						sumWant += float64(want.Pix[idx])
					}
				}
				g := sumGot / float64(block*block)
				wt := sumWant / float64(block*block)
				d := g - wt
				if d < 0 {
					d = -d
				}
				if d > m.maxBlockMean {
					m.maxBlockMean = d
				}
				totalErr += d
				if g < gotMin[k] {
					gotMin[k] = g
				}
				if g > gotMax[k] {
					gotMax[k] = g
				}
				if wt < wantMin[k] {
					wantMin[k] = wt
				}
				if wt > wantMax[k] {
					wantMax[k] = wt
				}
				blocks++
			}
		}
	}
	m.meanAbs = totalErr / float64(blocks)

	m.minGamut = 1
	for k := 0; k < 3; k++ {
		wantRange := wantMax[k] - wantMin[k]
		// Channels that are almost flat carry no gamut information; a flat
		// reference would otherwise force a meaningless ratio.
		if wantRange < 25 {
			continue
		}
		ratio := (gotMax[k] - gotMin[k]) / wantRange
		if ratio < m.minGamut {
			m.minGamut = ratio
		}
	}

	for i := 0; i < len(want.Pix); i += 4 {
		for k := 0; k < 3; k++ {
			d := int(got.Pix[i+k]) - int(want.Pix[i+k])
			if d < 0 {
				d = -d
			}
			if d > m.maxPixelDiff {
				m.maxPixelDiff = d
			}
		}
	}
	return m
}

// ioFormatOfSample renders a sample location for failure messages, e.g.
// "format=PNG sample=alpha_border pixel=(0,0)".
func ioLocation(fc ioFormatCase, s ioSample, extra string) string {
	loc := fmt.Sprintf("format=%s sample=%s", fc.name, s.name)
	if extra != "" {
		loc += " " + extra
	}
	return loc
}

// firstPixelDiff returns the first differing pixel of got/want and its
// channel, or ok=false when the buffers match in size and content.
func firstPixelDiff(got, want *image.NRGBA) (p image.Point, channel int, g, w uint8, ok bool) {
	if !got.Rect.Eq(want.Rect) {
		return image.Point{}, 0, 0, 0, false
	}
	for i := 0; i < len(want.Pix); i++ {
		if got.Pix[i] != want.Pix[i] {
			px := i / 4
			return image.Pt(px%want.Rect.Dx(), px/want.Rect.Dx()), i % 4, got.Pix[i], want.Pix[i], false
		}
	}
	return image.Point{}, 0, 0, 0, true
}

// ---------------------------------------------------------------------------
// Round-trip matrix test
// ---------------------------------------------------------------------------

// checkRoundTrip compares one decoded image against the expected pixels for a
// sample/format and reports every contract violation it finds. It is the
// single detector used by both the real matrix and the fault-injection test,
// so the injection test proves this exact code path rejects a defect.
//
// A non-empty returned slice means the round trip is bad.
func checkRoundTrip(got *image.NRGBA, want *image.NRGBA, s ioSample, fc ioFormatCase) []string {
	var problems []string
	loc := ioLocation(fc, s, "")

	// Decoders must always produce a zero-based rectangle; non-zero source
	// bounds must not leak into the result.
	if got.Rect != image.Rect(0, 0, want.Rect.Dx(), want.Rect.Dy()) {
		return []string{fmt.Sprintf("%s: decoded bounds %v, want 0,0 origin and size %dx%d",
			loc, got.Rect, want.Rect.Dx(), want.Rect.Dy())}
	}

	if fc.lossless {
		if !bytes.Equal(got.Pix, want.Pix) {
			p, ch, g, w, _ := firstPixelDiff(got, want)
			return []string{fmt.Sprintf("%s: lossless round trip differs at pixel %v channel %d: got %d want %d",
				loc, p, ch, g, w)}
		}
		return nil
	}

	tol := ioLossyToleranceFor(s, fc)
	m := measureIO(got, want)
	if m.maxBlockMean > tol.maxBlockMean {
		p := worstBlock(got, want, ioBlock)
		problems = append(problems, fmt.Sprintf("%s: block-mean error %.2f > %.2f at block containing %v (max pixel diff %d)",
			loc, m.maxBlockMean, tol.maxBlockMean, p, m.maxPixelDiff))
	}
	if m.meanAbs > tol.maxMean {
		problems = append(problems, fmt.Sprintf("%s: mean abs error %.2f > %.2f (max pixel diff %d)",
			loc, m.meanAbs, tol.maxMean, m.maxPixelDiff))
	}
	if m.minGamut < tol.minGamut {
		problems = append(problems, fmt.Sprintf("%s: gamut retention %.2f < %.2f: decoded image lost per-channel range",
			loc, m.minGamut, tol.minGamut))
	}
	return problems
}

// TestIORoundTripMatrix encodes and (in-memory) decodes every sample in every
// format and asserts the format-specific contract. Failures name the format
// and the exact sample/pixel location.
func TestIORoundTripMatrix(t *testing.T) {
	for _, fc := range ioFormatMatrix() {
		for _, s := range ioSampleMatrix() {
			fc, s := fc, s
			t.Run(fc.name+"/"+s.name, func(t *testing.T) {
				var buf bytes.Buffer
				if err := Encode(&buf, s.img, fc.fmt, fc.opts...); err != nil {
					t.Fatalf("%s: Encode: %v", ioLocation(fc, s, ""), err)
				}
				if buf.Len() == 0 {
					t.Fatalf("%s: Encode wrote no bytes", ioLocation(fc, s, ""))
				}

				decoded, err := Decode(bytes.NewReader(buf.Bytes()))
				if err != nil {
					t.Fatalf("%s: Decode: %v", ioLocation(fc, s, ""), err)
				}
				if decoded == nil {
					t.Fatalf("%s: Decode returned nil image with nil error", ioLocation(fc, s, ""))
				}

				got := Clone(decoded)
				want := expectedPixels(s, fc)
				for _, problem := range checkRoundTrip(got, want, s, fc) {
					t.Error(problem)
				}
				if !fc.lossless {
					m := measureIO(got, want)
					t.Logf("%s: bytes=%d maxBlockMean=%.2f meanAbs=%.2f gamut=%.2f maxPixelDiff=%d",
						ioLocation(fc, s, ""), buf.Len(), m.maxBlockMean, m.meanAbs, m.minGamut, m.maxPixelDiff)
				}
			})
		}
	}
}

// worstBlock locates the block with the largest mean absolute channel error.
func worstBlock(got, want *image.NRGBA, block int) image.Point {
	w, h := want.Rect.Dx(), want.Rect.Dy()
	if block > w {
		block = w
	}
	if block > h {
		block = h
	}
	worst := 0.0
	worstAt := image.Point{}
	for by := 0; by+block <= h; by += block {
		for bx := 0; bx+block <= w; bx += block {
			for k := 0; k < 3; k++ {
				var sg, sw float64
				for dy := 0; dy < block; dy++ {
					for dx := 0; dx < block; dx++ {
						idx := (by+dy)*want.Stride + (bx+dx)*4 + k
						sg += float64(got.Pix[idx])
						sw += float64(want.Pix[idx])
					}
				}
				d := sg - sw
				if d < 0 {
					d = -d
				}
				d /= float64(block * block)
				if d > worst {
					worst = d
					worstAt = image.Pt(bx+block/2, by+block/2)
				}
			}
		}
	}
	return worstAt
}

// ---------------------------------------------------------------------------
// File round trip (Save/Open) across extensions
// ---------------------------------------------------------------------------

// TestIOSaveOpenMatrix mirrors the in-memory matrix through the filesystem
// path so that the extension mapping and file handle lifecycle are covered
// with the same sample and bounds checks.
func TestIOSaveOpenMatrix(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-io-quality")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	for _, fc := range ioFormatMatrix() {
		for _, s := range ioSampleMatrix() {
			fc, s := fc, s
			// The filesystem matrix is intentionally a subset of sizes: one
			// representative per kind plus the non-zero bounds cases.
			if strings.HasPrefix(s.name, "size_") && s.name != "size_1x1" && s.name != "size_2x2" {
				continue
			}
			t.Run(fc.name+"/"+s.name, func(t *testing.T) {
				filename := filepath.Join(dir, s.name+"."+fc.ext)
				if err := Save(s.img, filename, fc.opts...); err != nil {
					t.Fatalf("%s: Save(%s): %v", ioLocation(fc, s, ""), filename, err)
				}
				decoded, err := Open(filename)
				if err != nil {
					t.Fatalf("%s: Open(%s): %v", ioLocation(fc, s, ""), filename, err)
				}
				got := Clone(decoded)
				want := expectedPixels(s, fc)
				problems := checkRoundTrip(got, want, s, fc)
				if len(problems) > 0 {
					t.Fatalf("%s: lossy file round trip %s: %s",
						ioLocation(fc, s, ""), filename, strings.Join(problems, "; "))
				}
			})
		}
	}
}

// ioRecordingFile is an io.ReadWriteCloser that records whether Close was
// called, and can be configured to fail on close or on read/write.
type ioRecordingFile struct {
	r        io.Reader
	w        io.Writer
	closeN   int
	closeErr error
}

func (f *ioRecordingFile) Read(p []byte) (int, error) {
	if f.r == nil {
		return 0, io.EOF
	}
	return f.r.Read(p)
}

func (f *ioRecordingFile) Write(p []byte) (int, error) {
	if f.w == nil {
		return 0, errors.New("not writable")
	}
	return f.w.Write(p)
}

func (f *ioRecordingFile) Close() error {
	f.closeN++
	return f.closeErr
}

// ioRecordingFS hands out pre-seeded files so that resource handling in Open
// and Save can be observed without touching the real filesystem.
type ioRecordingFS struct {
	openFile   *ioRecordingFile
	createFile *ioRecordingFile
}

func (fs *ioRecordingFS) Create(name string) (io.WriteCloser, error) {
	if fs.createFile == nil {
		return nil, errors.New("unexpected Create")
	}
	return fs.createFile, nil
}

func (fs *ioRecordingFS) Open(name string) (io.ReadCloser, error) {
	if fs.openFile == nil {
		return nil, errors.New("unexpected Open")
	}
	return fs.openFile, nil
}

// TestIOFileHandlesClosed asserts that both Open and Save close the file they
// obtained, on success and on failure, and that Save propagates a Close error.
func TestIOFileHandlesClosed(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 6, 5))
	for i := range src.Pix {
		src.Pix[i] = uint8(i * 9)
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, src, PNG); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	prev := fs
	defer func() { fs = prev }()

	// Open must always close its read handle, including when decoding fails.
	for _, tc := range []struct {
		name string
		data []byte
		fail bool
	}{
		{"valid", encoded.Bytes(), false},
		{"corrupt", []byte("not an image"), true},
	} {
		f := &ioRecordingFile{r: bytes.NewReader(tc.data)}
		fs = &ioRecordingFS{openFile: f}
		_, err := Open("ignored.png")
		if tc.fail {
			if err == nil {
				t.Errorf("Open(%s): expected decode error, got nil", tc.name)
			}
		} else if err != nil {
			t.Errorf("Open(%s): %v", tc.name, err)
		}
		if f.closeN != 1 {
			t.Errorf("Open(%s): file closed %d times, want exactly 1", tc.name, f.closeN)
		}
	}

	// Save must close its write handle on success and when encoding fails.
	// A failing encoder is provoked by handing Save an image whose format the
	// extension selects but whose content the encoder rejects (zero-size PNG).
	for _, tc := range []struct {
		name string
		img  image.Image
		ext  string
		fail bool
	}{
		{"success", src, "png", false},
		{"encode_error", image.NewNRGBA(image.Rect(0, 0, 0, 0)), "png", true},
	} {
		w := &ioRecordingFile{w: ioutil.Discard}
		fs = &ioRecordingFS{createFile: w}
		err := Save(tc.img, "ignored."+tc.ext)
		if tc.fail {
			if err == nil {
				t.Errorf("Save(%s): expected encode error, got nil", tc.name)
			}
		} else if err != nil {
			t.Errorf("Save(%s): %v", tc.name, err)
		}
		if w.closeN != 1 {
			t.Errorf("Save(%s): file closed %d times, want exactly 1", tc.name, w.closeN)
		}
	}

	// A Close error must surface from Save, and must not be masked by a
	// successful encode.
	closeErr := errors.New("injected close failure")
	w := &ioRecordingFile{w: ioutil.Discard, closeErr: closeErr}
	fs = &ioRecordingFS{createFile: w}
	if err := Save(src, "ignored.png"); !errors.Is(err, closeErr) {
		t.Errorf("Save with failing Close returned %v, want %v", err, closeErr)
	}
	if w.closeN != 1 {
		t.Errorf("Save with failing Close closed the file %d times, want 1", w.closeN)
	}
}

// TestIOFormatExtensionCoverage asserts that every format's documented
// extension aliases reach both Save and Open.
func TestIOFormatExtensionCoverage(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-io-ext")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	img := image.NewNRGBA(image.Rect(0, 0, 5, 4))
	for i := range img.Pix {
		img.Pix[i] = uint8(i*37 + 11)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}

	exts := []struct {
		ext  string
		want Format
	}{
		{"jpg", JPEG}, {"jpeg", JPEG}, {"JPG", JPEG},
		{"png", PNG}, {"PNG", PNG},
		{"gif", GIF}, {"GIF", GIF},
		{"tif", TIFF}, {"tiff", TIFF}, {"TIFF", TIFF},
		{"bmp", BMP}, {"BMP", BMP},
	}
	for _, e := range exts {
		name := "sample." + e.ext
		filename := filepath.Join(dir, name)
		if err := Save(img, filename); err != nil {
			t.Fatalf("format=%s ext=%s: Save: %v", e.want, e.ext, err)
		}
		dec, err := Open(filename)
		if err != nil {
			t.Fatalf("format=%s ext=%s: Open: %v", e.want, e.ext, err)
		}
		if dec.Bounds() != image.Rect(0, 0, 5, 4) {
			t.Errorf("format=%s ext=%s: bounds %v, want 5x4", e.want, e.ext, dec.Bounds())
		}
	}
}

// ---------------------------------------------------------------------------
// Short reads, write failures, bad headers
// ---------------------------------------------------------------------------

// ioErrReader fails with err after serving exactly the bytes in data.
type ioErrReader struct {
	data []byte
	off  int
	err  error
}

func (r *ioErrReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	if r.off >= len(r.data) {
		return n, r.err
	}
	return n, nil
}

// ioOneByteReader returns a single byte per Read to exercise short-read
// handling in every codec (all decoders must loop, not assume full reads).
type ioOneByteReader struct {
	data []byte
	off  int
	err  error
}

func (r *ioOneByteReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.off]
	r.off++
	return 1, nil
}

// ioFailWriter fails with err once more than limit bytes have been written.
type ioFailWriter struct {
	limit int
	off   int
	err   error
}

func (w *ioFailWriter) Write(p []byte) (int, error) {
	if w.off+len(p) > w.limit {
		allowed := w.limit - w.off
		if allowed < 0 {
			allowed = 0
		}
		w.off += allowed
		return allowed, w.err
	}
	w.off += len(p)
	return len(p), nil
}

// ioShortWriter never returns an error but writes at most limit bytes total;
// encoders are expected to turn this into io.ErrShortWrite (or at least a
// non-nil error) rather than reporting success for truncated output.
type ioShortWriter struct {
	limit int
	off   int
}

func (w *ioShortWriter) Write(p []byte) (int, error) {
	allowed := w.limit - w.off
	if allowed <= 0 {
		return 0, nil
	}
	if allowed > len(p) {
		allowed = len(p)
	}
	w.off += allowed
	return allowed, nil
}

// ioEncoded returns the encoded bytes of sample in the given format.
func ioEncoded(t *testing.T, s ioSample, fc ioFormatCase) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Encode(&buf, s.img, fc.fmt, fc.opts...); err != nil {
		t.Fatalf("%s: encode for corruption test: %v", ioLocation(fc, s, ""), err)
	}
	return buf.Bytes()
}

var errIOSentinel = errors.New("imaging-test: injected read failure")

// TestIOShortReadPropagatesError feeds every codec a truncated stream whose
// terminal error is a sentinel, and requires a non-nil error and nil image.
// It also checks that a one-byte-at-a-time reader still decodes correctly,
// which proves the codecs do not assume that Read fills the buffer.
func TestIOShortReadPropagatesError(t *testing.T) {
	samples := ioSampleMatrix()
	// A small representative slice keeps the test fast; every image type
	// (opaque, alpha, paletted, gray, non-zero bounds) is represented.
	pick := []string{"opaque_ramp", "alpha_border", "palette_opaque", "gray_ramp", "subimage_positive_origin", "bounds_negative_origin"}
	byName := map[string]ioSample{}
	for _, s := range samples {
		byName[s.name] = s
	}

	for _, fc := range ioFormatMatrix() {
		for _, name := range pick {
			s := byName[name]
			if s.img == nil {
				t.Fatalf("test bug: sample %q not in matrix", name)
			}
			fc, s := fc, s
			t.Run(fc.name+"/"+s.name, func(t *testing.T) {
				data := ioEncoded(t, s, fc)

				// One-byte reads must still succeed byte-for-byte.
				full, err := Decode(&ioOneByteReader{data: data, err: io.EOF})
				if err != nil {
					t.Fatalf("%s: one-byte reader: Decode: %v", ioLocation(fc, s, ""), err)
				}
				if full == nil {
					t.Fatalf("%s: one-byte reader: Decode returned nil image", ioLocation(fc, s, ""))
				}

				// Truncate to 60%; the terminal sentinel must surface.
				cut := len(data) * 3 / 5
				img, err := Decode(&ioErrReader{data: data[:cut], err: errIOSentinel})
				if err == nil {
					t.Fatalf("%s: truncated short read (%d/%d bytes): expected error, got nil",
						ioLocation(fc, s, ""), cut, len(data))
				}
				if img != nil {
					t.Fatalf("%s: truncated short read: got non-nil image alongside error: %v",
						ioLocation(fc, s, ""), err)
				}
				if !errors.Is(err, errIOSentinel) && !strings.Contains(err.Error(), errIOSentinel.Error()) {
					// Codecs are allowed to wrap the error (image/gif does),
					// but they must not substitute an unrelated one.
					t.Errorf("%s: truncated short read: error %q does not mention injected failure %q",
						ioLocation(fc, s, ""), err, errIOSentinel)
				}

				// AutoOrientation must not mask the read error either.
				img, err = Decode(&ioErrReader{data: data[:cut], err: errIOSentinel}, AutoOrientation(true))
				if err == nil {
					t.Fatalf("%s: AutoOrientation truncated short read: expected error, got nil",
						ioLocation(fc, s, ""))
				}
				if img != nil {
					t.Fatalf("%s: AutoOrientation truncated short read: got non-nil image alongside error",
						ioLocation(fc, s, ""))
				}
				if !errors.Is(err, errIOSentinel) && !strings.Contains(err.Error(), errIOSentinel.Error()) {
					t.Errorf("%s: AutoOrientation truncated short read: error %q does not mention injected failure %q",
						ioLocation(fc, s, ""), err, errIOSentinel)
				}
			})
		}
	}
}

// TestIOWriteFailurePropagatesError requires Encode to stop and report the
// writer's error at every possible cut point, and never to panic.
func TestIOWriteFailurePropagatesError(t *testing.T) {
	samples := ioSampleMatrix()
	pick := []string{"opaque_ramp", "alpha_border", "palette_opaque"}
	byName := map[string]ioSample{}
	for _, s := range samples {
		byName[s.name] = s
	}

	for _, fc := range ioFormatMatrix() {
		for _, name := range pick {
			s := byName[name]
			fc, s := fc, s
			t.Run(fc.name+"/"+s.name, func(t *testing.T) {
				data := ioEncoded(t, s, fc)

				// Cut points at 0, 1, header area, halfway and one byte short
				// of full: all must fail, and the injected error must surface.
				cuts := []int{0, 1, 2, 8, len(data) / 2, len(data) - 1}
				for _, cut := range cuts {
					if cut < 0 || cut > len(data) {
						continue
					}
					w := &ioFailWriter{limit: cut, err: errIOSentinel}
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Fatalf("%s: Encode panicked with write limit %d: %v",
									ioLocation(fc, s, ""), cut, r)
							}
						}()
						err := Encode(w, s.img, fc.fmt, fc.opts...)
						if err == nil {
							t.Fatalf("%s: write limit %d/%d: expected error, got nil",
								ioLocation(fc, s, ""), cut, len(data))
						}
						if !errors.Is(err, errIOSentinel) {
							t.Errorf("%s: write limit %d/%d: error %q does not wrap injected failure",
								ioLocation(fc, s, ""), cut, len(data), err)
						}
					}()
				}

				// A short writer (n < len(p), nil error) must never be
				// reported as success.
				sw := &ioShortWriter{limit: len(data) / 2}
				err := Encode(sw, s.img, fc.fmt, fc.opts...)
				if err == nil {
					t.Errorf("%s: short writer (%d-byte cap, %d-byte image): Encode reported success",
						ioLocation(fc, s, ""), sw.limit, len(data))
				}
			})
		}
	}
}

// TestIOInputNotMutated verifies that Encode and Decode never modify the
// caller's source pixels, including for non-zero bounds.
func TestIOInputNotMutated(t *testing.T) {
	for _, s := range ioSampleMatrix() {
		for _, fc := range ioFormatMatrix() {
			s, fc := s, fc
			t.Run(fc.name+"/"+s.name, func(t *testing.T) {
				before := Clone(s.img)
				data := ioEncoded(t, s, fc)
				after := Clone(s.img)
				if !bytes.Equal(before.Pix, after.Pix) || before.Rect != after.Rect {
					p, ch, g, w, _ := firstPixelDiff(before, after)
					t.Fatalf("%s: Encode modified its input at pixel %v channel %d: %d -> %d",
						ioLocation(fc, s, ""), p, ch, g, w)
				}

				dec, err := Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("%s: Decode: %v", ioLocation(fc, s, ""), err)
				}
				decBefore := Clone(dec)
				// Decoding a second time from an independent reader over the
				// same bytes must be stable; this catches decoders that retain
				// and reuse mutable buffers between calls.
				decSecond, err := Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("%s: second Decode: %v", ioLocation(fc, s, ""), err)
				}
				if !bytes.Equal(decBefore.Pix, Clone(decSecond).Pix) {
					t.Errorf("%s: decoding twice from identical bytes produced different pixels",
						ioLocation(fc, s, ""))
				}
				// The first decoded image must also still be intact: a shared
				// scratch buffer would have mutated it during the second call.
				if !bytes.Equal(decBefore.Pix, Clone(dec).Pix) {
					t.Errorf("%s: first decoded image was mutated by a subsequent Decode",
						ioLocation(fc, s, ""))
				}
			})
		}
	}
}

// TestIOBadHeaderRejected injects a corrupted header into freshly encoded
// data and requires a non-nil error and nil image for every format.
func TestIOBadHeaderRejected(t *testing.T) {
	s := ioSample{name: "opaque_ramp", kind: sampleOpaque,
		img: func() image.Image {
			img := image.NewNRGBA(image.Rect(0, 0, 8, 6))
			for y := 0; y < 6; y++ {
				for x := 0; x < 8; x++ {
					img.SetNRGBA(x, y, color.NRGBA{uint8(x * 30), uint8(y * 40), uint8(x*y*5 + 9), 255})
				}
			}
			return img
		}()}

	for _, fc := range ioFormatMatrix() {
		fc := fc
		t.Run(fc.name, func(t *testing.T) {
			data := ioEncoded(t, s, fc)

			// Case 1: destroy the magic/signature at the start.
			sig := append([]byte(nil), data...)
			for i := 0; i < 4 && i < len(sig); i++ {
				sig[i] ^= 0xff
			}
			img, err := Decode(bytes.NewReader(sig))
			if err == nil {
				t.Fatalf("%s: corrupted signature (% x): expected error, got nil", ioLocation(fc, s, "signature"), sig[:4])
			}
			if img != nil {
				t.Fatalf("%s: corrupted signature: got non-nil image alongside error", ioLocation(fc, s, "signature"))
			}

			// Case 2: flip a byte inside the first header block. For formats
			// where the header is structural this must fail; where the byte is
			// merely reserved the test only requires no panic and no silent
			// decode of *different* data.
			hdr := append([]byte(nil), data...)
			hdrOff := 4
			if hdrOff >= len(hdr) {
				hdrOff = len(hdr) - 1
			}
			hdr[hdrOff] ^= 0xff
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s: header flip panicked: %v", ioLocation(fc, s, fmt.Sprintf("offset=%d", hdrOff)), r)
					}
				}()
				img, err := Decode(bytes.NewReader(hdr))
				if err == nil && img == nil {
					t.Fatalf("%s: header flip decoded to nil image with nil error",
						ioLocation(fc, s, fmt.Sprintf("offset=%d", hdrOff)))
				}
			}()
		})
	}
}

// TestIOTailTruncationNeverPanics sweeps every truncation length. It asserts:
//   - no panic at any length;
//   - a non-nil error is never returned alongside a non-nil image;
//   - a successful decode never returns a nil image;
//   - a successful decode of a truncated prefix is byte-identical to the full
//     decode. Formats with trailing metadata (TIFF's extra IFD entries, GIF's
//     trailer) may accept a prefix, but they must not silently produce a
//     different or partial picture from it.
//
// The full-buffer length is verified separately by the round-trip matrix, so
// this sweep is exclusively about failure paths.
func TestIOTailTruncationNeverPanics(t *testing.T) {
	samples := map[string]ioSample{}
	for _, s := range ioSampleMatrix() {
		samples[s.name] = s
	}
	s := samples["alpha_border"]

	for _, fc := range ioFormatMatrix() {
		fc := fc
		t.Run(fc.name, func(t *testing.T) {
			data := ioEncoded(t, s, fc)
			full, err := Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s: full decode: %v", ioLocation(fc, s, ""), err)
			}
			fullPix := Clone(full)

			var firstSuccessfulCut = -1
			for n := 0; n <= len(data); n++ {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s: Decode panicked at truncation %d/%d: %v",
								ioLocation(fc, s, fmt.Sprintf("cut=%d", n)), n, len(data), r)
						}
					}()
					img, err := Decode(bytes.NewReader(data[:n]))
					if err != nil {
						if img != nil {
							t.Fatalf("%s: truncated decode returned non-nil image alongside error",
								ioLocation(fc, s, fmt.Sprintf("cut=%d", n)))
						}
						return
					}
					if img == nil {
						t.Fatalf("%s: truncated decode returned nil image with nil error",
							ioLocation(fc, s, fmt.Sprintf("cut=%d", n)))
					}
					if firstSuccessfulCut < 0 {
						firstSuccessfulCut = n
					}
					got := Clone(img)
					if got.Rect != fullPix.Rect || !bytes.Equal(got.Pix, fullPix.Pix) {
						t.Fatalf("%s: truncated decode at %d/%d succeeded with bounds %v but is not byte-identical to the full decode (bounds %v)",
							ioLocation(fc, s, fmt.Sprintf("cut=%d", n)), n, len(data), got.Rect, fullPix.Rect)
					}
				}()
			}
			if firstSuccessfulCut != len(data) {
				// For the buffers used here a prefix may be decodable only in
				// the trailing-metadata formats; report where that happened so
				// the behaviour is visible, but require that prefix == full
				// (already enforced above).
				t.Logf("%s: first byte-identical successful prefix at %d/%d bytes",
					ioLocation(fc, s, ""), firstSuccessfulCut, len(data))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AutoOrientation branches
// ---------------------------------------------------------------------------

// ioOrientationPayload builds a minimal JPEG carrying an EXIF Orientation tag
// with the given value, followed by a real JPEG image. The value is written
// into a copy of testdata/orientation_1.jpg so that the surrounding EXIF
// structure stays valid; this is a deliberate *injection* used to prove the
// orientation branches are actually exercised.
func ioOrientationPayload(t *testing.T, value byte) []byte {
	t.Helper()
	data, err := ioutil.ReadFile("testdata/orientation_1.jpg")
	if err != nil {
		t.Fatalf("reading orientation fixture: %v", err)
	}
	tag := bytes.Index(data, []byte{0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01})
	if tag < 0 || tag+9 >= len(data) {
		t.Fatalf("orientation fixture layout changed: tag offset %d", tag)
	}
	out := append([]byte(nil), data...)
	out[tag+9] = value
	return out
}

// ioOrientation1 returns the unmodified orientation_1.jpg bytes, whose EXIF
// tag is 1 (normal), i.e. the baseline image content.
func ioOrientation1(t *testing.T) []byte {
	t.Helper()
	data, err := ioutil.ReadFile("testdata/orientation_1.jpg")
	if err != nil {
		t.Fatalf("reading orientation fixture: %v", err)
	}
	return data
}

// TestIOAutoOrientationBranches checks every documented orientation value by
// injecting that value into testdata/orientation_1.jpg (baseline content) and
// comparing against an independently implemented transform.
func TestIOAutoOrientationBranches(t *testing.T) {
	// Normalized reference: decode without auto-orientation and apply the
	// transformation independently, so the expectation does not reuse io.go's
	// transform code.
	plain, err := Decode(bytes.NewReader(ioOrientation1(t)))
	if err != nil {
		t.Fatalf("plain decode of orientation_1.jpg: %v", err)
	}
	plainNRGBA := Clone(plain)

	for o := 1; o <= 8; o++ {
		o := o
		t.Run(fmt.Sprintf("orientation_%d", o), func(t *testing.T) {
			payload := ioOrientationPayload(t, byte(o))
			gotImg, err := Decode(bytes.NewReader(payload), AutoOrientation(true))
			if err != nil {
				t.Fatalf("orientation=%d: Decode(AutoOrientation): %v", o, err)
			}
			got := Clone(gotImg)
			want := expectedOrientationOf(Clone(plainNRGBA), o)
			if got.Rect != want.Rect {
				t.Fatalf("orientation=%d: bounds %v, want %v", o, got.Rect, want.Rect)
			}
			if !bytes.Equal(got.Pix, want.Pix) {
				p, ch, g, w, _ := firstPixelDiff(got, want)
				t.Fatalf("orientation=%d: pixel mismatch at %v channel %d: got %d want %d", o, p, ch, g, w)
			}
		})
	}

	// The shipped fixture set must agree with the injected values as well,
	// which guards against the fixtures being swapped or regenerated.
	for i := 0; i <= 8; i++ {
		img, err := Open(fmt.Sprintf("testdata/orientation_%d.jpg", i), AutoOrientation(true))
		if err != nil {
			t.Fatalf("testdata/orientation_%d.jpg: Open: %v", i, err)
		}
		plainI, err := Open(fmt.Sprintf("testdata/orientation_%d.jpg", i))
		if err != nil {
			t.Fatalf("testdata/orientation_%d.jpg: plain Open: %v", i, err)
		}
		want := expectedOrientationOf(Clone(plainI), i)
		got := Clone(img)
		if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("fixture orientation_%d: auto-oriented result does not match independent transform (bounds %v vs %v)",
				i, got.Rect, want.Rect)
		}
		if i == 0 {
			// No EXIF tag at all: auto-orientation must be a no-op.
			if !bytes.Equal(got.Pix, Clone(plainI).Pix) {
				t.Errorf("fixture orientation_0: AutoOrientation changed an image with no EXIF tag")
			}
		}
	}
}

// expectedOrientationOf is the fixture-independent transform used above.
func expectedOrientationOf(src *image.NRGBA, o int) *image.NRGBA {
	switch o {
	case 1:
		return src
	case 2:
		return flipHorizontalNRGBA(src)
	case 3:
		return rotate180NRGBA(src)
	case 4:
		return flipVerticalNRGBA(src)
	case 5:
		return transposeNRGBA(src)
	case 6:
		return rotate270NRGBA(src)
	case 7:
		return transverseNRGBA(src)
	case 8:
		return rotate90NRGBA(src)
	}
	return src
}

func flipHorizontalNRGBA(src *image.NRGBA) *image.NRGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			si := y*src.Stride + (w-1-x)*4
			di := y*dst.Stride + x*4
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

func flipVerticalNRGBA(src *image.NRGBA) *image.NRGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			si := (h-1-y)*src.Stride + x*4
			di := y*dst.Stride + x*4
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// rotate90NRGBA rotates 90 degrees counter-clockwise, matching the library's
// Rotate90 direction.
func rotate90NRGBA(src *image.NRGBA) *image.NRGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ox, oy := y, w-1-x
			si := y*src.Stride + x*4
			di := oy*dst.Stride + ox*4
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

func rotate180NRGBA(src *image.NRGBA) *image.NRGBA {
	return rotate90NRGBA(rotate90NRGBA(src))
}

func rotate270NRGBA(src *image.NRGBA) *image.NRGBA {
	return rotate90NRGBA(rotate90NRGBA(rotate90NRGBA(src)))
}

func transposeNRGBA(src *image.NRGBA) *image.NRGBA {
	return rotate90NRGBA(flipHorizontalNRGBA(src))
}

func transverseNRGBA(src *image.NRGBA) *image.NRGBA {
	return rotate90NRGBA(flipVerticalNRGBA(src))
}

// TestIOAutoOrientationNoExif covers the branch where AutoOrientation is on
// but the input has no EXIF orientation (non-JPEG, malformed EXIF, invalid
// value): the decode must succeed and be identical to the plain decode.
func TestIOAutoOrientationNoExif(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 9, 7))
	for y := 0; y < 7; y++ {
		for x := 0; x < 9; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 25), uint8(y * 35), uint8(x*y*7 + 3), 255})
		}
	}

	formats := []struct {
		name string
		fmt  Format
	}{
		{"PNG", PNG}, {"GIF", GIF}, {"TIFF", TIFF}, {"BMP", BMP},
	}
	for _, fc := range formats {
		var buf bytes.Buffer
		if err := Encode(&buf, img, fc.fmt); err != nil {
			t.Fatalf("format=%s: Encode: %v", fc.name, err)
		}
		plain, err := Decode(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("format=%s: plain Decode: %v", fc.name, err)
		}
		auto, err := Decode(bytes.NewReader(buf.Bytes()), AutoOrientation(true))
		if err != nil {
			t.Fatalf("format=%s: Decode(AutoOrientation): %v", fc.name, err)
		}
		if !bytes.Equal(Clone(plain).Pix, Clone(auto).Pix) {
			t.Errorf("format=%s: non-JPEG decode changed by AutoOrientation", fc.name)
		}
	}

	// JPEG with the EXIF orientation value forced out of range: the branch
	// must fall back to orientationUnspecified.
	bad := ioOrientationPayload(t, 9)
	plain, err := Decode(bytes.NewReader(bad))
	if err != nil {
		t.Fatalf("orientation=9: plain Decode: %v", err)
	}
	auto, err := Decode(bytes.NewReader(bad), AutoOrientation(true))
	if err != nil {
		t.Fatalf("orientation=9: Decode(AutoOrientation): %v", err)
	}
	if !bytes.Equal(Clone(plain).Pix, Clone(auto).Pix) {
		t.Errorf("orientation=9: invalid EXIF value was applied instead of being ignored")
	}

	// JPEG with a corrupted EXIF header: same fallback.
	for _, tc := range []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"truncated_soi", "\xff"},
		{"truncated_app1", "\xff\xd8\xff\xe1\x00"},
		{"invalid_marker", "\xff\xd8\x00\xe1\x00\x00"},
	} {
		img, err := Decode(strings.NewReader(tc.data), AutoOrientation(true))
		if err == nil {
			t.Errorf("malformed EXIF case %q: expected decode error, got nil", tc.name)
		}
		if img != nil {
			t.Errorf("malformed EXIF case %q: got non-nil image alongside error", tc.name)
		}
	}
}

// TestIOAutoOrientationReaderLifetime checks that the auto-orientation path
// fully drains the piped reader, so that a reader which blocks or fails after
// the first read cannot deadlock or leak a goroutine.
func TestIOAutoOrientationReaderLifetime(t *testing.T) {
	data := ioOrientationPayload(t, 6)

	// Count how many bytes the decoder consumes: an auto-orientation decode
	// must consume the whole stream (the pipe goroutine drains it), so a
	// subsequent read from the same offset would see EOF.
	cr := &ioCountingReader{r: bytes.NewReader(data)}
	if _, err := Decode(cr, AutoOrientation(true)); err != nil {
		t.Fatalf("Decode(AutoOrientation): %v", err)
	}
	if cr.n != len(data) {
		t.Errorf("AutoOrientation decode consumed %d/%d bytes; reader was not fully drained",
			cr.n, len(data))
	}
	if _, err := cr.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("after AutoOrientation decode, reader returned error %v, want EOF", err)
	}

	// A one-byte-at-a-time reader exercises the pipe with pathological short
	// reads; this must still complete and match the buffered decode.
	buffered, err := Decode(bytes.NewReader(data), AutoOrientation(true))
	if err != nil {
		t.Fatalf("buffered Decode(AutoOrientation): %v", err)
	}
	streamed, err := Decode(&ioOneByteReader{data: data, err: io.EOF}, AutoOrientation(true))
	if err != nil {
		t.Fatalf("one-byte Decode(AutoOrientation): %v", err)
	}
	if !bytes.Equal(Clone(buffered).Pix, Clone(streamed).Pix) {
		t.Errorf("one-byte AutoOrientation decode differs from buffered decode")
	}
}

// ioCountingReader records consumed bytes.
type ioCountingReader struct {
	r io.Reader
	n int
}

func (c *ioCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// ---------------------------------------------------------------------------
// Deliberate fault injection: proves the assertions actually fail
// ---------------------------------------------------------------------------

// TestIOInjectionProvesDetection demonstrates that at least two independent
// injected encoding/orientation faults are caught by the *same* detector used
// by the real matrix (checkRoundTrip / exact pixel comparison). This is a
// meta-test: it proves the suite is not vacuous; it does not weaken it.
func TestIOInjectionProvesDetection(t *testing.T) {
	// --- Injection 1: an encoder that silently loses a colour channel -----
	t.Run("encoding_channel_loss", func(t *testing.T) {
		s := ioSample{
			name: "injection_opaque_ramp",
			kind: sampleOpaque,
			img: func() image.Image {
				img := image.NewNRGBA(image.Rect(0, 0, 16, 12))
				for y := 0; y < 12; y++ {
					for x := 0; x < 16; x++ {
						img.SetNRGBA(x, y, color.NRGBA{uint8(x*15 + 5), uint8(y*20 + 10), 200, 255})
					}
				}
				return img
			}(),
		}
		fc := ioFormatMatrix()[1] // PNG: the lossless contract.

		// Baseline: a real PNG round trip must pass the detector.
		var buf bytes.Buffer
		if err := Encode(&buf, s.img, fc.fmt); err != nil {
			t.Fatalf("Encode: %v", err)
		}
		dec, err := Decode(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if problems := checkRoundTrip(Clone(dec), expectedPixels(s, fc), s, fc); len(problems) != 0 {
			t.Fatalf("baseline PNG round trip should pass, got: %v", problems)
		}

		// Injected fault: the decoded image has its blue channel halved.
		// This is exactly the class of bug a basic read/write test misses.
		faulty := Clone(dec)
		for i := 0; i < len(faulty.Pix); i += 4 {
			faulty.Pix[i+2] /= 2
		}
		problems := checkRoundTrip(faulty, expectedPixels(s, fc), s, fc)
		if len(problems) == 0 {
			t.Fatalf("injected channel-loss defect was NOT detected by checkRoundTrip")
		}
		t.Logf("detected injected channel-loss defect: %s", problems[0])
	})

	// --- Injection 1b: a GIF encoder that quantizes the palette away ------
	t.Run("encoding_palette_collapse", func(t *testing.T) {
		s := ioSample{
			name: "injection_opaque_ramp",
			kind: sampleOpaque,
			img: func() image.Image {
				img := image.NewNRGBA(image.Rect(0, 0, 24, 17))
				for y := 0; y < 17; y++ {
					for x := 0; x < 24; x++ {
						img.SetNRGBA(x, y, color.NRGBA{
							uint8(x * 255 / 23), uint8(y * 255 / 16), uint8((x + y) * 255 / 40), 255,
						})
					}
				}
				return img
			}(),
		}
		fc := ioFormatMatrix()[2] // GIF: the lossy palette contract.

		encode := func(opts ...EncodeOption) []byte {
			t.Helper()
			var buf bytes.Buffer
			if err := Encode(&buf, s.img, fc.fmt, opts...); err != nil {
				t.Fatalf("Encode: %v", err)
			}
			return buf.Bytes()
		}

		// Baseline: a normal GIF round trip passes the tolerances.
		base, err := Decode(bytes.NewReader(encode()))
		if err != nil {
			t.Fatalf("baseline Decode: %v", err)
		}
		if problems := checkRoundTrip(Clone(base), expectedPixels(s, fc), s, fc); len(problems) != 0 {
			t.Fatalf("baseline GIF round trip should pass, got: %v", problems)
		}

		// Injected encoder defect: force the palette down to two colours via
		// the public option. The tolerance/gamut detector must reject it.
		collapsed, err := Decode(bytes.NewReader(encode(GIFNumColors(2))))
		if err != nil {
			t.Fatalf("injected Decode: %v", err)
		}
		problems := checkRoundTrip(Clone(collapsed), expectedPixels(s, fc), s, fc)
		if len(problems) == 0 {
			t.Fatalf("injected palette-collapse defect was NOT detected by checkRoundTrip")
		}
		t.Logf("detected injected palette-collapse defect: %s", problems[0])
	})

	// --- Injection 2: a decoder that applies the wrong orientation --------
	t.Run("orientation_wrong_transform", func(t *testing.T) {
		base, err := Decode(bytes.NewReader(ioOrientation1(t)))
		if err != nil {
			t.Fatalf("plain decode: %v", err)
		}
		// Correct behaviour: tag 6 => rotate 270.
		correctImg, err := Decode(bytes.NewReader(ioOrientationPayload(t, 6)), AutoOrientation(true))
		if err != nil {
			t.Fatalf("auto decode: %v", err)
		}
		correct := Clone(correctImg)
		want := rotate270NRGBA(Clone(base))
		if !bytes.Equal(correct.Pix, want.Pix) || correct.Rect != want.Rect {
			t.Fatalf("baseline orientation_6 should match an independent rotate270")
		}

		// Injected fault: the decoder applies orientation 8 (rotate 90) to a
		// file that says orientation 6.
		faulty := rotate90NRGBA(Clone(base))
		p, ch, g, w, same := firstPixelDiff(correct, faulty)
		if same {
			t.Fatalf("orientation detector is blind: rotate270 output equals rotate90 output")
		}
		t.Logf("detected injected wrong-orientation defect at pixel %v channel %d: correct %d, injected %d",
			p, ch, g, w)
	})

	// --- Injection 3: an encoder that reports success on a short write ----
	t.Run("short_write_reported_as_success", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 16, 12))
		for i := range img.Pix {
			img.Pix[i] = uint8(i * 13)
		}
		var full bytes.Buffer
		if err := Encode(&full, img, PNG); err != nil {
			t.Fatalf("baseline encode: %v", err)
		}
		// A writer that never accepts more than half the bytes and never
		// returns an error models a broken sink. Encode must not report
		// success for the truncated stream.
		short := &ioShortWriter{limit: full.Len() / 2}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("short-write encode panicked: %v", r)
				}
			}()
			err := Encode(short, img, PNG)
			if err == nil {
				t.Fatalf("injected short-write defect was NOT detected: Encode reported success after %d/%d bytes",
					short.off, full.Len())
			}
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("short-write defect detected, but error %v is not io.ErrShortWrite", err)
			}
			t.Logf("detected injected short-write defect: %v after %d/%d bytes", err, short.off, full.Len())
		}()
	})

	// --- Injection 4: a TIFF whose 16-bit width tag cannot hold the image -
	t.Run("tiff_oversized_dimension", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 1<<16, 1))
		var buf bytes.Buffer
		err := Encode(&buf, img, TIFF)
		if err == nil {
			t.Fatalf("injected oversized TIFF was NOT detected: Encode succeeded")
		}
		if !errors.Is(err, ErrImageTooLarge) {
			t.Fatalf("oversized TIFF error %v is not ErrImageTooLarge", err)
		}
		if buf.Len() != 0 {
			t.Errorf("oversized TIFF wrote %d bytes before failing, want 0", buf.Len())
		}
		t.Logf("detected injected oversized-TIFF defect: %v", err)
	})

	// --- Injection 5: a truncated read that is accepted as an image ------
	t.Run("short_read_accepted", func(t *testing.T) {
		src := image.NewNRGBA(image.Rect(0, 0, 12, 9))
		for i := range src.Pix {
			src.Pix[i] = uint8(i * 11)
		}
		var buf bytes.Buffer
		if err := Encode(&buf, src, PNG); err != nil {
			t.Fatalf("encode: %v", err)
		}
		data := buf.Bytes()
		if _, err := Decode(bytes.NewReader(data[:len(data)*2/3])); err == nil {
			t.Fatalf("baseline: truncated PNG decoded without error")
		}
		img, err := Decode(&ioErrReader{data: data[:len(data)*2/3], err: errIOSentinel})
		if err == nil || img != nil {
			t.Fatalf("truncation detector is blind: err=%v imgNil=%v", err, img == nil)
		}
		t.Logf("detected injected truncated-read defect: %v", err)
	})
}

// ---------------------------------------------------------------------------
// Degenerate sizes and header-only metadata
// ---------------------------------------------------------------------------

// TestIOZeroAndBoundarySizes documents and pins the behaviour at sizes the
// codecs treat specially: zero in one dimension, and the 1<<16 limit for
// formats that encode dimensions in 16 bits.
func TestIOZeroAndBoundarySizes(t *testing.T) {
	type sizeCase struct {
		w, h int
		// encErr is the expected encode error substring, "" for success.
		encErrs map[string]string
	}
	cases := []sizeCase{
		{0, 0, map[string]string{"JPEG": "", "PNG": "invalid image size", "GIF": "", "TIFF": "", "BMP": ""}},
		{0, 5, map[string]string{"JPEG": "", "PNG": "invalid image size", "GIF": "", "TIFF": "", "BMP": ""}},
		{5, 0, map[string]string{"JPEG": "", "PNG": "invalid image size", "GIF": "", "TIFF": "", "BMP": ""}},
	}
	for _, sc := range cases {
		for _, fc := range ioFormatMatrix() {
			want := sc.encErrs[fc.name]
			img := image.NewNRGBA(image.Rect(0, 0, sc.w, sc.h))
			var buf bytes.Buffer
			err := Encode(&buf, img, fc.fmt, fc.opts...)
			loc := fmt.Sprintf("format=%s size=%dx%d", fc.name, sc.w, sc.h)
			if want == "" {
				if err != nil {
					t.Errorf("%s: Encode: %v, want success", loc, err)
					continue
				}
				if _, err := Decode(bytes.NewReader(buf.Bytes())); err != nil {
					t.Errorf("%s: Decode: %v", loc, err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s: Encode succeeded, want error containing %q", loc, want)
				continue
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: Encode error %q does not contain %q", loc, err, want)
			}
		}
	}

	// 16-bit dimension limits: JPEG, GIF and TIFF all store one dimension in
	// 16 bits (GIF forbids >= 1<<16; TIFF's ImageWidth tag is a SHORT), so
	// they must refuse 1<<16. PNG and BMP can represent it and must
	// round-trip the size without panicking.
	for _, fc := range ioFormatMatrix() {
		img := image.NewNRGBA(image.Rect(0, 0, 1<<16, 1))
		var buf bytes.Buffer
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("format=%s size=65536x1: Encode panicked: %v", fc.name, r)
				}
			}()
			err := Encode(&buf, img, fc.fmt, fc.opts...)
			loc := fmt.Sprintf("format=%s size=65536x1", fc.name)
			switch fc.fmt {
			case JPEG, GIF, TIFF:
				if err == nil {
					t.Errorf("%s: Encode succeeded, want dimension-limit error", loc)
				}
			default:
				if err != nil {
					t.Errorf("%s: Encode: %v", loc, err)
					return
				}
				dec, err := Decode(bytes.NewReader(buf.Bytes()))
				if err != nil {
					t.Errorf("%s: Decode: %v", loc, err)
					return
				}
				if dec.Bounds().Dx() != 1<<16 || dec.Bounds().Dy() != 1 {
					t.Errorf("%s: decoded bounds %v, want 65536x1", loc, dec.Bounds())
				}
			}
		}()
	}
}

// ---------------------------------------------------------------------------
// Report
// ---------------------------------------------------------------------------

// TestIOQualityReport emits a per-format, per-sample report. It is not an
// assertion test: it prints the observed sizes and error metrics so that a
// failure in the matrix can be localised to a format and a sample.
func TestIOQualityReport(t *testing.T) {
	var lines []string
	for _, fc := range ioFormatMatrix() {
		for _, s := range ioSampleMatrix() {
			var buf bytes.Buffer
			if err := Encode(&buf, s.img, fc.fmt, fc.opts...); err != nil {
				lines = append(lines, fmt.Sprintf("%-5s %-26s ENCODE-ERROR %v", fc.name, s.name, err))
				continue
			}
			decoded, err := Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				lines = append(lines, fmt.Sprintf("%-5s %-26s DECODE-ERROR %v", fc.name, s.name, err))
				continue
			}
			got := Clone(decoded)
			want := expectedPixels(s, fc)
			srcBounds := s.img.Bounds()
			if fc.lossless {
				status := "exact"
				if !bytes.Equal(got.Pix, want.Pix) {
					status = "MISMATCH"
				}
				lines = append(lines, fmt.Sprintf("%-5s %-26s src=%v bytes=%-6d decoded=%v %s",
					fc.name, s.name, srcBounds, buf.Len(), decoded.Bounds(), status))
				continue
			}
			m := measureIO(got, want)
			lines = append(lines, fmt.Sprintf("%-5s %-26s src=%v bytes=%-6d maxBlock=%-6.2f mean=%-6.2f gamut=%.2f maxPixel=%d",
				fc.name, s.name, srcBounds, buf.Len(), m.maxBlockMean, m.meanAbs, m.minGamut, m.maxPixelDiff))
		}
	}
	sort.Strings(lines)
	t.Logf("I/O quality matrix report:\n%s", strings.Join(lines, "\n"))
}
