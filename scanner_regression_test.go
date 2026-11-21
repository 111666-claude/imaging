package imaging

import (
	"crypto/md5"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"testing"
)

// goldenRect has a non-zero origin on purpose: scanning must not depend on
// Bounds().Min being (0, 0).
var goldenRect = image.Rect(-7, 3, 121, 85) // 128x82

type goldenFormat struct {
	name string
	img  func() image.Image
}

type goldenOp struct {
	name string
	fn   func(image.Image) *image.NRGBA
}

func goldenFormats() []goldenFormat {
	src := func() *image.NRGBA {
		img := image.NewNRGBA(goldenRect)
		benchPattern(img)
		return img
	}
	convert := func(mk func(r image.Rectangle) draw.Image) func() image.Image {
		return func() image.Image {
			dst := mk(goldenRect)
			draw.Draw(dst, goldenRect, src(), goldenRect.Min, draw.Src)
			return dst
		}
	}
	return []goldenFormat{
		{"NRGBA", func() image.Image { return src() }},
		{"NRGBA64", convert(func(r image.Rectangle) draw.Image { return image.NewNRGBA64(r) })},
		{"RGBA", convert(func(r image.Rectangle) draw.Image { return image.NewRGBA(r) })},
		{"RGBA64", convert(func(r image.Rectangle) draw.Image { return image.NewRGBA64(r) })},
		{"Gray", convert(func(r image.Rectangle) draw.Image { return image.NewGray(r) })},
		{"Gray16", convert(func(r image.Rectangle) draw.Image { return image.NewGray16(r) })},
		{"YCbCr-420", func() image.Image { return makeBenchYCbCr(goldenRect) }},
		{"Paletted", func() image.Image { return makeBenchPaletted(goldenRect) }},
		{"Alpha", convert(func(r image.Rectangle) draw.Image { return image.NewAlpha(r) })},
		{"Alpha16", convert(func(r image.Rectangle) draw.Image { return image.NewAlpha16(r) })},
		{"Generic", func() image.Image {
			type genericImage struct{ *image.NRGBA }
			return &genericImage{src()}
		}},
		// Sub-images share the parent's Pix with a stride larger than the
		// row width; scanning must honor the stride.
		{"NRGBA-SubImage", func() image.Image {
			r := image.Rect(goldenRect.Min.X-5, goldenRect.Min.Y-9, goldenRect.Max.X+11, goldenRect.Max.Y+13)
			big := image.NewNRGBA(r)
			benchPattern(big)
			return big.SubImage(goldenRect)
		}},
		{"RGBA-SubImage", func() image.Image {
			r := image.Rect(goldenRect.Min.X-5, goldenRect.Min.Y-9, goldenRect.Max.X+11, goldenRect.Max.Y+13)
			big := image.NewRGBA(r)
			benchPattern(big)
			return big.SubImage(goldenRect)
		}},
	}
}

func goldenOps() []goldenOp {
	return []goldenOp{
		{"Clone", func(img image.Image) *image.NRGBA { return Clone(img) }},
		{"ResizeLinear", func(img image.Image) *image.NRGBA { return Resize(img, 97, 61, Linear) }},
		{"ResizeNearest", func(img image.Image) *image.NRGBA { return Resize(img, 97, 61, NearestNeighbor) }},
		{"Blur", func(img image.Image) *image.NRGBA { return Blur(img, 1.2) }},
		{"Rotate90", func(img image.Image) *image.NRGBA { return Rotate90(img) }},
		{"Rotate270", func(img image.Image) *image.NRGBA { return Rotate270(img) }},
		{"Rotate37", func(img image.Image) *image.NRGBA { return Rotate(img, 37, color.NRGBA{}) }},
	}
}

// TestScannerGolden guards the scanner refactor: for every supported input
// format the outputs of the main image operations must stay pixel-identical
// to the pre-refactor implementation. The checksums below were generated
// from the original scanner (see golden_gen_test.go).
func TestScannerGolden(t *testing.T) {
	want := map[[2]string]string{
		{"NRGBA", "Clone"}:                  "c683b8c44e0cfbaa9537067e1585b273",
		{"NRGBA", "ResizeLinear"}:           "3ff17459bd3b7f040a3c712ea6886bce",
		{"NRGBA", "ResizeNearest"}:          "dea92f2bddcb9dd4fe6ac0784e2be436",
		{"NRGBA", "Blur"}:                   "c34279deb5839977d8efd104acd00b18",
		{"NRGBA", "Rotate90"}:               "9b7c185f2221f8a5e9c5acbd6413de0e",
		{"NRGBA", "Rotate270"}:              "fae9975667ed4c0e5bc39ca8cc83f9b8",
		{"NRGBA", "Rotate37"}:               "18678dc46c51b4ff4577457e80d626c7",
		{"NRGBA64", "Clone"}:                "95d912f145be6a99bf392e5317811769",
		{"NRGBA64", "ResizeLinear"}:         "e5c8fe092c623a8b01d5bec41b3b00bf",
		{"NRGBA64", "ResizeNearest"}:        "c996bdd6a1b3ca9f437c3c1c1b7a0d37",
		{"NRGBA64", "Blur"}:                 "c150235eb55ea26b6f7d0c1458d5e24d",
		{"NRGBA64", "Rotate90"}:             "04b38e033d0b80dae846cdeaecb82ea8",
		{"NRGBA64", "Rotate270"}:            "86e14e8adcda03b9d4977edaa2283ef0",
		{"NRGBA64", "Rotate37"}:             "eaa67a120047de29bdfab0d0bb2e724f",
		{"RGBA", "Clone"}:                   "34eeefe2a8391e06ea17b2d392051212",
		{"RGBA", "ResizeLinear"}:            "c3b0a8c43beb21fb9012120fecb70aec",
		{"RGBA", "ResizeNearest"}:           "58e940d5c62727b300247e4555152ac9",
		{"RGBA", "Blur"}:                    "5d199e6bdbdd46ab41effc82e4e5605d",
		{"RGBA", "Rotate90"}:                "58cbaf440345f68b2651fdddef8eb106",
		{"RGBA", "Rotate270"}:               "42eb906de8465e6e6c5f7e202f855ffe",
		{"RGBA", "Rotate37"}:                "14e259420a0ae7dfb605aeac06bbe446",
		{"RGBA64", "Clone"}:                 "95d912f145be6a99bf392e5317811769",
		{"RGBA64", "ResizeLinear"}:          "e5c8fe092c623a8b01d5bec41b3b00bf",
		{"RGBA64", "ResizeNearest"}:         "c996bdd6a1b3ca9f437c3c1c1b7a0d37",
		{"RGBA64", "Blur"}:                  "c150235eb55ea26b6f7d0c1458d5e24d",
		{"RGBA64", "Rotate90"}:              "04b38e033d0b80dae846cdeaecb82ea8",
		{"RGBA64", "Rotate270"}:             "86e14e8adcda03b9d4977edaa2283ef0",
		{"RGBA64", "Rotate37"}:              "eaa67a120047de29bdfab0d0bb2e724f",
		{"Gray", "Clone"}:                   "1315944806fc9ec0351b5259748b81de",
		{"Gray", "ResizeLinear"}:            "b1e3c3d647b5eed08202706c555e2219",
		{"Gray", "ResizeNearest"}:           "ec569105014222e85efaffda87da90a7",
		{"Gray", "Blur"}:                    "e231dd6b1f01ff52c0d04a8652d3684b",
		{"Gray", "Rotate90"}:                "7b54b515d05cc7ce43a934f565266ac4",
		{"Gray", "Rotate270"}:               "4f61c4c48a07296688741fc1b4c57e81",
		{"Gray", "Rotate37"}:                "a2e73f8ff337353f6485cd61b61bcdae",
		{"Gray16", "Clone"}:                 "1315944806fc9ec0351b5259748b81de",
		{"Gray16", "ResizeLinear"}:          "b1e3c3d647b5eed08202706c555e2219",
		{"Gray16", "ResizeNearest"}:         "ec569105014222e85efaffda87da90a7",
		{"Gray16", "Blur"}:                  "e231dd6b1f01ff52c0d04a8652d3684b",
		{"Gray16", "Rotate90"}:              "7b54b515d05cc7ce43a934f565266ac4",
		{"Gray16", "Rotate270"}:             "4f61c4c48a07296688741fc1b4c57e81",
		{"Gray16", "Rotate37"}:              "a2e73f8ff337353f6485cd61b61bcdae",
		{"YCbCr-420", "Clone"}:              "09e0f1c83d61bdbd4e9bd9fcd662ba2b",
		{"YCbCr-420", "ResizeLinear"}:       "89d9d74c2972569bdeb70103cd802274",
		{"YCbCr-420", "ResizeNearest"}:      "37cdb42f741c181904426001b7aeb23b",
		{"YCbCr-420", "Blur"}:               "8653e96390be4888d76aaa5edd656bf1",
		{"YCbCr-420", "Rotate90"}:           "9a1af64087ed596d51d80f15865e7222",
		{"YCbCr-420", "Rotate270"}:          "911034a9b9a53acc0862cd62b28c4df4",
		{"YCbCr-420", "Rotate37"}:           "cf8aa614f89abb9f360d28f12304d8f0",
		{"Paletted", "Clone"}:               "d81b635a35c59faab5521debdbfaa43c",
		{"Paletted", "ResizeLinear"}:        "302f91e82cbac60e5badc3e2bedca497",
		{"Paletted", "ResizeNearest"}:       "4d77b6d36b1801e44f4f99b83f271241",
		{"Paletted", "Blur"}:                "3c163ff445a85ec02c49f192c0b934e7",
		{"Paletted", "Rotate90"}:            "742ae8b2222b8b9fee995b2a6c18e104",
		{"Paletted", "Rotate270"}:           "79576068ff930077e18dc33ae2628ec9",
		{"Paletted", "Rotate37"}:            "d922c8c7359e952a3ece4596e5839f24",
		{"Alpha", "Clone"}:                  "d1d853740d924473111b591421cd957f",
		{"Alpha", "ResizeLinear"}:           "6bffe6ed6619c5e0a34ea56ef19e2059",
		{"Alpha", "ResizeNearest"}:          "7c82ab2d9216ed716d6d60a031a4e4a4",
		{"Alpha", "Blur"}:                   "5a2c8063a7f7398261f78ff26dd89bc3",
		{"Alpha", "Rotate90"}:               "aca138b96e4d1195097710f033160e1a",
		{"Alpha", "Rotate270"}:              "841bd79f4a4220aa54cb36eb139567b8",
		{"Alpha", "Rotate37"}:               "68fe5237eb843a216921717e744482f1",
		{"Alpha16", "Clone"}:                "d1d853740d924473111b591421cd957f",
		{"Alpha16", "ResizeLinear"}:         "6bffe6ed6619c5e0a34ea56ef19e2059",
		{"Alpha16", "ResizeNearest"}:        "7c82ab2d9216ed716d6d60a031a4e4a4",
		{"Alpha16", "Blur"}:                 "5a2c8063a7f7398261f78ff26dd89bc3",
		{"Alpha16", "Rotate90"}:             "aca138b96e4d1195097710f033160e1a",
		{"Alpha16", "Rotate270"}:            "841bd79f4a4220aa54cb36eb139567b8",
		{"Alpha16", "Rotate37"}:             "68fe5237eb843a216921717e744482f1",
		{"Generic", "Clone"}:                "95d912f145be6a99bf392e5317811769",
		{"Generic", "ResizeLinear"}:         "e5c8fe092c623a8b01d5bec41b3b00bf",
		{"Generic", "ResizeNearest"}:        "c996bdd6a1b3ca9f437c3c1c1b7a0d37",
		{"Generic", "Blur"}:                 "c150235eb55ea26b6f7d0c1458d5e24d",
		{"Generic", "Rotate90"}:             "04b38e033d0b80dae846cdeaecb82ea8",
		{"Generic", "Rotate270"}:            "86e14e8adcda03b9d4977edaa2283ef0",
		{"Generic", "Rotate37"}:             "eaa67a120047de29bdfab0d0bb2e724f",
		{"NRGBA-SubImage", "Clone"}:         "c683b8c44e0cfbaa9537067e1585b273",
		{"NRGBA-SubImage", "ResizeLinear"}:  "3ff17459bd3b7f040a3c712ea6886bce",
		{"NRGBA-SubImage", "ResizeNearest"}: "dea92f2bddcb9dd4fe6ac0784e2be436",
		{"NRGBA-SubImage", "Blur"}:          "c34279deb5839977d8efd104acd00b18",
		{"NRGBA-SubImage", "Rotate90"}:      "9b7c185f2221f8a5e9c5acbd6413de0e",
		{"NRGBA-SubImage", "Rotate270"}:     "fae9975667ed4c0e5bc39ca8cc83f9b8",
		{"NRGBA-SubImage", "Rotate37"}:      "18678dc46c51b4ff4577457e80d626c7",
		{"RGBA-SubImage", "Clone"}:          "34eeefe2a8391e06ea17b2d392051212",
		{"RGBA-SubImage", "ResizeLinear"}:   "c3b0a8c43beb21fb9012120fecb70aec",
		{"RGBA-SubImage", "ResizeNearest"}:  "58e940d5c62727b300247e4555152ac9",
		{"RGBA-SubImage", "Blur"}:           "5d199e6bdbdd46ab41effc82e4e5605d",
		{"RGBA-SubImage", "Rotate90"}:       "58cbaf440345f68b2651fdddef8eb106",
		{"RGBA-SubImage", "Rotate270"}:      "42eb906de8465e6e6c5f7e202f855ffe",
		{"RGBA-SubImage", "Rotate37"}:       "14e259420a0ae7dfb605aeac06bbe446",
	}
	for _, f := range goldenFormats() {
		for _, op := range goldenOps() {
			t.Run(f.name+"/"+op.name, func(t *testing.T) {
				out := op.fn(f.img())
				got := fmt.Sprintf("%x", md5.Sum(out.Pix))
				key := [2]string{f.name, op.name}
				exp, ok := want[key]
				if !ok {
					t.Fatalf("no golden hash for %v (got %s)", key, got)
				}
				if got != exp {
					t.Fatalf("output changed: got %s want %s", got, exp)
				}
			})
		}
	}
}
