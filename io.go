package imaging

import (
	"encoding/binary"
	"errors"
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

	// The methods below are used by Save in safe replacement mode.
	CreateTemp(dir, pattern string) (saveFile, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Stat(name string) (os.FileInfo, error)
}

// saveFile is a temporary file used by Save in safe replacement mode.
// It is an interface (rather than *os.File) so that tests can inject
// failures at the write, chmod, sync, close and rename stages.
type saveFile interface {
	io.Writer
	Name() string
	Chmod(mode os.FileMode) error
	Sync() error
	Close() error
}

type localFS struct{}

func (localFS) Create(name string) (io.WriteCloser, error) { return os.Create(name) }
func (localFS) Open(name string) (io.ReadCloser, error)    { return os.Open(name) }

func (localFS) CreateTemp(dir, pattern string) (saveFile, error) {
	return ioutil.TempFile(dir, pattern)
}

func (localFS) Rename(oldpath, newpath string) error  { return os.Rename(oldpath, newpath) }
func (localFS) Remove(name string) error              { return os.Remove(name) }
func (localFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

var fs fileSystem = localFS{}

type decodeConfig struct {
	autoOrientation bool
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

// Decode reads an image from r.
func Decode(r io.Reader, opts ...DecodeOption) (image.Image, error) {
	cfg := defaultDecodeConfig
	for _, option := range opts {
		option(&cfg)
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

// Open loads an image from file.
//
// Examples:
//
//	// Load an image from file.
//	img, err := imaging.Open("test.jpg")
//
//	// Load an image and transform it depending on the EXIF orientation tag (if present).
//	img, err := imaging.Open("test.jpg", imaging.AutoOrientation(true))
//
func Open(filename string, opts ...DecodeOption) (image.Image, error) {
	file, err := fs.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
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

// ErrNilImage is returned by Encode and Save when the image is nil.
var ErrNilImage = errors.New("imaging: nil image")

// ErrEmptyImage is returned by Save in safe replacement mode when the image
// has empty bounds (zero width or height), since replacing a published file
// with an image no encoder can meaningfully represent is never intended.
var ErrEmptyImage = errors.New("imaging: empty image")

// errDestinationIsDir is returned by Save in safe replacement mode when the
// destination path refers to an existing directory.
var errDestinationIsDir = errors.New("destination is a directory")

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

	// Options used by the safe replacement mode of Save (see SafeReplace).
	safeReplace bool
	safeSync    bool
	safeMode    os.FileMode
	safeModeSet bool
}

var defaultEncodeConfig = encodeConfig{
	jpegQuality:         95,
	gifNumColors:        256,
	gifQuantizer:        nil,
	gifDrawer:           nil,
	pngCompressionLevel: png.DefaultCompression,
	safeSync:            true,
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

// SafeReplace returns an EncodeOption that switches Save to the safe
// replacement mode. In this mode Save encodes the image to a temporary file
// created in the same directory as filename and atomically renames it over
// filename only after the encoding, the writes, the optional sync and the
// close have all succeeded. If any of these steps fails, the destination file
// is left untouched (its contents are preserved) and the temporary file is
// removed. If the destination did not exist, it is not created at all.
//
// The encoding format is always determined from the extension of the final
// filename, never from the temporary file name. If the destination already
// exists, its permission bits are preserved; otherwise the new file gets mode
// 0644. Both can be overridden with SafeReplaceFileMode.
//
// The default Save behavior (writing directly into the destination) is
// unchanged; pass SafeReplace to opt in to the safe mode.
//
// On platforms where renaming over an existing file is not possible while
// that file is open (notably Windows), Save returns the rename error, keeps
// the old file bytes and removes the temporary file. Save never falls back to
// deleting the destination before renaming, because that would break the
// atomic replacement guarantee.
//
// SafeReplace is ignored by Encode.
func SafeReplace() EncodeOption {
	return func(c *encodeConfig) {
		c.safeReplace = true
	}
}

// SafeReplaceSync returns an EncodeOption that controls whether Save flushes
// the temporary file to stable storage (File.Sync) before replacing the
// destination in safe replacement mode. Enabling it reduces the chance that a
// system crash leaves a renamed but incompletely written file. Default is true.
//
// The option is ignored unless SafeReplace is also specified.
func SafeReplaceSync(enabled bool) EncodeOption {
	return func(c *encodeConfig) {
		c.safeSync = enabled
	}
}

// SafeReplaceFileMode returns an EncodeOption that sets the permission bits of
// the file created by Save in safe replacement mode. When it is not specified,
// an existing destination keeps its permission bits and a new destination is
// created with mode 0644. Unlike the mode passed to os.Create, this mode is
// applied with File.Chmod, so it is not filtered by the process umask.
//
// The option enables the safe replacement mode and is ignored by Encode.
func SafeReplaceFileMode(mode os.FileMode) EncodeOption {
	return func(c *encodeConfig) {
		c.safeReplace = true
		c.safeMode = mode
		c.safeModeSet = true
	}
}

// Encode writes the image img to w in the specified format (JPEG, PNG, GIF, TIFF or BMP).
func Encode(w io.Writer, img image.Image, format Format, opts ...EncodeOption) error {
	if img == nil {
		return ErrNilImage
	}

	cfg := defaultEncodeConfig
	for _, option := range opts {
		option(&cfg)
	}
	return encode(w, img, format, cfg)
}

// encode writes img to w using an already resolved configuration. The
// encode options are consumed by the caller so that they are applied exactly
// once per call.
func encode(w io.Writer, img image.Image, format Format, cfg encodeConfig) error {
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
// By default Save creates (or truncates) the destination file and writes the
// encoded image directly to it. If the encoding fails, the destination may be
// left truncated or partially written. Pass the SafeReplace option to write to
// a temporary file in the destination directory and atomically rename it over
// the destination only after a successful encode, so that a failure never
// corrupts an existing file:
//
//	// Save the image as PNG, atomically replacing an existing file.
//	err := imaging.Save(img, "out.png", imaging.SafeReplace())
//
// Examples:
//
//	// Save the image as PNG.
//	err := imaging.Save(img, "out.png")
//
//	// Save the image as JPEG with optional quality parameter set to 80.
//	err := imaging.Save(img, "out.jpg", imaging.JPEGQuality(80))
//
func Save(img image.Image, filename string, opts ...EncodeOption) (err error) {
	f, err := FormatFromFilename(filename)
	if err != nil {
		return err
	}

	cfg := defaultEncodeConfig
	for _, option := range opts {
		option(&cfg)
	}
	if img == nil {
		return ErrNilImage
	}
	if cfg.safeReplace {
		return saveSafe(img, f, filename, cfg)
	}

	file, err := fs.Create(filename)
	if err != nil {
		return err
	}
	err = encode(file, img, f, cfg)
	errc := file.Close()
	if err == nil {
		err = errc
	}
	return err
}

// SaveSafe saves the image to file using the safe replacement mode.
// It is equivalent to calling Save with the SafeReplace option:
//
//	imaging.Save(img, filename, imaging.SafeReplace())
//
// The destination file is replaced atomically and is left unchanged if the
// encoding or any of the write, sync, close or rename steps fails.
func SaveSafe(img image.Image, filename string, opts ...EncodeOption) error {
	all := make([]EncodeOption, 0, len(opts)+1)
	all = append(all, opts...)
	all = append(all, SafeReplace())
	return Save(img, filename, all...)
}

// saveSafe implements the safe replacement mode of Save.
func saveSafe(img image.Image, format Format, filename string, cfg encodeConfig) (err error) {
	if img.Bounds().Empty() {
		return ErrEmptyImage
	}

	dir, base := filepath.Split(filename)
	if dir == "" {
		dir = "."
	}

	// Reject the destination early if it is a directory: renaming over it
	// cannot succeed, and doing so before encoding avoids the cost of
	// producing an image that would have to be thrown away.
	if info, statErr := fs.Stat(filename); statErr == nil && info.IsDir() {
		return &os.PathError{Op: "save", Path: filename, Err: errDestinationIsDir}
	}

	mode := os.FileMode(0644)
	if cfg.safeModeSet {
		mode = cfg.safeMode
	} else if info, statErr := fs.Stat(filename); statErr == nil {
		// Preserve the permission bits of the file being replaced.
		mode = info.Mode().Perm()
	}

	// The temporary file is created next to the destination so that the
	// final rename stays within a single filesystem and can be atomic.
	file, err := fs.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpname := file.Name()

	// cleanup removes the temporary file. It is called after every failure
	// path below, including panics caused by a misbehaving encoder.
	defer func() {
		if file != nil {
			// Close may already have been called; ignore its error since
			// the file is being discarded.
			_ = file.Close()
		}
		if tmpname == "" {
			return
		}
		// If the temporary file was never successfully written, the
		// destination may be left untouched. A stray temporary file,
		// however, would be visible to the editor, so its removal
		// failure is reported whenever there is no more important error.
		if cleanupErr := fs.Remove(tmpname); cleanupErr != nil && err == nil {
			err = cleanupErr
		}
	}()

	// The image is encoded directly into the temporary file. If the
	// encoder or any of its writes fails, the partial temporary file is
	// removed by the deferred cleanup and the destination is untouched.
	if encErr := encode(file, img, format, cfg); encErr != nil {
		return encErr
	}
	if chmodErr := file.Chmod(mode); chmodErr != nil {
		return chmodErr
	}
	if cfg.safeSync {
		if syncErr := file.Sync(); syncErr != nil {
			return syncErr
		}
	}
	if closeErr := file.Close(); closeErr != nil {
		return closeErr
	}
	file = nil

	// Only a fully written, synced and closed file is moved over the
	// destination. A failed rename leaves the old destination bytes intact.
	if err = fs.Rename(tmpname, filename); err != nil {
		return err
	}

	// The rename consumed the temporary name; there is nothing to remove.
	tmpname = ""
	return nil
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
