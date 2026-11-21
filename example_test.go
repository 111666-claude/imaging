package imaging_test

import (
	"fmt"
	"image"
	"image/color"
	"log"

	"github.com/disintegration/imaging"
)

func Example() {
	// Open a test image.
	src, err := imaging.Open("testdata/flowers.png")
	if err != nil {
		log.Fatalf("failed to open image: %v", err)
	}

	// Crop the original image to 300x300px size using the center anchor.
	src = imaging.CropAnchor(src, 300, 300, imaging.Center)

	// Resize the cropped image to width = 200px preserving the aspect ratio.
	src = imaging.Resize(src, 200, 0, imaging.Lanczos)

	// Create a blurred version of the image.
	img1 := imaging.Blur(src, 5)

	// Create a grayscale version of the image with higher contrast and sharpness.
	img2 := imaging.Grayscale(src)
	img2 = imaging.AdjustContrast(img2, 20)
	img2 = imaging.Sharpen(img2, 2)

	// Create an inverted version of the image.
	img3 := imaging.Invert(src)

	// Create an embossed version of the image using a convolution filter.
	img4 := imaging.Convolve3x3(
		src,
		[9]float64{
			-1, -1, 0,
			-1, 1, 1,
			0, 1, 1,
		},
		nil,
	)

	// Create a new image and paste the four produced images into it.
	dst := imaging.New(400, 400, color.NRGBA{0, 0, 0, 0})
	dst = imaging.Paste(dst, img1, image.Pt(0, 0))
	dst = imaging.Paste(dst, img2, image.Pt(0, 200))
	dst = imaging.Paste(dst, img3, image.Pt(200, 0))
	dst = imaging.Paste(dst, img4, image.Pt(200, 200))

	// Save the resulting image as JPEG.
	err = imaging.Save(dst, "testdata/out_example.jpg")
	if err != nil {
		log.Fatalf("failed to save image: %v", err)
	}
}

func ExampleSharpness() {
	// Build a 64x32 image with a sharp black/white edge on the left half and
	// a flat solid fill on the right half.
	src := imaging.New(64, 32, color.NRGBA{128, 128, 128, 255})
	for x := 0; x < 32; x++ {
		c := color.NRGBA{0, 0, 0, 255}
		if x >= 16 {
			c = color.NRGBA{255, 255, 255, 255}
		}
		col := imaging.New(1, 32, c)
		src = imaging.Paste(src, col, image.Pt(x, 0))
	}

	// Analyze local sharpness on a non-overlapping 32px grid. Window
	// rectangles come back in the original image coordinates.
	m, err := imaging.Sharpness(src, &imaging.SharpnessOptions{
		WindowSize:     32,
		Step:           32,
		NoiseThreshold: 4,
	})
	if err != nil {
		log.Fatalf("sharpness analysis failed: %v", err)
	}

	for _, r := range m.Regions {
		fmt.Printf("%v score=%.2f coverage=%.2f\n", r.Rect, r.Score, r.Coverage)
	}
	// Output:
	// (0,0)-(32,32) score=0.08 coverage=1.00
	// (32,0)-(64,32) score=0.00 coverage=1.00
}
