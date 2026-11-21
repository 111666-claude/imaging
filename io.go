package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

type fileSystem interface {
	Create(string) (io.WriteCloser, error)
	Open(string) (io.ReadCloser, error)
}

type localFS struct{}

func (localFS) Create(name string) (io.WriteCloser, error) { return os.Create(name) }
func (localFS) Open(name string) (io.ReadCloser, error)    { return os.Open(name) }

var fs fileSystem = localFS{}

type decodeConfig struct {
	autoOrientation bool
	maxInputBytes   int64
	maxPixels       int64
	maxDimension    int
}

var defaultDecodeConfig = decodeConfig{
	autoOrientation: false,
}

// DecodeOption sets an optional parameter for the Decode and Open functions.
type DecodeOption func(*decodeConfig)

// AutoOrientation returns a DecodeOption that sets the auto-orientation mode.
// If auto-orientation is enabled, the image will be transformed after decoding
// according to the EXIF orientation tag (if present). By default it's disabled.
func AutoOrientation(enabled bool) DecodeOption {
	return func(c *decodeConfig) {
		c.autoOrientation = enabled
	}
}

// MaxInputBytes returns a DecodeOption that limits the number of bytes
// consumed from the input. If the limit is exceeded, Decode and Open fail
// with ErrInputTooLarge. A zero or negative value means no limit (default).
func MaxInputBytes(n int64) DecodeOption {
	return func(c *decodeConfig) {
		c.maxInputBytes = n
	}
}

// MaxPixels returns a DecodeOption that limits the total number of pixels
// (width*height) of the decoded image. The limit is enforced before pixel
// data is allocated whenever the image dimensions can be determined from the
// header. If the limit is exceeded, Decode and Open fail with
// ErrImageTooLarge. A zero or negative value means no limit (default).
func MaxPixels(n int) DecodeOption {
	return func(c *decodeConfig) {
		c.maxPixels = int64(n)
	}
}

// MaxDimension returns a DecodeOption that limits the width and height of
// the decoded image: neither side may exceed the given value. The limit is
// enforced before pixel data is allocated whenever the image dimensions can
// be determined from the header. If the limit is exceeded, Decode and Open
// fail with ErrImageTooLarge. A zero or negative value means no limit
// (default).
func MaxDimension(n int) DecodeOption {
	return func(c *decodeConfig) {
		c.maxDimension = n
	}
}

// ErrInputTooLarge means the input data exceeds the limit set by MaxInputBytes.
var ErrInputTooLarge = errors.New("imaging: input data exceeds the size limit")

// ErrImageTooLarge means the decoded image dimensions exceed the limits set
// by MaxPixels or MaxDimension.
var ErrImageTooLarge = errors.New("imaging: image dimensions exceed the limit")

// limitErrorReader reads at most n+1 bytes from r. Once more than n bytes
// have been consumed it reports ErrInputTooLarge instead of io.EOF, so that
// an over-limit input is distinguishable from a truncated one.
type limitErrorReader struct {
	r       io.Reader
	n       int64 // remaining allowed bytes, plus one
	exceeds bool
}

func (l *limitErrorReader) Read(p []byte) (int, error) {
	if l.exceeds {
		return 0, ErrInputTooLarge
	}
	if l.n <= 0 {
		l.exceeds = true
		return 0, ErrInputTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n <= 0 && err == nil {
		// The whole allowance (limit+1 bytes) was consumed: the input is
		// larger than the limit.
		l.exceeds = true
	}
	return n, err
}

// checkDimensions verifies the given image dimensions against the limits.
// The pixel count is computed in 64-bit arithmetic so that maliciously large
// declared dimensions cannot overflow it.
func checkDimensions(width, height int, cfg *decodeConfig) error {
	if cfg.maxDimension > 0 && (width > cfg.maxDimension || height > cfg.maxDimension) {
		return fmt.Errorf("%w: %dx%d exceeds the maximum side length of %d",
			ErrImageTooLarge, width, height, cfg.maxDimension)
	}
	if cfg.maxPixels > 0 && width > 0 && height > 0 {
		if int64(width)*int64(height) > cfg.maxPixels {
			return fmt.Errorf("%w: %dx%d exceeds the maximum of %d pixels",
				ErrImageTooLarge, width, height, cfg.maxPixels)
		}
	}
	return nil
}

// hasLimits reports whether any of the safety budget limits are set.
func (c *decodeConfig) hasLimits() bool {
	return c.maxInputBytes > 0 || c.maxPixels > 0 || c.maxDimension > 0
}

// Decode reads an image from r.
func Decode(r io.Reader, opts ...DecodeOption) (image.Image, error) {
	cfg := defaultDecodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	if cfg.hasLimits() {
		return decodeWithLimits(r, &cfg)
	}

	if !cfg.autoOrientation {
		img, _, err := image.Decode(r)
		return img, err
	}

	var orient orientation
	pr, pw := io.Pipe()
	r = io.TeeReader(r, pw)
	done := make(chan struct{})
	go func() {
		defer close(done)
		orient = readOrientation(pr)
		io.Copy(ioutil.Discard, pr)
	}()

	img, _, err := image.Decode(r)
	pw.Close()
	<-done
	if err != nil {
		return nil, err
	}

	return fixOrientation(img, orient), nil
}

// decodeWithLimits decodes an image from r enforcing the safety budget set in
// cfg. It works with any io.Reader, including non-seekable streams: the image
// header is sniffed through a tee into a small buffer, the declared dimensions
// are checked before any pixel data is allocated, and the buffered prefix is
// then replayed to the decoder. No background goroutines are started.
func decodeWithLimits(r io.Reader, cfg *decodeConfig) (image.Image, error) {
	var lr *limitErrorReader
	if cfg.maxInputBytes > 0 {
		lr = &limitErrorReader{r: r, n: cfg.maxInputBytes + 1}
		r = lr
	}

	// Sniff the image configuration. Every byte the format decoder reads is
	// copied into buf so the stream can be reassembled afterwards.
	var buf bytes.Buffer
	imgCfg, _, err := image.DecodeConfig(io.TeeReader(r, &buf))
	if err != nil {
		if lr != nil && lr.exceeds {
			return nil, fmt.Errorf("%w: more than %d bytes", ErrInputTooLarge, cfg.maxInputBytes)
		}
		return nil, err
	}

	// The EXIF orientation (if any) is read from the buffered header prefix:
	// for JPEG the APP1 marker always precedes the SOF marker that
	// DecodeConfig stops at, so the prefix contains the whole EXIF block.
	var orient orientation
	if cfg.autoOrientation {
		orient = readOrientation(bytes.NewReader(buf.Bytes()))
	}

	// Check the dimensions after the EXIF orientation is applied, so the
	// limits match the image the caller will actually receive.
	width, height := imgCfg.Width, imgCfg.Height
	if orient == orientationRotate90 || orient == orientationRotate270 ||
		orient == orientationTranspose || orient == orientationTransverse {
		width, height = height, width
	}
	if err := checkDimensions(width, height, cfg); err != nil {
		return nil, err
	}

	img, _, err := image.Decode(io.MultiReader(&buf, r))
	if err != nil {
		if lr != nil && lr.exceeds {
			return nil, fmt.Errorf("%w: more than %d bytes", ErrInputTooLarge, cfg.maxInputBytes)
		}
		return nil, err
	}
	if lr != nil && lr.exceeds {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrInputTooLarge, cfg.maxInputBytes)
	}

	// Backstop: re-check the actual decoded bounds in case the declared
	// dimensions did not match the decoded image.
	b := img.Bounds()
	if err := checkDimensions(b.Dx(), b.Dy(), cfg); err != nil {
		return nil, err
	}

	if cfg.autoOrientation {
		img = fixOrientation(img, orient)
	}
	return img, nil
}

// Open loads an image from file.
//
// Examples:
//
//	// Load an image from file.
//	img, err := imaging.Open("test.jpg")
//
//	// Load an image and transform it depending on the EXIF orientation tag (if present).
//	img, err := imaging.Open("test.jpg", imaging.AutoOrientation(true))
func Open(filename string, opts ...DecodeOption) (image.Image, error) {
	file, err := fs.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	cfg := defaultDecodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	// Reject oversize files up front when the file size is known, without
	// reading a single byte.
	if cfg.maxInputBytes > 0 {
		if st, ok := file.(interface{ Stat() (os.FileInfo, error) }); ok {
			if fi, err := st.Stat(); err == nil && fi.Size() > cfg.maxInputBytes {
				return nil, fmt.Errorf("%w: file size %d exceeds the limit of %d bytes",
					ErrInputTooLarge, fi.Size(), cfg.maxInputBytes)
			}
		}
	}

	if cfg.hasLimits() {
		return decodeWithLimits(file, &cfg)
	}
	return Decode(file, opts...)
}

// Format is an image file format.
type Format int

// Image file formats.
const (
	JPEG Format = iota
	PNG
	GIF
	TIFF
	BMP
)

var formatExts = map[string]Format{
	"jpg":  JPEG,
	"jpeg": JPEG,
	"png":  PNG,
	"gif":  GIF,
	"tif":  TIFF,
	"tiff": TIFF,
	"bmp":  BMP,
}

var formatNames = map[Format]string{
	JPEG: "JPEG",
	PNG:  "PNG",
	GIF:  "GIF",
	TIFF: "TIFF",
	BMP:  "BMP",
}

func (f Format) String() string {
	return formatNames[f]
}

// ErrUnsupportedFormat means the given image format is not supported.
var ErrUnsupportedFormat = errors.New("imaging: unsupported image format")

// FormatFromExtension parses image format from filename extension:
// "jpg" (or "jpeg"), "png", "gif", "tif" (or "tiff") and "bmp" are supported.
func FormatFromExtension(ext string) (Format, error) {
	if f, ok := formatExts[strings.ToLower(strings.TrimPrefix(ext, "."))]; ok {
		return f, nil
	}
	return -1, ErrUnsupportedFormat
}

// FormatFromFilename parses image format from filename:
// "jpg" (or "jpeg"), "png", "gif", "tif" (or "tiff") and "bmp" are supported.
func FormatFromFilename(filename string) (Format, error) {
	ext := filepath.Ext(filename)
	return FormatFromExtension(ext)
}

type encodeConfig struct {
	jpegQuality         int
	gifNumColors        int
	gifQuantizer        draw.Quantizer
	gifDrawer           draw.Drawer
	pngCompressionLevel png.CompressionLevel
}

var defaultEncodeConfig = encodeConfig{
	jpegQuality:         95,
	gifNumColors:        256,
	gifQuantizer:        nil,
	gifDrawer:           nil,
	pngCompressionLevel: png.DefaultCompression,
}

// EncodeOption sets an optional parameter for the Encode and Save functions.
type EncodeOption func(*encodeConfig)

// JPEGQuality returns an EncodeOption that sets the output JPEG quality.
// Quality ranges from 1 to 100 inclusive, higher is better. Default is 95.
func JPEGQuality(quality int) EncodeOption {
	return func(c *encodeConfig) {
		c.jpegQuality = quality
	}
}

// GIFNumColors returns an EncodeOption that sets the maximum number of colors
// used in the GIF-encoded image. It ranges from 1 to 256.  Default is 256.
func GIFNumColors(numColors int) EncodeOption {
	return func(c *encodeConfig) {
		c.gifNumColors = numColors
	}
}

// GIFQuantizer returns an EncodeOption that sets the quantizer that is used to produce
// a palette of the GIF-encoded image.
func GIFQuantizer(quantizer draw.Quantizer) EncodeOption {
	return func(c *encodeConfig) {
		c.gifQuantizer = quantizer
	}
}

// GIFDrawer returns an EncodeOption that sets the drawer that is used to convert
// the source image to the desired palette of the GIF-encoded image.
func GIFDrawer(drawer draw.Drawer) EncodeOption {
	return func(c *encodeConfig) {
		c.gifDrawer = drawer
	}
}

// PNGCompressionLevel returns an EncodeOption that sets the compression level
// of the PNG-encoded image. Default is png.DefaultCompression.
func PNGCompressionLevel(level png.CompressionLevel) EncodeOption {
	return func(c *encodeConfig) {
		c.pngCompressionLevel = level
	}
}

// Encode writes the image img to w in the specified format (JPEG, PNG, GIF, TIFF or BMP).
func Encode(w io.Writer, img image.Image, format Format, opts ...EncodeOption) error {
	cfg := defaultEncodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	switch format {
	case JPEG:
		if nrgba, ok := img.(*image.NRGBA); ok && nrgba.Opaque() {
			rgba := &image.RGBA{
				Pix:    nrgba.Pix,
				Stride: nrgba.Stride,
				Rect:   nrgba.Rect,
			}
			return jpeg.Encode(w, rgba, &jpeg.Options{Quality: cfg.jpegQuality})
		}
		return jpeg.Encode(w, img, &jpeg.Options{Quality: cfg.jpegQuality})

	case PNG:
		encoder := png.Encoder{CompressionLevel: cfg.pngCompressionLevel}
		return encoder.Encode(w, img)

	case GIF:
		return gif.Encode(w, img, &gif.Options{
			NumColors: cfg.gifNumColors,
			Quantizer: cfg.gifQuantizer,
			Drawer:    cfg.gifDrawer,
		})

	case TIFF:
		return tiff.Encode(w, img, &tiff.Options{Compression: tiff.Deflate, Predictor: true})

	case BMP:
		return bmp.Encode(w, img)
	}

	return ErrUnsupportedFormat
}

// Save saves the image to file with the specified filename.
// The format is determined from the filename extension:
// "jpg" (or "jpeg"), "png", "gif", "tif" (or "tiff") and "bmp" are supported.
//
// Examples:
//
//	// Save the image as PNG.
//	err := imaging.Save(img, "out.png")
//
//	// Save the image as JPEG with optional quality parameter set to 80.
//	err := imaging.Save(img, "out.jpg", imaging.JPEGQuality(80))
func Save(img image.Image, filename string, opts ...EncodeOption) (err error) {
	f, err := FormatFromFilename(filename)
	if err != nil {
		return err
	}
	file, err := fs.Create(filename)
	if err != nil {
		return err
	}
	err = Encode(file, img, f, opts...)
	errc := file.Close()
	if err == nil {
		err = errc
	}
	return err
}

// orientation is an EXIF flag that specifies the transformation
// that should be applied to image to display it correctly.
type orientation int

const (
	orientationUnspecified = 0
	orientationNormal      = 1
	orientationFlipH       = 2
	orientationRotate180   = 3
	orientationFlipV       = 4
	orientationTranspose   = 5
	orientationRotate270   = 6
	orientationTransverse  = 7
	orientationRotate90    = 8
)

// readOrientation tries to read the orientation EXIF flag from image data in r.
// If the EXIF data block is not found or the orientation flag is not found
// or any other error occures while reading the data, it returns the
// orientationUnspecified (0) value.
func readOrientation(r io.Reader) orientation {
	const (
		markerSOI      = 0xffd8
		markerAPP1     = 0xffe1
		exifHeader     = 0x45786966
		byteOrderBE    = 0x4d4d
		byteOrderLE    = 0x4949
		orientationTag = 0x0112
	)

	// Check if JPEG SOI marker is present.
	var soi uint16
	if err := binary.Read(r, binary.BigEndian, &soi); err != nil {
		return orientationUnspecified
	}
	if soi != markerSOI {
		return orientationUnspecified // Missing JPEG SOI marker.
	}

	// Find JPEG APP1 marker.
	for {
		var marker, size uint16
		if err := binary.Read(r, binary.BigEndian, &marker); err != nil {
			return orientationUnspecified
		}
		if err := binary.Read(r, binary.BigEndian, &size); err != nil {
			return orientationUnspecified
		}
		if marker>>8 != 0xff {
			return orientationUnspecified // Invalid JPEG marker.
		}
		if marker == markerAPP1 {
			break
		}
		if size < 2 {
			return orientationUnspecified // Invalid block size.
		}
		if _, err := io.CopyN(ioutil.Discard, r, int64(size-2)); err != nil {
			return orientationUnspecified
		}
	}

	// Check if EXIF header is present.
	var header uint32
	if err := binary.Read(r, binary.BigEndian, &header); err != nil {
		return orientationUnspecified
	}
	if header != exifHeader {
		return orientationUnspecified
	}
	if _, err := io.CopyN(ioutil.Discard, r, 2); err != nil {
		return orientationUnspecified
	}

	// Read byte order information.
	var (
		byteOrderTag uint16
		byteOrder    binary.ByteOrder
	)
	if err := binary.Read(r, binary.BigEndian, &byteOrderTag); err != nil {
		return orientationUnspecified
	}
	switch byteOrderTag {
	case byteOrderBE:
		byteOrder = binary.BigEndian
	case byteOrderLE:
		byteOrder = binary.LittleEndian
	default:
		return orientationUnspecified // Invalid byte order flag.
	}
	if _, err := io.CopyN(ioutil.Discard, r, 2); err != nil {
		return orientationUnspecified
	}

	// Skip the EXIF offset.
	var offset uint32
	if err := binary.Read(r, byteOrder, &offset); err != nil {
		return orientationUnspecified
	}
	if offset < 8 {
		return orientationUnspecified // Invalid offset value.
	}
	if _, err := io.CopyN(ioutil.Discard, r, int64(offset-8)); err != nil {
		return orientationUnspecified
	}

	// Read the number of tags.
	var numTags uint16
	if err := binary.Read(r, byteOrder, &numTags); err != nil {
		return orientationUnspecified
	}

	// Find the orientation tag.
	for i := 0; i < int(numTags); i++ {
		var tag uint16
		if err := binary.Read(r, byteOrder, &tag); err != nil {
			return orientationUnspecified
		}
		if tag != orientationTag {
			if _, err := io.CopyN(ioutil.Discard, r, 10); err != nil {
				return orientationUnspecified
			}
			continue
		}
		if _, err := io.CopyN(ioutil.Discard, r, 6); err != nil {
			return orientationUnspecified
		}
		var val uint16
		if err := binary.Read(r, byteOrder, &val); err != nil {
			return orientationUnspecified
		}
		if val < 1 || val > 8 {
			return orientationUnspecified // Invalid tag value.
		}
		return orientation(val)
	}
	return orientationUnspecified // Missing orientation tag.
}

// fixOrientation applies a transform to img corresponding to the given orientation flag.
func fixOrientation(img image.Image, o orientation) image.Image {
	switch o {
	case orientationNormal:
	case orientationFlipH:
		img = FlipH(img)
	case orientationFlipV:
		img = FlipV(img)
	case orientationRotate90:
		img = Rotate90(img)
	case orientationRotate180:
		img = Rotate180(img)
	case orientationRotate270:
		img = Rotate270(img)
	case orientationTranspose:
		img = Transpose(img)
	case orientationTransverse:
		img = Transverse(img)
	}
	return img
}
