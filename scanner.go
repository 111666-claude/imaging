package imaging

import (
	"image"
	"image/color"
)

// scanFunc scans the rectangular region (x1, y1)-(x2, y2) of a source image
// into dst as packed NRGBA pixels (4 bytes per pixel, row by row). The
// coordinates are relative to the image bounds: (0, 0) is Bounds().Min.
// dst must have a length of at least (x2-x1)*(y2-y1)*4.
type scanFunc func(x1, y1, x2, y2 int, dst []uint8)

// scanner reads pixels of an image in NRGBA format. The concrete scanning
// strategy is selected once in newScanner, so scanning itself does no type
// dispatch.
type scanner struct {
	w, h int
	scan scanFunc
}

func newScanner(img image.Image) *scanner {
	s := &scanner{
		w: img.Bounds().Dx(),
		h: img.Bounds().Dy(),
	}
	switch img := img.(type) {
	case *image.NRGBA:
		s.scan = scanNRGBA(img)
	case *image.NRGBA64:
		s.scan = scanNRGBA64(img)
	case *image.RGBA:
		s.scan = scanRGBA(img)
	case *image.RGBA64:
		s.scan = scanRGBA64(img)
	case *image.Gray:
		s.scan = scanGray(img)
	case *image.Gray16:
		s.scan = scanGray16(img)
	case *image.YCbCr:
		s.scan = scanYCbCr(img)
	case *image.Paletted:
		s.scan = scanPaletted(img, paletteToNRGBA(img.Palette))
	default:
		s.scan = scanGeneric(img)
	}
	return s
}

// paletteToNRGBA converts a color palette to NRGBA once, so the paletted
// scan path is a plain table lookup.
func paletteToNRGBA(p color.Palette) []color.NRGBA {
	palette := make([]color.NRGBA, len(p))
	for i := 0; i < len(p); i++ {
		palette[i] = color.NRGBAModel.Convert(p[i]).(color.NRGBA)
	}
	return palette
}

// unpremultiply8 converts an 8-bit alpha-premultiplied RGBA pixel s to NRGBA.
func unpremultiply8(d, s []uint8) {
	a := s[3]
	switch a {
	case 0:
		d[0] = 0
		d[1] = 0
		d[2] = 0
		d[3] = 0
	case 0xff:
		d[0] = s[0]
		d[1] = s[1]
		d[2] = s[2]
		d[3] = 0xff
	default:
		a16 := uint16(a)
		d[0] = uint8(uint16(s[0]) * 0xff / a16)
		d[1] = uint8(uint16(s[1]) * 0xff / a16)
		d[2] = uint8(uint16(s[2]) * 0xff / a16)
		d[3] = a
	}
}

// unpremultiply16 converts a 16-bit alpha-premultiplied RGBA pixel s
// (big-endian, 2 bytes per channel) to 8-bit NRGBA.
func unpremultiply16(d, s []uint8) {
	a := s[6]
	switch a {
	case 0:
		d[0] = 0
		d[1] = 0
		d[2] = 0
	case 0xff:
		d[0] = s[0]
		d[1] = s[2]
		d[2] = s[4]
	default:
		r32 := uint32(s[0])<<8 | uint32(s[1])
		g32 := uint32(s[2])<<8 | uint32(s[3])
		b32 := uint32(s[4])<<8 | uint32(s[5])
		a32 := uint32(s[6])<<8 | uint32(s[7])
		d[0] = uint8((r32 * 0xffff / a32) >> 8)
		d[1] = uint8((g32 * 0xffff / a32) >> 8)
		d[2] = uint8((b32 * 0xffff / a32) >> 8)
	}
	d[3] = a
}

// unpremultiply32 converts 16-bit-per-channel alpha-premultiplied RGBA
// values (as returned by color.Color.RGBA) to 8-bit NRGBA.
func unpremultiply32(d []uint8, r16, g16, b16, a16 uint32) {
	switch a16 {
	case 0xffff:
		d[0] = uint8(r16 >> 8)
		d[1] = uint8(g16 >> 8)
		d[2] = uint8(b16 >> 8)
		d[3] = 0xff
	case 0:
		d[0] = 0
		d[1] = 0
		d[2] = 0
		d[3] = 0
	default:
		d[0] = uint8(((r16 * 0xffff) / a16) >> 8)
		d[1] = uint8(((g16 * 0xffff) / a16) >> 8)
		d[2] = uint8(((b16 * 0xffff) / a16) >> 8)
		d[3] = uint8(a16 >> 8)
	}
}

func scanNRGBA(img *image.NRGBA) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		size := (x2 - x1) * 4
		j := 0
		i := y1*img.Stride + x1*4
		if size == 4 {
			for y := y1; y < y2; y++ {
				d := dst[j : j+4 : j+4]
				s := img.Pix[i : i+4 : i+4]
				d[0] = s[0]
				d[1] = s[1]
				d[2] = s[2]
				d[3] = s[3]
				j += size
				i += img.Stride
			}
		} else {
			for y := y1; y < y2; y++ {
				copy(dst[j:j+size], img.Pix[i:i+size])
				j += size
				i += img.Stride
			}
		}
	}
}

func scanNRGBA64(img *image.NRGBA64) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1*8
			for x := x1; x < x2; x++ {
				s := img.Pix[i : i+8 : i+8]
				d := dst[j : j+4 : j+4]
				d[0] = s[0]
				d[1] = s[2]
				d[2] = s[4]
				d[3] = s[6]
				j += 4
				i += 8
			}
		}
	}
}

func scanRGBA(img *image.RGBA) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1*4
			for x := x1; x < x2; x++ {
				unpremultiply8(dst[j:j+4:j+4], img.Pix[i:i+4:i+4])
				j += 4
				i += 4
			}
		}
	}
}

func scanRGBA64(img *image.RGBA64) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1*8
			for x := x1; x < x2; x++ {
				unpremultiply16(dst[j:j+4:j+4], img.Pix[i:i+8:i+8])
				j += 4
				i += 8
			}
		}
	}
}

func scanGray(img *image.Gray) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1
			for x := x1; x < x2; x++ {
				c := img.Pix[i]
				d := dst[j : j+4 : j+4]
				d[0] = c
				d[1] = c
				d[2] = c
				d[3] = 0xff
				j += 4
				i++
			}
		}
	}
}

func scanGray16(img *image.Gray16) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1*2
			for x := x1; x < x2; x++ {
				c := img.Pix[i]
				d := dst[j : j+4 : j+4]
				d[0] = c
				d[1] = c
				d[2] = c
				d[3] = 0xff
				j += 4
				i += 2
			}
		}
	}
}

func scanPaletted(img *image.Paletted, palette []color.NRGBA) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1
			for x := x1; x < x2; x++ {
				c := palette[img.Pix[i]]
				d := dst[j : j+4 : j+4]
				d[0] = c.R
				d[1] = c.G
				d[2] = c.B
				d[3] = c.A
				j += 4
				i++
			}
		}
	}
}

func scanYCbCr(img *image.YCbCr) scanFunc {
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		x1 += img.Rect.Min.X
		x2 += img.Rect.Min.X
		y1 += img.Rect.Min.Y
		y2 += img.Rect.Min.Y

		hy := img.Rect.Min.Y / 2
		hx := img.Rect.Min.X / 2
		for y := y1; y < y2; y++ {
			iy := (y-img.Rect.Min.Y)*img.YStride + (x1 - img.Rect.Min.X)

			var yBase int
			switch img.SubsampleRatio {
			case image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422:
				yBase = (y - img.Rect.Min.Y) * img.CStride
			case image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio440:
				yBase = (y/2 - hy) * img.CStride
			}

			for x := x1; x < x2; x++ {
				var ic int
				switch img.SubsampleRatio {
				case image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio440:
					ic = yBase + (x - img.Rect.Min.X)
				case image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420:
					ic = yBase + (x/2 - hx)
				default:
					ic = img.COffset(x, y)
				}

				yy1 := int32(img.Y[iy]) * 0x10101
				cb1 := int32(img.Cb[ic]) - 128
				cr1 := int32(img.Cr[ic]) - 128

				r := yy1 + 91881*cr1
				if uint32(r)&0xff000000 == 0 {
					r >>= 16
				} else {
					r = ^(r >> 31)
				}

				g := yy1 - 22554*cb1 - 46802*cr1
				if uint32(g)&0xff000000 == 0 {
					g >>= 16
				} else {
					g = ^(g >> 31)
				}

				b := yy1 + 116130*cb1
				if uint32(b)&0xff000000 == 0 {
					b >>= 16
				} else {
					b = ^(b >> 31)
				}

				d := dst[j : j+4 : j+4]
				d[0] = uint8(r)
				d[1] = uint8(g)
				d[2] = uint8(b)
				d[3] = 0xff

				iy++
				j += 4
			}
		}
	}
}

func scanGeneric(img image.Image) scanFunc {
	b := img.Bounds()
	return func(x1, y1, x2, y2 int, dst []uint8) {
		j := 0
		x1 += b.Min.X
		x2 += b.Min.X
		y1 += b.Min.Y
		y2 += b.Min.Y
		for y := y1; y < y2; y++ {
			for x := x1; x < x2; x++ {
				r16, g16, b16, a16 := img.At(x, y).RGBA()
				unpremultiply32(dst[j:j+4:j+4], r16, g16, b16, a16)
				j += 4
			}
		}
	}
}
