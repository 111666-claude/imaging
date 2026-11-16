package imaging_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"

	"github.com/disintegration/imaging"
)

func ExamplePaletteQuantizer() {
	// A small pixel-art texture with four colors and a transparent border.
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	colors := []color.NRGBA{
		{0xff, 0x00, 0x00, 0xff},
		{0x00, 0xff, 0x00, 0xff},
		{0x00, 0x00, 0xff, 0xff},
		{0xff, 0xff, 0x00, 0xff},
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if x == 0 || y == 0 {
				src.SetNRGBA(x, y, color.NRGBA{})
				continue
			}
			src.SetNRGBA(x, y, colors[(x+y)%len(colors)])
		}
	}

	// Build an adaptive palette with at most four colors.
	palette := imaging.AdaptivePalette(src, 4)
	fmt.Println(len(palette), palette[0])

	// Use the quantizer for GIF encoding. The transparent color is reserved
	// as the first palette entry and is not written to the output.
	var buf bytes.Buffer
	err := imaging.Encode(&buf, src, imaging.GIF,
		imaging.GIFNumColors(4),
		imaging.GIFQuantizer(imaging.NewPaletteQuantizer(4)),
	)
	fmt.Println(err)
	// Output:
	// 4 {0 0 0 0}
	// <nil>
}
