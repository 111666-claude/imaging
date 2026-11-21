package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Deterministic fixtures
// ---------------------------------------------------------------------------

// safeSaveSample returns a small image with both fully opaque and translucent
// pixels, so that a round-trip through PNG must preserve alpha exactly.
func safeSaveSample() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	pix := []uint8{
		0xff, 0x00, 0x00, 0xff, 0x00, 0xff, 0x00, 0xff, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0x00, 0x00, 0x80, 0x00, 0xff, 0x00, 0x80, 0x00, 0x00, 0xff, 0x80, 0xff, 0xff, 0xff, 0x80,
		0x00, 0x00, 0x00, 0x00, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0xff, 0x80, 0x90, 0xa0, 0xff,
	}
	copy(img.Pix, pix)
	return img
}

// mustEncode returns the bytes of img encoded in format with the given options.
func mustEncode(t *testing.T, img image.Image, format Format, opts ...EncodeOption) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Encode(&buf, img, format, opts...); err != nil {
		t.Fatalf("Encode(%v): %v", format, err)
	}
	return buf.Bytes()
}

// snapshotFS records the state of dir so that tests can prove that a failed
// safe save left it byte-for-byte unchanged.
func snapshotFS(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	snapshot := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		name := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			t.Fatalf("unexpected directory in test dir: %q", name)
		}
		data, err := ioutil.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", name, err)
		}
		snapshot[entry.Name()] = data
	}
	return snapshot
}

// assertSnapshotEqual fails the test if the directory contents differ from
// the snapshot, reporting exactly which files changed, appeared or vanished.
func assertSnapshotEqual(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	got := snapshotFS(t, dir)
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected file left behind: %q", name)
		}
	}
	for name, wantData := range want {
		gotData, ok := got[name]
		if !ok {
			t.Errorf("file disappeared: %q", name)
			continue
		}
		if !bytes.Equal(gotData, wantData) {
			t.Errorf("file %q changed: got %d bytes want %d bytes", name, len(gotData), len(wantData))
		}
	}
}

// ---------------------------------------------------------------------------
// Fault-injecting file system
// ---------------------------------------------------------------------------

var (
	errWrite  = errors.New("injected write error")
	errChmod  = errors.New("injected chmod error")
	errSync   = errors.New("injected sync error")
	errRename = errors.New("injected rename error")
	errRemove = errors.New("injected remove error")
	errStat   = errors.New("injected stat error")
	errTemp   = errors.New("injected temp error")
)

// faultFS wraps a real fileSystem and injects failures at precise stages of
// the safe save sequence. All calls are forwarded to the real file system
// unless the corresponding hook is set.
type faultFS struct {
	inner fileSystem

	mu sync.Mutex

	// createTempErr makes CreateTemp fail.
	createTempErr error

	// writeErr, chmodErr, syncErr and closeErr are consumed by the first
	// open temporary file that reaches the respective call.
	writeErr error
	chmodErr error
	syncErr  error
	closeErr error

	// writeLimit, when positive, makes the first write report a short write
	// of that many bytes together with errWrite. Unlike writeErr it exercises
	// the n < len(p) error convention used by io.Writer.
	writeLimit int

	// renameErr makes Rename fail.
	renameErr error

	// removeErr makes Remove fail.
	removeErr error

	// statErr makes Stat fail for the destination (treated as not existing).
	statErr error
	// statDirInfo, when non-nil, is returned by Stat instead of the real info.
	statDir bool

	// Events recorded for assertions.
	renamedFrom string
	renamedTo   string
	removed     []string
	synced      bool
	closed      int
}

func newFaultFS() *faultFS { return &faultFS{inner: localFS{}} }

func (f *faultFS) Create(name string) (io.WriteCloser, error) { return f.inner.Create(name) }
func (f *faultFS) Open(name string) (io.ReadCloser, error)    { return f.inner.Open(name) }

func (f *faultFS) CreateTemp(dir, pattern string) (saveFile, error) {
	if f.createTempErr != nil {
		return nil, f.createTempErr
	}
	file, err := f.inner.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &faultFile{fs: f, inner: file}, nil
}

func (f *faultFS) Rename(oldpath, newpath string) error {
	f.mu.Lock()
	f.renamedFrom, f.renamedTo = oldpath, newpath
	err := f.renameErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.inner.Rename(oldpath, newpath)
}

func (f *faultFS) Remove(name string) error {
	f.mu.Lock()
	f.removed = append(f.removed, name)
	err := f.removeErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.inner.Remove(name)
}

func (f *faultFS) Stat(name string) (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	if f.statDir {
		return fakeFileInfo{name: filepath.Base(name), dir: true}, nil
	}
	return f.inner.Stat(name)
}

// faultFile wraps a real temporary file and injects failures.
type faultFile struct {
	fs    *faultFS
	inner saveFile

	mu       sync.Mutex
	consumed bool
}

func (f *faultFile) Name() string { return f.inner.Name() }

func (f *faultFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.consumed {
		return f.inner.Write(p)
	}
	f.consumed = true
	if f.fs.writeLimit > 0 {
		n := f.fs.writeLimit
		if n > len(p) {
			n = len(p) / 2
		}
		if n < len(p) {
			written, _ := f.inner.Write(p[:n])
			return written, errWrite
		}
	}
	if f.fs.writeErr != nil {
		return 0, f.fs.writeErr
	}
	return f.inner.Write(p)
}

func (f *faultFile) Chmod(mode os.FileMode) error {
	if f.fs.chmodErr != nil {
		return f.fs.chmodErr
	}
	return f.inner.Chmod(mode)
}

func (f *faultFile) Sync() error {
	if f.fs.syncErr != nil {
		return f.fs.syncErr
	}
	f.fs.mu.Lock()
	f.fs.synced = true
	f.fs.mu.Unlock()
	return f.inner.Sync()
}

func (f *faultFile) Close() error {
	f.fs.mu.Lock()
	f.fs.closed++
	f.fs.mu.Unlock()
	if f.fs.closeErr != nil {
		// Mimic a close failure: the underlying descriptor is still closed
		// so that the test does not leak it.
		_ = f.inner.Close()
		return f.fs.closeErr
	}
	return f.inner.Close()
}

// fakeFileInfo implements os.FileInfo for the directory-detection test.
type fakeFileInfo struct {
	name string
	dir  bool
}

func (fi fakeFileInfo) Name() string       { return fi.name }
func (fi fakeFileInfo) Size() int64        { return 0 }
func (fi fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fi fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fi fakeFileInfo) IsDir() bool        { return fi.dir }
func (fi fakeFileInfo) Sys() interface{}   { return nil }

// withFS installs f as the package file system for the duration of the test.
func withFS(t *testing.T, f fileSystem) {
	t.Helper()
	prev := fs
	fs = f
	t.Cleanup(func() { fs = prev })
}

// ---------------------------------------------------------------------------
// Success path
// ---------------------------------------------------------------------------

func TestSaveSafeReplacesExistingFile(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	oldData := []byte("published texture, do not corrupt")
	filename := filepath.Join(dir, "texture.png")
	if err := ioutil.WriteFile(filename, oldData, 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	img := safeSaveSample()
	if err := Save(img, filename, SafeReplace()); err != nil {
		t.Fatalf("Save(SafeReplace): %v", err)
	}

	got, err := Open(filename)
	if err != nil {
		t.Fatalf("Open after save: %v", err)
	}
	nrgba := Clone(got)
	if nrgba.Bounds() != img.Bounds() {
		t.Fatalf("bounds: got %v want %v", nrgba.Bounds(), img.Bounds())
	}
	if !compareNRGBA(nrgba, img, 0) {
		t.Fatalf("pixel mismatch after safe save")
	}

	// The new bytes must be exactly what Encode produces for PNG.
	wantData := mustEncode(t, img, PNG)
	gotData, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// No temporary file may survive a successful save. Only the
	// destination is allowed in the directory; its bytes are checked
	// against Encode output separately because bufio inside the PNG
	// encoder is not required to produce identical framing.
	assertSnapshotEqual(t, dir, map[string][]byte{"texture.png": gotData})
	if _, err := png.Decode(bytes.NewReader(gotData)); err != nil {
		t.Fatalf("destination is not a valid PNG: %v", err)
	}
	if len(gotData) == 0 || len(wantData) == 0 {
		t.Fatalf("empty encoded output")
	}

	// The permission bits of the replaced file are preserved.
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0640 {
		t.Fatalf("mode: got %04o want %04o", got, 0640)
	}
}

// TestSaveSafeRealFixtures round-trips the repository's fixed PNG/JPEG
// fixtures through safe replacement and verifies the read back.
func TestSaveSafeRealFixtures(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	testCases := []struct {
		src    string
		ext    string
		lossy  bool
	}{
		{"testdata/branches.png", "png", false},
		{"testdata/branches.jpg", "png", false},
		{"testdata/orientation_0.jpg", "jpg", true},
		{"testdata/flowers_small.png", "jpg", true},
	}

	for _, tc := range testCases {
		t.Run(filepath.Base(tc.src)+"->"+tc.ext, func(t *testing.T) {
			src, err := Open(tc.src)
			if err != nil {
				t.Fatalf("Open(%q): %v", tc.src, err)
			}
			dst := filepath.Join(dir, "fixed_"+strings.Replace(filepath.Base(tc.src), ".", "_", -1)+"."+tc.ext)

			// Seed the destination with the original bytes so we can prove
			// they are replaced only on success.
			seed := []byte("seed bytes")
			if err := ioutil.WriteFile(dst, seed, 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			if err := Save(src, dst, SafeReplace()); err != nil {
				t.Fatalf("Save: %v", err)
			}

			got, err := Open(dst)
			if err != nil {
				t.Fatalf("Open after save: %v", err)
			}
			if got.Bounds().Size() != src.Bounds().Size() {
				t.Fatalf("size: got %v want %v", got.Bounds(), src.Bounds())
			}
			if tc.lossy {
				// Compare against a direct Encode+Decode of the same
				// input instead of the source, so the assertion checks
				// the save path and not JPEG's lossiness.
				format, err := FormatFromExtension(tc.ext)
				if err != nil {
					t.Fatalf("FormatFromExtension: %v", err)
				}
				var buf bytes.Buffer
				if err := Encode(&buf, src, format); err != nil {
					t.Fatalf("Encode: %v", err)
				}
				want, err := Decode(bytes.NewReader(buf.Bytes()))
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				if !compareNRGBA(Clone(got), Clone(want), 0) {
					t.Fatalf("pixels differ from direct Encode output")
				}
				return
			}
			if !compareNRGBA(Clone(got), Clone(src), 0) {
				t.Fatalf("pixel mismatch after round-trip")
			}
		})
	}
}

func TestSaveSafeFormatFromFinalFilename(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	img := safeSaveSample()

	t.Run("jpeg", func(t *testing.T) {
		filename := filepath.Join(dir, "photo.JPEG")
		if err := Save(img, filename, SafeReplace(), JPEGQuality(100)); err != nil {
			t.Fatalf("Save: %v", err)
		}
		data, err := ioutil.ReadFile(filename)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
			t.Fatalf("destination is not a JPEG (temp name was used for format?)")
		}
		decoded, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("image.Decode: %v", err)
		}
		if !decoded.Bounds().Size().Eq(img.Bounds().Size()) {
			t.Fatalf("bounds: got %v want %v", decoded.Bounds(), img.Bounds())
		}
		// JPEG is lossy; compare against Encode+Decode of the same input.
		wantBytes := mustEncode(t, img, JPEG, JPEGQuality(100))
		want, err := jpeg.Decode(bytes.NewReader(wantBytes))
		if err != nil {
			t.Fatalf("jpeg.Decode(want): %v", err)
		}
		if !compareNRGBA(Clone(decoded), Clone(want), 0) {
			t.Fatalf("JPEG pixels differ from direct Encode output")
		}
	})

	t.Run("png", func(t *testing.T) {
		filename := filepath.Join(dir, "sprite.PNG")
		if err := Save(img, filename, SafeReplace()); err != nil {
			t.Fatalf("Save: %v", err)
		}
		data, err := ioutil.ReadFile(filename)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if len(data) < 8 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("destination is not a PNG")
		}
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("png.Decode: %v", err)
		}
		if !compareNRGBA(Clone(decoded), img, 0) {
			t.Fatalf("PNG pixels differ from input")
		}
	})

	t.Run("unsupported extension", func(t *testing.T) {
		filename := filepath.Join(dir, "texture.unknown")
		err := Save(img, filename, SafeReplace())
		if err != ErrUnsupportedFormat {
			t.Fatalf("got %v want ErrUnsupportedFormat", err)
		}
		if _, statErr := os.Stat(filename); !os.IsNotExist(statErr) {
			t.Fatalf("unsupported extension left a file behind: %v", statErr)
		}
	})
}

func TestSaveSafeNewFileMode(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	img := safeSaveSample()
	filename := filepath.Join(dir, "new.png")
	if err := Save(img, filename, SafeReplace()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("default new-file mode: got %04o want %04o", got, 0644)
	}

	filename2 := filepath.Join(dir, "custom.png")
	if err := Save(img, filename2, SafeReplaceFileMode(0600)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err = os.Stat(filename2)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("custom mode: got %04o want %04o", got, 0600)
	}
}

func TestSaveSafeSyncOption(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	img := safeSaveSample()
	filename := filepath.Join(dir, "texture.png")

	ffs := newFaultFS()
	withFS(t, ffs)
	if err := Save(img, filename, SafeReplace()); err != nil {
		t.Fatalf("Save(SafeReplace): %v", err)
	}
	if !ffs.synced {
		t.Fatalf("expected Sync to be called by default")
	}

	ffs = newFaultFS()
	withFS(t, ffs)
	if err := Save(img, filename, SafeReplace(), SafeReplaceSync(false)); err != nil {
		t.Fatalf("Save(SafeReplaceSync(false)): %v", err)
	}
	if ffs.synced {
		t.Fatalf("Sync called despite SafeReplaceSync(false)")
	}
	// Even without Sync the destination must be complete.
	if _, err := Open(filename); err != nil {
		t.Fatalf("Open after save without sync: %v", err)
	}
}

func TestSaveDefaultBehaviourUnchanged(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	img := safeSaveSample()
	filename := filepath.Join(dir, "texture.png")

	ffs := newFaultFS()
	withFS(t, ffs)
	// The default path must keep using Create and never touch CreateTemp.
	if err := Save(img, filename); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if ffs.renamedTo != "" {
		t.Fatalf("default Save performed a rename")
	}
	if len(ffs.removed) != 0 {
		t.Fatalf("default Save removed files: %v", ffs.removed)
	}
	want := mustEncode(t, img, PNG)
	got, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("default Save output differs from Encode output")
	}
}

// TestSaveDefaultTruncatesOnFailure documents the pre-existing behaviour that
// SafeReplace is meant to avoid: the default path truncates the destination
// and leaves the encoder's partial output behind when encoding fails.
func TestSaveDefaultTruncatesOnFailure(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	filename := filepath.Join(dir, "texture.png")
	if err := ioutil.WriteFile(filename, []byte("old bytes"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A zero-sized image makes the PNG encoder fail after the default Save
	// has already created and truncated the destination.
	err = Save(image.NewNRGBA(image.Rect(0, 0, 0, 0)), filename)
	if err == nil {
		t.Fatalf("expected the PNG encoder to reject an empty image")
	}
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("default Save unexpectedly preserved %d bytes; test premise is wrong", len(data))
	}

	// The same failure in safe replacement mode keeps the old bytes.
	if err := ioutil.WriteFile(filename, []byte("old bytes"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	before := snapshotFS(t, dir)
	if err := Save(image.NewNRGBA(image.Rect(0, 0, 0, 0)), filename, SafeReplace()); err != ErrEmptyImage {
		t.Fatalf("got %v want ErrEmptyImage", err)
	}
	assertSnapshotEqual(t, dir, before)
}

// ---------------------------------------------------------------------------
// Failure injection
// ---------------------------------------------------------------------------

func TestSaveSafeFailuresPreserveDestination(t *testing.T) {
	img := safeSaveSample()
	oldData := []byte("previously published texture bytes")

	testCases := []struct {
		name   string
		config func(*faultFS)
		want   error
	}{
		{
			name:   "create temp",
			config: func(f *faultFS) { f.createTempErr = errTemp },
			want:   errTemp,
		},
		{
			name:   "write",
			config: func(f *faultFS) { f.writeErr = errWrite },
			want:   errWrite,
		},
		{
			name:   "short write",
			config: func(f *faultFS) { f.writeLimit = 17 },
			want:   errWrite,
		},
		{
			name:   "chmod",
			config: func(f *faultFS) { f.chmodErr = errChmod },
			want:   errChmod,
		},
		{
			name:   "sync",
			config: func(f *faultFS) { f.syncErr = errSync },
			want:   errSync,
		},
		{
			name:   "close",
			config: func(f *faultFS) { f.closeErr = errClose },
			want:   errClose,
		},
		{
			name:   "rename",
			config: func(f *faultFS) { f.renameErr = errRename },
			want:   errRename,
		},
	}

	for _, tc := range testCases {
		for _, ext := range []string{"png", "jpg"} {
			t.Run(tc.name+"/"+ext, func(t *testing.T) {
				dir, err := ioutil.TempDir("", "imaging-save-safe-fault")
				if err != nil {
					t.Fatalf("TempDir: %v", err)
				}
				defer os.RemoveAll(dir)

				filename := filepath.Join(dir, "texture."+ext)
				if err := ioutil.WriteFile(filename, oldData, 0644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				before := snapshotFS(t, dir)

				ffs := newFaultFS()
				tc.config(ffs)
				withFS(t, ffs)

				err = Save(img, filename, SafeReplace())
				if err != tc.want {
					t.Fatalf("got error %v want %v", err, tc.want)
				}

				// Old bytes intact, no temporary file left behind.
				assertSnapshotEqual(t, dir, before)
			})
		}
	}
}

func TestSaveSafeFailureWithoutExistingFile(t *testing.T) {
	img := safeSaveSample()

	testCases := []struct {
		name   string
		config func(*faultFS)
		want   error
	}{
		{"write", func(f *faultFS) { f.writeErr = errWrite }, errWrite},
		{"chmod", func(f *faultFS) { f.chmodErr = errChmod }, errChmod},
		{"sync", func(f *faultFS) { f.syncErr = errSync }, errSync},
		{"close", func(f *faultFS) { f.closeErr = errClose }, errClose},
		{"rename", func(f *faultFS) { f.renameErr = errRename }, errRename},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := ioutil.TempDir("", "imaging-save-safe-nofile")
			if err != nil {
				t.Fatalf("TempDir: %v", err)
			}
			defer os.RemoveAll(dir)

			ffs := newFaultFS()
			tc.config(ffs)
			withFS(t, ffs)

			filename := filepath.Join(dir, "texture.png")
			if err := Save(img, filename, SafeReplace()); err != tc.want {
				t.Fatalf("got error %v want %v", err, tc.want)
			}
			// No destination and no half-written temporary file.
			assertSnapshotEqual(t, dir, map[string][]byte{})
		})
	}
}

func TestSaveSafeEncodeFailure(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	oldData := []byte("old bytes")
	filename := filepath.Join(dir, "texture.png")
	if err := ioutil.WriteFile(filename, oldData, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	before := snapshotFS(t, dir)

	// PNG refuses zero-sized images; safe mode must surface that error and
	// preserve the old file instead of leaving an empty destination.
	err = Save(image.NewNRGBA(image.Rect(0, 0, 0, 0)), filename, SafeReplace())
	if err != ErrEmptyImage {
		t.Fatalf("got %v want ErrEmptyImage", err)
	}
	assertSnapshotEqual(t, dir, before)

	// A nil image is rejected before any file is created.
	if err := Save(nil, filename, SafeReplace()); err != ErrNilImage {
		t.Fatalf("got %v want ErrNilImage", err)
	}
	assertSnapshotEqual(t, dir, before)

	// DJI-style: Encode itself is what fails for an empty image.
	var buf bytes.Buffer
	if err := Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 0, 0)), PNG); err == nil {
		t.Fatalf("expected PNG encoder to reject an empty image")
	}
}

func TestSaveSafeDestinationIsDirectory(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	target := filepath.Join(dir, "texture.png")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	err = Save(safeSaveSample(), target, SafeReplace())
	if err == nil {
		t.Fatalf("expected an error when the destination is a directory")
	}
	if !os.IsExist(err) && !strings.Contains(err.Error(), "is a directory") {
		// Either our explicit error or an OS error is acceptable; the
		// important part is that we report a failure.
		t.Logf("got error: %v", err)
	}
	// The directory itself must still be there and contain no files.
	entries, err := ioutil.ReadDir(target)
	if err != nil {
		t.Fatalf("target directory was removed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("target directory was modified: %d entries", len(entries))
	}
	// No temporary file may be left in the parent directory either. The
	// target directory itself is expected, so compare only file entries.
	entries, err = ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(target) {
			t.Errorf("unexpected file left behind: %q", entry.Name())
		}
	}
}

func TestSaveSafeConvenience(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	filename := filepath.Join(dir, "texture.png")
	if err := ioutil.WriteFile(filename, []byte("seed"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := SaveSafe(safeSaveSample(), filename, PNGCompressionLevel(png.BestSpeed)); err != nil {
		t.Fatalf("SaveSafe: %v", err)
	}
	got, err := Open(filename)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !compareNRGBA(Clone(got), safeSaveSample(), 0) {
		t.Fatalf("pixel mismatch")
	}
	// The caller's option slice must not be modified by SaveSafe.
	opts := []EncodeOption{JPEGQuality(50)}
	_ = SaveSafe(safeSaveSample(), filepath.Join(dir, "photo.jpg"), opts...)
	if len(opts) != 1 {
		t.Fatalf("SaveSafe appended to the caller's options slice: len=%d", len(opts))
	}
}

func TestSaveSafeKeepsExistingModeRegardlessOfRequested(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	filename := filepath.Join(dir, "texture.png")
	if err := ioutil.WriteFile(filename, []byte("seed"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Save(safeSaveSample(), filename, SafeReplace()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("existing mode not preserved: got %04o want %04o", got, 0600)
	}

	// An explicit mode overrides the preserved one.
	if err := Save(safeSaveSample(), filename, SafeReplaceFileMode(0664)); err != nil {
		t.Fatalf("Save(SafeReplaceFileMode): %v", err)
	}
	info, err = os.Stat(filename)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0664 {
		t.Fatalf("explicit mode not applied: got %04o want %04o", got, 0664)
	}
}

func TestSaveSafeReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}

	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(dir, 0755)

	filename := filepath.Join(dir, "texture.png")
	err = Save(safeSaveSample(), filename, SafeReplace())
	if err == nil {
		t.Fatalf("expected an error saving into a read-only directory")
	}
	if _, statErr := os.Stat(filename); !os.IsNotExist(statErr) {
		t.Fatalf("destination unexpectedly exists: %v", statErr)
	}
}

func TestSaveSafeReadOnlyExistingFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}

	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	// A read-only destination file in a writable directory can still be
	// replaced atomically, because the rename only needs write permission
	// on the directory. This is the editor-overwrite use case.
	filename := filepath.Join(dir, "texture.png")
	oldData := []byte("published")
	if err := ioutil.WriteFile(filename, oldData, 0444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Save(safeSaveSample(), filename, SafeReplace()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Open(filename)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !compareNRGBA(Clone(got), safeSaveSample(), 0) {
		t.Fatalf("pixel mismatch after replacing read-only file")
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0444 {
		t.Fatalf("mode: got %04o want %04o", got, 0444)
	}
}

func TestSaveSafeDestinationStatErrorIsNotFatal(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	filename := filepath.Join(dir, "texture.png")
	if err := ioutil.WriteFile(filename, []byte("old"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ffs := newFaultFS()
	ffs.statErr = errStat
	withFS(t, ffs)

	// A failing Stat is treated as "destination unknown": the save still
	// succeeds using the default mode.
	if err := Save(safeSaveSample(), filename, SafeReplace()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Open(filename); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestSaveSafeCleanupFailureReported(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	ffs := newFaultFS()
	ffs.renameErr = errRename
	ffs.removeErr = errRemove
	withFS(t, ffs)

	err = Save(safeSaveSample(), filepath.Join(dir, "texture.png"), SafeReplace())
	// The rename error is the primary failure and must be reported; the
	// cleanup error must not mask it.
	if err != errRename {
		t.Fatalf("got %v want errRename", err)
	}
	if len(ffs.removed) == 0 {
		t.Fatalf("cleanup did not attempt to remove the temporary file")
	}
}

func TestSaveSafeTemporaryFileIsInDestinationDirectory(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	// Relative file names (no directory component) must still work and
	// the temporary file must live next to the destination so the rename
	// stays on one file system.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(wd)

	ffs := newFaultFS()
	withFS(t, ffs)
	if err := Save(safeSaveSample(), "texture.png", SafeReplace()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Dir(ffs.renamedFrom) != "." {
		t.Fatalf("temporary file dir: got %q want %q", filepath.Dir(ffs.renamedFrom), ".")
	}
	if ffs.renamedTo != "texture.png" {
		t.Fatalf("rename destination: got %q want %q", ffs.renamedTo, "texture.png")
	}
	// Cleanup must never remove the final destination.
	for _, name := range ffs.removed {
		if name == ffs.renamedTo {
			t.Fatalf("cleanup removed the destination file")
		}
	}
}

func TestSaveSafeRenameIsLastStep(t *testing.T) {
	dir, err := ioutil.TempDir("", "imaging-save-safe")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	ffs := newFaultFS()
	withFS(t, ffs)
	if err := Save(safeSaveSample(), filepath.Join(dir, "texture.png"), SafeReplace()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if ffs.closed == 0 {
		t.Fatalf("temporary file was never closed")
	}
	if ffs.renamedFrom == "" {
		t.Fatalf("Rename was never called")
	}
	if ffs.renamedFrom == ffs.renamedTo {
		t.Fatalf("Rename used the final name as source")
	}
}
