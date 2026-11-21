package imaging

import (
	"fmt"
	"image"
	"math"
	"math/rand"
	"sync"
	"testing"
)

// randomNRGBA returns a deterministic pseudo-random image of the given size.
func randomNRGBA(rnd *rand.Rand, w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rnd.Read(img.Pix)
	return img
}

func TestResizePlanMatchesResize(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))

	custom := ResampleFilter{
		Support: 1.5,
		Kernel: func(x float64) float64 {
			x = math.Abs(x)
			if x < 1.5 {
				return 1.0 - x/1.5
			}
			return 0
		},
	}

	testCases := []struct {
		name       string
		srcW, srcH int
		dstW, dstH int
		f          ResampleFilter
	}{
		{"down both", 200, 150, 100, 100, Lanczos},
		{"down keep h", 200, 150, 100, 150, CatmullRom},
		{"down keep w", 200, 150, 200, 75, Linear},
		{"up both", 60, 40, 120, 90, Lanczos},
		{"aspect w0", 200, 150, 0, 100, Box},
		{"aspect h0", 200, 150, 100, 0, Linear},
		{"same size", 100, 100, 100, 100, Lanczos},
		{"nearest", 200, 150, 100, 100, NearestNeighbor},
		{"custom kernel", 200, 150, 100, 100, custom},
		{"1px src", 1, 1, 10, 10, Lanczos},
		{"1px dst", 200, 150, 1, 1, Lanczos},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plan := NewResizePlan(tc.srcW, tc.srcH, tc.dstW, tc.dstH, tc.f)

			// Apply the plan to several images of different content.
			for i := 0; i < 4; i++ {
				src := randomNRGBA(rnd, tc.srcW, tc.srcH)
				before := append([]uint8(nil), src.Pix...)

				got := plan.Apply(src)
				want := Resize(src, tc.dstW, tc.dstH, tc.f)

				if !compareNRGBA(got, want, 0) {
					t.Fatalf("plan result differs from Resize for image %d", i)
				}
				if !compareBytes(src.Pix, before, 0) {
					t.Fatalf("input image modified by Apply")
				}
			}
		})
	}
}

func TestResizePlanInvalid(t *testing.T) {
	src := randomNRGBA(rand.New(rand.NewSource(1)), 10, 10)
	empty := &image.NRGBA{}

	testCases := []struct {
		name       string
		srcW, srcH int
		dstW, dstH int
	}{
		{"negative w", 10, 10, -1, 10},
		{"negative h", 10, 10, 10, -1},
		{"zero both", 10, 10, 0, 0},
		{"empty src w", 0, 10, 5, 5},
		{"empty src h", 10, 0, 5, 5},
		{"empty src both", 0, 0, 5, 5},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plan := NewResizePlan(tc.srcW, tc.srcH, tc.dstW, tc.dstH, Lanczos)
			got := plan.Apply(src)
			if !compareNRGBA(got, empty, 0) {
				t.Fatalf("got %#v want empty image", got)
			}
		})
	}

	t.Run("nil plan", func(t *testing.T) {
		var plan *ResizePlan
		if got := plan.Apply(src); !compareNRGBA(got, empty, 0) {
			t.Fatalf("got %#v want empty image", got)
		}
	})

	t.Run("size mismatch", func(t *testing.T) {
		plan := NewResizePlan(10, 10, 5, 5, Lanczos)
		got := plan.Apply(randomNRGBA(rand.New(rand.NewSource(2)), 20, 20))
		if !compareNRGBA(got, empty, 0) {
			t.Fatalf("got %#v want empty image", got)
		}
	})

	t.Run("sizes", func(t *testing.T) {
		plan := NewResizePlan(200, 150, 0, 100, Lanczos)
		w, h := plan.SrcSize()
		if w != 200 || h != 150 {
			t.Fatalf("SrcSize got %dx%d want 200x150", w, h)
		}
		w, h = plan.DstSize()
		if w != 133 || h != 100 {
			t.Fatalf("DstSize got %dx%d want 133x100", w, h)
		}
	})
}

func TestResizePlanConcurrent(t *testing.T) {
	const (
		srcW, srcH = 128, 96
		dstW, dstH = 32, 24
		workers    = 8
		perWorker  = 8
	)
	rnd := rand.New(rand.NewSource(7))

	plan := NewResizePlan(srcW, srcH, dstW, dstH, Lanczos)

	images := make([]*image.NRGBA, workers*perWorker)
	want := make([]*image.NRGBA, len(images))
	for i := range images {
		images[i] = randomNRGBA(rnd, srcW, srcH)
		want[i] = Resize(images[i], dstW, dstH, Lanczos)
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				idx := w*perWorker + i
				got := plan.Apply(images[idx])
				if !compareNRGBA(got, want[idx], 0) {
					t.Errorf("concurrent result %d differs from Resize", idx)
				}
			}
		}(w)
	}
	wg.Wait()

	// The plan must still produce correct results after concurrent use.
	got := plan.Apply(images[0])
	if !compareNRGBA(got, want[0], 0) {
		t.Fatalf("plan corrupted by concurrent use")
	}
}

func TestResizePlanCache(t *testing.T) {
	src := randomNRGBA(rand.New(rand.NewSource(3)), 64, 48)

	t.Run("shares identical plans", func(t *testing.T) {
		c := NewResizePlanCache(4)
		p1 := c.Get(64, 48, 32, 24, Lanczos)
		p2 := c.Get(64, 48, 32, 24, Lanczos)
		if p1 != p2 {
			t.Fatalf("expected shared plan for identical parameters")
		}
		if c.Len() != 1 {
			t.Fatalf("Len got %d want 1", c.Len())
		}
	})

	t.Run("distinguishes custom kernels", func(t *testing.T) {
		// Two filters with equal Support and same-shaped closures produced by
		// one factory, but different kernel behavior: they must not share a plan.
		factory := func(cutoff float64) ResampleFilter {
			return ResampleFilter{
				Support: 1.0,
				Kernel: func(x float64) float64 {
					x = math.Abs(x)
					if x < cutoff {
						return 1.0
					}
					return 0
				},
			}
		}
		f1 := factory(1.0)
		f2 := factory(0.5)

		c := NewResizePlanCache(4)
		p1 := c.Get(64, 48, 32, 24, f1)
		p2 := c.Get(64, 48, 32, 24, f2)
		if p1 == p2 {
			t.Fatalf("different custom kernels must not share a plan")
		}
		if c.Len() != 2 {
			t.Fatalf("Len got %d want 2", c.Len())
		}

		got1 := p1.Apply(src)
		want1 := Resize(src, 32, 24, f1)
		if !compareNRGBA(got1, want1, 0) {
			t.Fatalf("cached plan result differs from Resize for kernel 1")
		}
		got2 := p2.Apply(src)
		want2 := Resize(src, 32, 24, f2)
		if !compareNRGBA(got2, want2, 0) {
			t.Fatalf("cached plan result differs from Resize for kernel 2")
		}
	})

	t.Run("bounded with LRU eviction", func(t *testing.T) {
		c := NewResizePlanCache(3)
		plans := make([]*ResizePlan, 0, 5)
		for i := 0; i < 5; i++ {
			plans = append(plans, c.Get(64, 48, 8+i, 8+i, Linear))
		}
		if c.Len() != 3 {
			t.Fatalf("Len got %d want 3", c.Len())
		}
		// The two oldest plans (sizes 8 and 9) must have been evicted.
		if p := c.Get(64, 48, 8, 8, Linear); p == plans[0] {
			t.Fatalf("oldest plan was not evicted")
		}
		if p := c.Get(64, 48, 12, 12, Linear); p != plans[4] {
			t.Fatalf("most recent plan was wrongly evicted")
		}
	})

	t.Run("LRU touch on hit", func(t *testing.T) {
		c := NewResizePlanCache(2)
		p1 := c.Get(64, 48, 10, 10, Box)
		c.Get(64, 48, 20, 20, Box)
		// Touch p1 so the 20x20 plan becomes the oldest.
		if got := c.Get(64, 48, 10, 10, Box); got != p1 {
			t.Fatalf("cache hit returned a different plan")
		}
		c.Get(64, 48, 30, 30, Box) // evicts the 20x20 plan
		if got := c.Get(64, 48, 10, 10, Box); got != p1 {
			t.Fatalf("recently used plan was wrongly evicted")
		}
	})

	t.Run("clear", func(t *testing.T) {
		c := NewResizePlanCache(4)
		p1 := c.Get(64, 48, 32, 24, Lanczos)
		c.Clear()
		if c.Len() != 0 {
			t.Fatalf("Len got %d want 0 after Clear", c.Len())
		}
		if p2 := c.Get(64, 48, 32, 24, Lanczos); p2 == p1 {
			t.Fatalf("expected a fresh plan after Clear")
		}
	})

	t.Run("disabled cache", func(t *testing.T) {
		c := NewResizePlanCache(0)
		p1 := c.Get(64, 48, 32, 24, Lanczos)
		p2 := c.Get(64, 48, 32, 24, Lanczos)
		if p1 == p2 {
			t.Fatalf("disabled cache must not share plans")
		}
		if c.Len() != 0 {
			t.Fatalf("Len got %d want 0 for disabled cache", c.Len())
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		c := NewResizePlanCache(4)
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					plan := c.Get(64, 48, 32, 24, Lanczos)
					got := plan.Apply(src)
					want := Resize(src, 32, 24, Lanczos)
					if !compareNRGBA(got, want, 0) {
						t.Errorf("cached plan result differs from Resize")
						return
					}
					_ = c.Len()
					_ = w
				}
			}(w)
		}
		wg.Wait()
		if c.Len() > 4 {
			t.Fatalf("cache grew beyond its limit: %d", c.Len())
		}
	})
}

func BenchmarkResizeBatch(b *testing.B) {
	const batchSize = 16
	rnd := rand.New(rand.NewSource(5))
	images := make([]*image.NRGBA, batchSize)
	for i := range images {
		images[i] = randomNRGBA(rnd, 512, 512)
	}

	for _, filter := range []struct {
		name string
		f    ResampleFilter
	}{
		{"Linear", Linear},
		{"Lanczos", Lanczos},
	} {
		b.Run(fmt.Sprintf("Resize %s", filter.name), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				Resize(images[i%batchSize], 100, 100, filter.f)
			}
		})
		b.Run(fmt.Sprintf("Plan %s", filter.name), func(b *testing.B) {
			plan := NewResizePlan(512, 512, 100, 100, filter.f)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				plan.Apply(images[i%batchSize])
			}
		})
	}
}
