package imaging_test

import (
	"fmt"
	"image"
	"image/color"

	"github.com/disintegration/imaging"
)

func ExampleGenerateMipmaps() {
	// A 4x2 cut-out texture: opaque red pixels inside a fully transparent
	// border. The transparent pixels have black RGB values, which must not
	// darken the red edge when the texture is minified.
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	red := color.NRGBA{0xff, 0x00, 0x00, 0xff}
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			if x == 1 || x == 2 {
				src.SetNRGBA(x, y, red)
			}
		}
	}

	// Build a complete mipmap chain down to 1x1.
	levels, err := imaging.GenerateMipmaps(src, imaging.MipmapOptions{})
	if err != nil {
		fmt.Println(err)
		return
	}

	for i, level := range levels {
		fmt.Printf("level %d: %dx%d\n", i, level.Rect.Dx(), level.Rect.Dy())
	}

	// The alpha-weighted average keeps the color fully red; only alpha
	// decreases.
	last := levels[len(levels)-1]
	fmt.Println(last.NRGBAAt(0, 0))

	// Output:
	// level 0: 4x2
	// level 1: 2x1
	// level 2: 1x1
	// {255 0 0 128}
}
