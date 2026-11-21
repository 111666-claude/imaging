package imaging

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

// benchPattern fills img with a deterministic pattern that exercises
// opaque, fully transparent and partially transparent pixels.
func benchPattern(img draw.Image) {
	r := img.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8(x*3 + y),
				G: uint8(x + y*5),
				B: uint8(x*2 + y*7),
				A: uint8((x*y + x + y) % 256),
			})
		}
	}
}

func makeBenchNRGBA(r image.Rectangle) *image.NRGBA {
	img := image.NewNRGBA(r)
	benchPattern(img)
	return img
}

func makeBenchNRGBA64(r image.Rectangle) *image.NRGBA64 {
	img := image.NewNRGBA64(r)
	benchPattern(img)
	return img
}

func makeBenchRGBA(r image.Rectangle) *image.RGBA {
	img := image.NewRGBA(r)
	benchPattern(img)
	return img
}

func makeBenchRGBA64(r image.Rectangle) *image.RGBA64 {
	img := image.NewRGBA64(r)
	benchPattern(img)
	return img
}

func makeBenchGray(r image.Rectangle) *image.Gray {
	img := image.NewGray(r)
	benchPattern(img)
	return img
}

func makeBenchGray16(r image.Rectangle) *image.Gray16 {
	img := image.NewGray16(r)
	benchPattern(img)
	return img
}

func makeBenchYCbCr(r image.Rectangle) *image.YCbCr {
	src := makeBenchNRGBA(r)
	img := image.NewYCbCr(r, image.YCbCrSubsampleRatio420)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			iy := img.YOffset(x, y)
			ic := img.COffset(x, y)
			img.Y[iy], img.Cb[ic], img.Cr[ic] = color.RGBToYCbCr(c.R, c.G, c.B)
		}
	}
	return img
}

func makeBenchPaletted(r image.Rectangle) *image.Paletted {
	src := makeBenchNRGBA(r)
	pal := make(color.Palette, 256)
	for i := range pal {
		pal[i] = color.NRGBA{uint8(i), uint8(255 - i), uint8(i * 3), uint8(i)}
	}
	img := image.NewPaletted(r, pal)
	draw.Draw(img, r, src, r.Min, draw.Src)
	return img
}

func makeBenchGeneric(r image.Rectangle) image.Image {
	type genericImage struct{ *image.NRGBA }
	return &genericImage{makeBenchNRGBA(r)}
}

func BenchmarkScanRows(b *testing.B) {
	rect := image.Rect(3, 5, 515, 517) // non-zero origin, 512x512
	images := []struct {
		name string
		img  image.Image
	}{
		{"NRGBA", makeBenchNRGBA(rect)},
		{"NRGBA64", makeBenchNRGBA64(rect)},
		{"RGBA", makeBenchRGBA(rect)},
		{"RGBA64", makeBenchRGBA64(rect)},
		{"Gray", makeBenchGray(rect)},
		{"Gray16", makeBenchGray16(rect)},
		{"YCbCr", makeBenchYCbCr(rect)},
		{"Paletted", makeBenchPaletted(rect)},
		{"Generic", makeBenchGeneric(rect)},
	}
	for _, tc := range images {
		b.Run(tc.name, func(b *testing.B) {
			s := newScanner(tc.img)
			buf := make([]uint8, s.w*4)
			b.SetBytes(int64(s.w * s.h * 4))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for y := 0; y < s.h; y++ {
					s.scan(0, y, s.w, y+1, buf)
				}
			}
		})
	}
}

func BenchmarkScanColumns(b *testing.B) {
	rect := image.Rect(3, 5, 515, 517) // non-zero origin, 512x512
	images := []struct {
		name string
		img  image.Image
	}{
		{"NRGBA", makeBenchNRGBA(rect)},
		{"RGBA", makeBenchRGBA(rect)},
		{"Gray16", makeBenchGray16(rect)},
		{"Paletted", makeBenchPaletted(rect)},
	}
	for _, tc := range images {
		b.Run(tc.name, func(b *testing.B) {
			s := newScanner(tc.img)
			buf := make([]uint8, s.h*4)
			b.SetBytes(int64(s.w * s.h * 4))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for x := 0; x < s.w; x++ {
					s.scan(x, 0, x+1, s.h, buf)
				}
			}
		})
	}
}
