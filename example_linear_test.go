package imaging_test

import (
	"fmt"
	"image"
	"image/color"

	"github.com/disintegration/imaging"
)

// downscaleBlackWhiteTexture builds a small black and white checkerboard,
// similar to a high-contrast UI texture.
func downscaleBlackWhiteTexture(width, height, cell int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	black := color.NRGBA{0x00, 0x00, 0x00, 0xff}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := black
			if (x/cell+y/cell)%2 == 0 {
				c = white
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// ExampleResizeLinear shows how to avoid the darkening that averaging
// sRGB-encoded values causes when a high-contrast texture is scaled down.
func ExampleResizeLinear() {
	src := downscaleBlackWhiteTexture(16, 16, 1)

	// Resampling in linear light averages the physical light of the black and
	// white cells, so an even mix stays a medium gray (sRGB 188).
	linear := imaging.ResizeLinear(src, 1, 1, imaging.Box)
	// The default averages the encoded values, which is darker (sRGB 128).
	legacy := imaging.Resize(src, 1, 1, imaging.Box)

	fmt.Println("linear light:", linear.NRGBAAt(0, 0))
	fmt.Println("default:     ", legacy.NRGBAAt(0, 0))
	// Output:
	// linear light: {188 188 188 255}
	// default:      {128 128 128 255}
}

// ExampleLinearLight shows the option form, which can also be passed to Fit,
// Fill and Thumbnail. The result is identical to ResizeLinear.
func ExampleLinearLight() {
	src := downscaleBlackWhiteTexture(8, 8, 1)
	dst := imaging.Resize(src, 2, 2, imaging.Lanczos, imaging.LinearLight(true))
	fmt.Println(dst.Bounds().Dx(), dst.Bounds().Dy(), dst.NRGBAAt(0, 0))
	// Output: 2 2 {188 188 188 255}
}
