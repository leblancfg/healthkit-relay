package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQRSizes(t *testing.T) {
	for _, c := range []struct{ n, version int }{{1, 1}, {14, 1}, {15, 2}, {84, 5}, {85, 6}, {213, 10}, {331, 13}, {332, 14}, {666, 20}} {
		q, err := EncodeQR(bytes.Repeat([]byte("a"), c.n))
		if err != nil {
			t.Fatalf("%d bytes: %v", c.n, err)
		}
		if q.Size != c.version*4+17 {
			t.Errorf("%d bytes: size %d, want version %d", c.n, q.Size, c.version)
		}
	}
	if _, err := EncodeQR(bytes.Repeat([]byte("a"), 667)); err == nil {
		t.Error("667 bytes: want an error")
	}
}

func TestQRFunctionPatterns(t *testing.T) {
	q, _ := EncodeQR([]byte("healthrelay://pair"))
	// Finder pattern rows at the top left: 1111111 0 / 1000001 0 / 1011101 0.
	for y, want := range []string{"11111110", "10000010", "10111010", "10111010", "10111010", "10000010", "11111110", "00000000"} {
		var got strings.Builder
		for x := 0; x < 8; x++ {
			if q.Dark(x, y) {
				got.WriteByte('1')
			} else {
				got.WriteByte('0')
			}
		}
		if got.String() != want {
			t.Errorf("row %d: %s, want %s", y, got.String(), want)
		}
	}
	if !q.Dark(8, q.Size-8) {
		t.Error("dark module missing")
	}
}

// TestQRDecodes decodes the codes with zbarimg when it is installed (nix shell nixpkgs#zbar).
func TestQRDecodes(t *testing.T) {
	zbar, err := exec.LookPath("zbarimg")
	if err != nil {
		t.Skip("zbarimg not installed")
	}
	inputs := []string{
		"healthrelay://pair?server=https%3A%2F%2Fhealth.example.com&code=ABCDEFGHJKMNPQRSTVWXYZ2345",
		"x",
		strings.Repeat("0123456789abcdef", 6),
		strings.Repeat("Health Relay ", 20),
		strings.Repeat("z", 666),
	}
	for i, in := range inputs {
		q, err := EncodeQR([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "qr.png")
		writeQRPNG(t, q, path)
		out, err := exec.Command(zbar, "--quiet", "--raw", path).Output()
		if err != nil {
			t.Fatalf("input %d (version %d): zbarimg: %v", i, (q.Size-17)/4, err)
		}
		if got := strings.TrimRight(string(out), "\n"); got != in {
			t.Errorf("input %d: decoded %q, want %q", i, got, in)
		}
	}
}

func writeQRPNG(t *testing.T, q *QR, path string) {
	t.Helper()
	const scale, quiet = 6, 4
	n := (q.Size + 2*quiet) * scale
	img := image.NewGray(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			mx, my := x/scale-quiet, y/scale-quiet
			c := color.Gray{255}
			if mx >= 0 && my >= 0 && mx < q.Size && my < q.Size && q.Dark(mx, my) {
				c = color.Gray{0}
			}
			img.SetGray(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
