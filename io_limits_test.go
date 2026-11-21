package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// pngWithDimensions builds a minimal PNG file header (signature + IHDR) with
// the given dimensions. The pixel data is missing, so the file is only useful
// for testing header-based checks.
func pngWithDimensions(t *testing.T, w, h uint32) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // color type RGBA
	binary.Write(buf, binary.BigEndian, uint32(len(ihdr)))
	buf.WriteString("IHDR")
	buf.Write(ihdr)
	crc := crc32.NewIEEE()
	crc.Write([]byte("IHDR"))
	crc.Write(ihdr)
	binary.Write(buf, binary.BigEndian, crc.Sum32())
	return buf.Bytes()
}

// bmpWithDimensions builds a minimal BMP file header with the given dimensions.
func bmpWithDimensions(t *testing.T, w, h int32) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	buf.WriteString("BM")
	binary.Write(buf, binary.LittleEndian, uint32(54)) // file size
	binary.Write(buf, binary.LittleEndian, uint32(0))  // reserved
	binary.Write(buf, binary.LittleEndian, uint32(54)) // pixel data offset
	binary.Write(buf, binary.LittleEndian, uint32(40)) // info header size
	binary.Write(buf, binary.LittleEndian, w)
	binary.Write(buf, binary.LittleEndian, h)
	binary.Write(buf, binary.LittleEndian, uint16(1))  // planes
	binary.Write(buf, binary.LittleEndian, uint16(24)) // bpp
	binary.Write(buf, binary.LittleEndian, uint32(0))  // compression
	binary.Write(buf, binary.LittleEndian, uint32(0))  // image size
	binary.Write(buf, binary.LittleEndian, uint32(0))  // x ppm
	binary.Write(buf, binary.LittleEndian, uint32(0))  // y ppm
	binary.Write(buf, binary.LittleEndian, uint32(0))  // colors used
	binary.Write(buf, binary.LittleEndian, uint32(0))  // colors important
	return buf.Bytes()
}

// nonSeekable hides any Seeker/Len/Size capabilities of r.
type nonSeekable struct {
	io.Reader
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := Encode(buf, img, PNG); err != nil {
		t.Fatalf("failed to encode PNG: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeLimitsRejectsHugeDeclaredDimensions(t *testing.T) {
	// 100000x100000 RGBA would take ~40GB decoded; 0x7fffffff on a side is
	// the maximum a BMP header can declare.
	testCases := []struct {
		name string
		data []byte
	}{
		{"png", pngWithDimensions(t, 100000, 100000)},
		{"bmp", bmpWithDimensions(t, 0x7fffffff, 0x7fffffff)},
	}

	limits := [][]DecodeOption{
		{MaxPixels(1 << 20)},
		{MaxDimension(4096)},
		{MaxPixels(1 << 20), MaxDimension(4096), MaxInputBytes(1 << 20)},
		{MaxPixels(1 << 20), AutoOrientation(true)},
	}

	for _, tc := range testCases {
		for _, opts := range limits {
			start := time.Now()
			var ms1, ms2 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&ms1)
			img, err := Decode(nonSeekable{bytes.NewReader(tc.data)}, opts...)
			runtime.ReadMemStats(&ms2)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatalf("%s: expected error, got image %v", tc.name, img.Bounds())
			}
			if !errors.Is(err, ErrImageTooLarge) {
				t.Fatalf("%s: got error %v, want ErrImageTooLarge", tc.name, err)
			}
			if elapsed > 5*time.Second {
				t.Fatalf("%s: rejection took too long: %v", tc.name, elapsed)
			}
			if allocated := ms2.TotalAlloc - ms1.TotalAlloc; allocated > 16<<20 {
				t.Fatalf("%s: rejection allocated %d bytes, want < 16MB", tc.name, allocated)
			}
		}
	}
}

func TestDecodeLimitsDimensionOverflowDoesNotPanic(t *testing.T) {
	// Declared dimensions near the int32 limit must not overflow the pixel
	// count computation: the budget check has to reject the image cleanly
	// instead of wrapping around and letting the decoder run.
	data := bmpWithDimensions(t, 0x7fffffff, 0x7fffffff)
	_, err := Decode(bytes.NewReader(data), MaxPixels(1<<20))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("got error %v, want ErrImageTooLarge", err)
	}
}

func TestDecodeLimitsTruncatedFile(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	full := encodePNG(t, img)
	truncated := full[:len(full)/2]

	_, err := Decode(nonSeekable{bytes.NewReader(truncated)}, MaxPixels(1<<20), MaxInputBytes(1<<20))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrImageTooLarge) || errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("truncated input reported as budget error: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("got error %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestDecodeLimitsUnsupportedFormat(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte("bad data")), MaxPixels(100), MaxInputBytes(100))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, image.ErrFormat) {
		t.Fatalf("got error %v, want image.ErrFormat", err)
	}
	if errors.Is(err, ErrImageTooLarge) || errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("unsupported format reported as budget error: %v", err)
	}
}

func TestDecodeLimitsMaxInputBytes(t *testing.T) {
	data := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 8, 8)))
	size := int64(len(data))

	_, err := Decode(bytes.NewReader(data), MaxInputBytes(size-1))
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("got error %v, want ErrInputTooLarge", err)
	}

	img, err := Decode(bytes.NewReader(data), MaxInputBytes(size))
	if err != nil {
		t.Fatalf("exact-size input rejected: %v", err)
	}
	if img.Bounds() != image.Rect(0, 0, 8, 8) {
		t.Fatalf("got bounds %v, want %v", img.Bounds(), image.Rect(0, 0, 8, 8))
	}
}

func TestDecodeLimitsBoundaries(t *testing.T) {
	data := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 10, 20)))

	testCases := []struct {
		name    string
		opts    []DecodeOption
		wantErr error
	}{
		{"pixels exact", []DecodeOption{MaxPixels(200)}, nil},
		{"pixels one less", []DecodeOption{MaxPixels(199)}, ErrImageTooLarge},
		{"dimension exact", []DecodeOption{MaxDimension(20)}, nil},
		{"dimension one less", []DecodeOption{MaxDimension(19)}, ErrImageTooLarge},
		{"dimension below width", []DecodeOption{MaxDimension(9)}, ErrImageTooLarge},
		{"no limits", []DecodeOption{}, nil},
		{"zero limits mean unlimited", []DecodeOption{MaxPixels(0), MaxDimension(0), MaxInputBytes(0)}, nil},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := Decode(nonSeekable{bytes.NewReader(data)}, tc.opts...)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
				if img.Bounds() != image.Rect(0, 0, 10, 20) {
					t.Fatalf("got bounds %v, want %v", img.Bounds(), image.Rect(0, 0, 10, 20))
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestDecodeLimitsAllFormats(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 6, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 6; x++ {
			src.Pix[y*src.Stride+x*4+0] = uint8(x * 40)
			src.Pix[y*src.Stride+x*4+1] = uint8(y * 60)
			src.Pix[y*src.Stride+x*4+2] = 0x80
			src.Pix[y*src.Stride+x*4+3] = 0xff
		}
	}

	for _, format := range []Format{JPEG, PNG, GIF, TIFF, BMP} {
		buf := &bytes.Buffer{}
		if err := Encode(buf, src, format); err != nil {
			t.Fatalf("%s: failed to encode: %v", format, err)
		}
		data := buf.Bytes()
		img, err := Decode(nonSeekable{bytes.NewReader(data)}, MaxPixels(24), MaxDimension(6), MaxInputBytes(int64(len(data))))
		if err != nil {
			t.Fatalf("%s: decode with exact limits failed: %v", format, err)
		}
		if img.Bounds() != image.Rect(0, 0, 6, 4) {
			t.Fatalf("%s: got bounds %v, want %v", format, img.Bounds(), image.Rect(0, 0, 6, 4))
		}

		_, err = Decode(nonSeekable{bytes.NewReader(data)}, MaxPixels(23))
		if !errors.Is(err, ErrImageTooLarge) {
			t.Fatalf("%s: got error %v, want ErrImageTooLarge", format, err)
		}
	}
}

func TestOpenLimitsMaxInputBytes(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging")
	if err != nil {
		t.Fatalf("failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(dir)

	filename := filepath.Join(dir, "test.png")
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	if err := Save(src, filename); err != nil {
		t.Fatalf("failed to save image: %v", err)
	}
	fi, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	_, err = Open(filename, MaxInputBytes(fi.Size()-1))
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("got error %v, want ErrInputTooLarge", err)
	}

	img, err := Open(filename, MaxInputBytes(fi.Size()))
	if err != nil {
		t.Fatalf("exact-size file rejected: %v", err)
	}
	if img.Bounds() != image.Rect(0, 0, 8, 8) {
		t.Fatalf("got bounds %v, want %v", img.Bounds(), image.Rect(0, 0, 8, 8))
	}
}

func TestDecodeLimitsAutoOrientation(t *testing.T) {
	for _, path := range []string{
		"testdata/orientation_0.jpg",
		"testdata/orientation_5.jpg",
		"testdata/orientation_8.jpg",
	} {
		want, err := Open(path, AutoOrientation(true))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		wb := want.Bounds()
		w, h := wb.Dx(), wb.Dy()

		// Generous limits must not change the auto-orientation result.
		got, err := Open(path, AutoOrientation(true), MaxPixels(w*h), MaxDimension(max(w, h)))
		if err != nil {
			t.Fatalf("%s: decode with exact limits failed: %v", path, err)
		}
		if got.Bounds() != wb {
			t.Fatalf("%s: got bounds %v, want %v", path, got.Bounds(), wb)
		}
		if !compareNRGBA(Clone(got), Clone(want), 0) {
			t.Fatalf("%s: decoded image with limits differs from unlimited decode", path)
		}

		// The limits apply to the rotated (final) dimensions.
		_, err = Open(path, AutoOrientation(true), MaxPixels(w*h-1))
		if !errors.Is(err, ErrImageTooLarge) {
			t.Fatalf("%s: got error %v, want ErrImageTooLarge", path, err)
		}
		_, err = Open(path, AutoOrientation(true), MaxDimension(max(w, h)-1))
		if !errors.Is(err, ErrImageTooLarge) {
			t.Fatalf("%s: got error %v, want ErrImageTooLarge", path, err)
		}
	}
}

func TestDecodeLimitsNoGoroutineLeak(t *testing.T) {
	valid := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 4, 4)))
	malicious := pngWithDimensions(t, 100000, 100000)
	truncated := valid[:len(valid)/2]

	decode := func() {
		Decode(nonSeekable{bytes.NewReader(valid)}, AutoOrientation(true), MaxPixels(1<<20))
		Decode(nonSeekable{bytes.NewReader(malicious)}, AutoOrientation(true), MaxPixels(1<<20))
		Decode(nonSeekable{bytes.NewReader(truncated)}, AutoOrientation(true), MaxPixels(1<<20))
	}

	decode() // warm up
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		decode()
	}
	// Give any stray goroutine a chance to exit (or to be observed).
	for i := 0; i < 100; i++ {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
