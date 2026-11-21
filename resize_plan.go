package imaging

import (
	"container/list"
	"image"
	"math"
	"sync"
	"unsafe"
)

// ResizePlan is a precomputed, reusable resize operation bound to a fixed
// source size, destination size and resample filter. A plan precomputes the
// resampling weights once, so applying it to many images of the same size
// (e.g. a batch of avatars) avoids recomputing the weights for every image.
//
// A plan is immutable after creation and safe for concurrent use by multiple
// goroutines. Applying a plan never modifies the input image or the plan
// itself, and produces a result identical to Resize with the same parameters.
//
// Example:
//
//	plan := imaging.NewResizePlan(800, 600, 100, 100, imaging.Lanczos)
//	for _, img := range batch {
//		thumbnails = append(thumbnails, plan.Apply(img))
//	}
type ResizePlan struct {
	srcW, srcH int
	dstW, dstH int

	valid    bool // false: Apply always returns an empty image
	sameSize bool // src == dst: Apply clones the input
	nearest  bool // nearest-neighbor fast path (filter.Support <= 0)

	hWeights [][]indexWeight // horizontal weights, nil if width unchanged
	vWeights [][]indexWeight // vertical weights, nil if height unchanged
}

// NewResizePlan creates a resize plan for images of size srcW x srcH resized
// to dstW x dstH using the given filter. If one of dstW or dstH is 0, the
// source aspect ratio is preserved (same rule as Resize). Negative or zero
// destination sizes, or a non-positive source size, produce a plan whose
// Apply always returns an empty image.
func NewResizePlan(srcW, srcH, dstW, dstH int, filter ResampleFilter) *ResizePlan {
	p := &ResizePlan{srcW: srcW, srcH: srcH}

	if dstW < 0 || dstH < 0 {
		return p
	}
	if dstW == 0 && dstH == 0 {
		return p
	}
	if srcW <= 0 || srcH <= 0 {
		return p
	}

	// If new width or height is 0 then preserve aspect ratio, minimum 1px.
	if dstW == 0 {
		tmpW := float64(dstH) * float64(srcW) / float64(srcH)
		dstW = int(math.Max(1.0, math.Floor(tmpW+0.5)))
	}
	if dstH == 0 {
		tmpH := float64(dstW) * float64(srcH) / float64(srcW)
		dstH = int(math.Max(1.0, math.Floor(tmpH+0.5)))
	}

	p.dstW, p.dstH = dstW, dstH
	p.valid = true

	if srcW == dstW && srcH == dstH {
		p.sameSize = true
		return p
	}

	if filter.Support <= 0 {
		// Nearest-neighbor special case: no weights needed.
		p.nearest = true
		return p
	}

	if srcW != dstW {
		p.hWeights = precomputeWeights(dstW, srcW, filter)
	}
	if srcH != dstH {
		p.vWeights = precomputeWeights(dstH, srcH, filter)
	}
	return p
}

// SrcSize returns the source image size the plan is bound to.
func (p *ResizePlan) SrcSize() (width, height int) {
	return p.srcW, p.srcH
}

// DstSize returns the destination size produced by the plan.
func (p *ResizePlan) DstSize() (width, height int) {
	return p.dstW, p.dstH
}

// Apply resizes img according to the plan and returns the transformed image.
// The result is pixel-identical to calling Resize with the same sizes and
// filter. The input image is not modified.
//
// If img does not have the source size the plan was created for, or the plan
// was created with invalid sizes, Apply returns an empty image.
func (p *ResizePlan) Apply(img image.Image) *image.NRGBA {
	if p == nil || !p.valid {
		return &image.NRGBA{}
	}
	if img.Bounds().Dx() != p.srcW || img.Bounds().Dy() != p.srcH {
		return &image.NRGBA{}
	}

	if p.sameSize {
		return Clone(img)
	}
	if p.nearest {
		return resizeNearest(img, p.dstW, p.dstH)
	}

	src := newScanner(img)
	if p.hWeights != nil && p.vWeights != nil {
		mid := resizeHorizontalWithWeights(src, p.dstW, p.hWeights)
		return resizeVerticalWithWeights(newScanner(mid), p.dstH, p.vWeights)
	}
	if p.hWeights != nil {
		return resizeHorizontalWithWeights(src, p.dstW, p.hWeights)
	}
	return resizeVerticalWithWeights(src, p.dstH, p.vWeights)
}

// planKey identifies a cached plan. Custom kernels are distinguished by the
// identity of the kernel function value, not by any name, so two different
// kernels can never share a plan by accident.
type planKey struct {
	srcW, srcH, dstW, dstH int
	support                float64
	kernel                 uintptr
}

// kernelID returns the identity of a kernel function value. A func value is a
// pointer to its closure, so distinct closures (even ones sharing code, e.g.
// produced by the same factory with different captured parameters) have
// distinct identities, while the same func value compares equal across calls.
// reflect.Value.Pointer cannot be used here: it returns the shared code
// pointer and would conflate distinct closures.
func kernelID(kernel func(float64) float64) uintptr {
	if kernel == nil {
		return 0
	}
	return *(*uintptr)(unsafe.Pointer(&kernel))
}

// ResizePlanCache is a bounded cache of resize plans. It allows reusing plans
// across a batch of images without keeping an unbounded number of weight
// tables alive: when the cache is full, the least recently used plan is
// evicted. The zero value is not usable; create a cache with
// NewResizePlanCache. A cache is safe for concurrent use.
type ResizePlanCache struct {
	mu    sync.Mutex
	max   int
	lru   *list.List // front = most recently used; elements are cacheEntry
	byKey map[planKey]*list.Element
}

type cacheEntry struct {
	key  planKey
	plan *ResizePlan
}

// NewResizePlanCache creates a plan cache holding at most maxEntries plans.
// A maxEntries <= 0 disables caching: Get then always builds a fresh plan.
func NewResizePlanCache(maxEntries int) *ResizePlanCache {
	return &ResizePlanCache{
		max:   maxEntries,
		lru:   list.New(),
		byKey: make(map[planKey]*list.Element),
	}
}

// Get returns the cached plan for the given sizes and filter, creating and
// caching it on first use. Plans for the same sizes and the same filter value
// are shared; filters with different kernel functions never share a plan.
func (c *ResizePlanCache) Get(srcW, srcH, dstW, dstH int, filter ResampleFilter) *ResizePlan {
	key := planKey{
		srcW: srcW, srcH: srcH, dstW: dstW, dstH: dstH,
		support: filter.Support,
		kernel:  kernelID(filter.Kernel),
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.byKey[key]; ok {
		c.lru.MoveToFront(e)
		return e.Value.(cacheEntry).plan
	}

	plan := NewResizePlan(srcW, srcH, dstW, dstH, filter)
	if c.max <= 0 {
		return plan
	}

	for c.lru.Len() >= c.max {
		back := c.lru.Back()
		delete(c.byKey, back.Value.(cacheEntry).key)
		c.lru.Remove(back)
	}
	c.byKey[key] = c.lru.PushFront(cacheEntry{key: key, plan: plan})
	return plan
}

// Len returns the number of plans currently held by the cache.
func (c *ResizePlanCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// Clear removes all cached plans, releasing their weight tables.
func (c *ResizePlanCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Init()
	c.byKey = make(map[planKey]*list.Element)
}
