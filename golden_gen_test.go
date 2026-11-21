package imaging

import (
	"crypto/md5"
	"fmt"
	"testing"
)

// TestPrintGoldenHashes is a development helper used to (re)generate the
// golden checksums embedded in scanner_regression_test.go. It is skipped
// in normal test runs.
func TestPrintGoldenHashes(t *testing.T) {
	if !testing.Verbose() {
		t.Skip("run with -v -run TestPrintGoldenHashes to regenerate golden hashes")
	}
	for _, f := range goldenFormats() {
		for _, op := range goldenOps() {
			out := op.fn(f.img())
			sum := md5.Sum(out.Pix)
			fmt.Printf("\t{%q, %q, %q},\t// %dx%d\n", f.name, op.name, fmt.Sprintf("%x", sum), out.Bounds().Dx(), out.Bounds().Dy())
		}
	}
}
