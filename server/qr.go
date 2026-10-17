package main

import (
	"errors"
	"fmt"
	"strings"
)

// A QR code encoder (ISO/IEC 18004), just enough for pairing links: byte mode, error
// correction level M, versions 1 to 20 (up to 666 bytes), automatic mask choice. It follows
// the structure of Project Nayuki's reference implementation; the standard library has none.

// QR is a square of modules, true = dark.
type QR struct {
	Size    int
	modules [][]bool
}

// Dark reports whether the module at column x, row y is dark.
func (q *QR) Dark(x, y int) bool { return q.modules[y][x] }

// Per version (index 1 to 20), for level M.
var (
	qrECCPerBlock = [...]int{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26}
	qrBlocks      = [...]int{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16}
)

const qrMaxVersion = 20

// EncodeQR returns the smallest QR code holding data.
func EncodeQR(data []byte) (*QR, error) {
	for v := 1; v <= qrMaxVersion; v++ {
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		capacity := qrDataCodewords(v) * 8
		if 4+countBits+8*len(data) > capacity {
			continue
		}
		var bits qrBits
		bits.append(0b0100, 4) // byte mode
		bits.append(len(data), countBits)
		for _, b := range data {
			bits.append(int(b), 8)
		}
		bits.append(0, min(4, capacity-len(bits)))
		bits.append(0, (8-len(bits)%8)%8)
		for pad := 0xEC; len(bits) < capacity; pad ^= 0xEC ^ 0x11 {
			bits.append(pad, 8)
		}
		return newQR(v, bits.bytes()), nil
	}
	return nil, errors.New("qr: data too long")
}

// qrRawModules is the number of modules available for data and error correction.
func qrRawModules(v int) int {
	n := (16*v+128)*v + 64
	if v >= 2 {
		align := v/7 + 2
		n -= (25*align-10)*align - 55
		if v >= 7 {
			n -= 36
		}
	}
	return n
}

func qrDataCodewords(v int) int {
	return qrRawModules(v)/8 - qrECCPerBlock[v]*qrBlocks[v]
}

type qrBits []bool

func (b *qrBits) append(value, n int) {
	for i := n - 1; i >= 0; i-- {
		*b = append(*b, value>>i&1 == 1)
	}
}

func (b qrBits) bytes() []byte {
	out := make([]byte, len(b)/8)
	for i, bit := range b {
		if bit {
			out[i/8] |= 0x80 >> (i % 8)
		}
	}
	return out
}

type qrBuilder struct {
	QR
	version  int
	function [][]bool
}

func newQR(version int, data []byte) *QR {
	size := version*4 + 17
	q := &qrBuilder{QR: QR{Size: size}, version: version}
	q.modules = make([][]bool, size)
	q.function = make([][]bool, size)
	for i := range q.modules {
		q.modules[i] = make([]bool, size)
		q.function[i] = make([]bool, size)
	}
	q.drawFunctionPatterns()
	q.drawCodewords(q.withECC(data))

	best, bestPenalty := 0, -1
	for mask := 0; mask < 8; mask++ {
		q.applyMask(mask)
		q.drawFormat(mask)
		if p := q.penalty(); bestPenalty < 0 || p < bestPenalty {
			best, bestPenalty = mask, p
		}
		q.applyMask(mask) // XOR again: undo
	}
	q.applyMask(best)
	q.drawFormat(best)
	return &q.QR
}

func (q *qrBuilder) set(x, y int, dark bool) {
	q.modules[y][x] = dark
	q.function[y][x] = true
}

func (q *qrBuilder) drawFunctionPatterns() {
	for i := 0; i < q.Size; i++ {
		q.set(6, i, i%2 == 0)
		q.set(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {q.Size - 4, 3}, {3, q.Size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < q.Size && y >= 0 && y < q.Size {
					d := max(abs(dx), abs(dy))
					q.set(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	pos := q.alignmentPositions()
	n := len(pos)
	for i := range pos {
		for j := range pos {
			if i == 0 && j == 0 || i == 0 && j == n-1 || i == n-1 && j == 0 {
				continue // finder corners
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.set(pos[i]+dx, pos[j]+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	q.drawFormat(0) // reserves the format areas; redrawn with the chosen mask
	if q.version >= 7 {
		rem := q.version
		for i := 0; i < 12; i++ {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		bits := q.version<<12 | rem
		for i := 0; i < 18; i++ {
			dark := bits>>i&1 == 1
			a, b := q.Size-11+i%3, i/3
			q.set(a, b, dark)
			q.set(b, a, dark)
		}
	}
}

func (q *qrBuilder) alignmentPositions() []int {
	if q.version == 1 {
		return nil
	}
	n := q.version/7 + 2
	step := (q.version*8 + n*3 + 5) / (n*4 - 4) * 2
	pos := make([]int, n)
	pos[0] = 6
	for i, p := n-1, q.Size-7; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

// drawFormat writes level M (format bits 00) and the mask, BCH-protected, twice.
func (q *qrBuilder) drawFormat(mask int) {
	data := 0<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := 0; i <= 5; i++ {
		q.set(8, i, bit(i))
	}
	q.set(8, 7, bit(6))
	q.set(8, 8, bit(7))
	q.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		q.set(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		q.set(q.Size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		q.set(8, q.Size-15+i, bit(i))
	}
	q.set(8, q.Size-8, true) // the dark module
}

// withECC splits data into blocks, appends each block's Reed-Solomon codewords and
// interleaves them.
func (q *qrBuilder) withECC(data []byte) []byte {
	blocks, ecc := qrBlocks[q.version], qrECCPerBlock[q.version]
	raw := qrRawModules(q.version) / 8
	short := blocks - raw%blocks
	shortLen := raw/blocks - ecc
	divisor := rsDivisor(ecc)
	var datas, eccs [][]byte
	for i, k := 0, 0; i < blocks; i++ {
		n := shortLen
		if i >= short {
			n++
		}
		d := data[k : k+n]
		k += n
		datas = append(datas, d)
		eccs = append(eccs, rsRemainder(d, divisor))
	}
	var out []byte
	for i := 0; i <= shortLen; i++ {
		for _, d := range datas {
			if i < len(d) {
				out = append(out, d[i])
			}
		}
	}
	for i := 0; i < ecc; i++ {
		for _, e := range eccs {
			out = append(out, e[i])
		}
	}
	return out
}

func (q *qrBuilder) drawCodewords(data []byte) {
	i := 0
	for right := q.Size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < q.Size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = q.Size - 1 - vert // upward
				}
				if !q.function[y][x] && i < len(data)*8 {
					q.modules[y][x] = data[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func (q *qrBuilder) applyMask(mask int) {
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			var invert bool
			switch mask {
			case 0:
				invert = (x+y)%2 == 0
			case 1:
				invert = y%2 == 0
			case 2:
				invert = x%3 == 0
			case 3:
				invert = (x+y)%3 == 0
			case 4:
				invert = (x/3+y/2)%2 == 0
			case 5:
				invert = x*y%2+x*y%3 == 0
			case 6:
				invert = (x*y%2+x*y%3)%2 == 0
			case 7:
				invert = ((x+y)%2+x*y%3)%2 == 0
			}
			if invert && !q.function[y][x] {
				q.modules[y][x] = !q.modules[y][x]
			}
		}
	}
}

// penalty scores how hard the symbol is to scan (runs, 2x2 blocks, finder look-alikes,
// dark/light balance); the mask with the lowest score is used.
func (q *qrBuilder) penalty() int {
	p := 0
	line := func(get func(i int) bool) {
		run := 1
		for i := 1; i <= q.Size; i++ {
			if i < q.Size && get(i) == get(i-1) {
				run++
				continue
			}
			if run >= 5 {
				p += run - 2
			}
			run = 1
		}
		// 1:1:3:1:1 finder-like patterns with four light modules on one side.
		for i := 0; i+11 <= q.Size; i++ {
			a := true
			b := true
			for k, v := range []bool{true, false, true, true, true, false, true, false, false, false, false} {
				if get(i+k) != v {
					a = false
				}
				if get(i+10-k) != v {
					b = false
				}
			}
			if a {
				p += 40
			}
			if b {
				p += 40
			}
		}
	}
	for y := 0; y < q.Size; y++ {
		line(func(i int) bool { return q.modules[y][i] })
	}
	for x := 0; x < q.Size; x++ {
		line(func(i int) bool { return q.modules[i][x] })
	}
	dark := 0
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			c := q.modules[y][x]
			if c {
				dark++
			}
			if x+1 < q.Size && y+1 < q.Size && c == q.modules[y][x+1] && c == q.modules[y+1][x] && c == q.modules[y+1][x+1] {
				p += 3
			}
		}
	}
	total := q.Size * q.Size
	k := (abs(dark*20-total*10)+total-1)/total - 1
	return p + k*10
}

func rsDivisor(degree int) []byte {
	out := make([]byte, degree)
	out[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range out {
			out[j] = gfMul(out[j], root)
			if j+1 < len(out) {
				out[j] ^= out[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return out
}

func rsRemainder(data, divisor []byte) []byte {
	out := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ out[0]
		copy(out, out[1:])
		out[len(out)-1] = 0
		for i, d := range divisor {
			out[i] ^= gfMul(d, factor)
		}
	}
	return out
}

// gfMul multiplies in GF(2^8) modulo x^8 + x^4 + x^3 + x^2 + 1.
func gfMul(x, y byte) byte {
	z := 0
	for i := 7; i >= 0; i-- {
		z = z<<1 ^ (z>>7)*0x11D
		z ^= int(y>>i&1) * int(x)
	}
	return byte(z)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// SVG renders the code with a quiet zone of 4 modules, scaled by the viewer.
func (q *QR) SVG() string {
	var b strings.Builder
	n := q.Size + 8
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			if q.modules[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// Terminal renders the code with half blocks, two rows per line, in black on white whatever
// the terminal's colors, with a quiet zone of 4 modules.
func (q *QR) Terminal() string {
	const quiet = 4
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < q.Size && y < q.Size && q.modules[y][x]
	}
	var b strings.Builder
	n := q.Size + 2*quiet
	for y := 0; y < n; y += 2 {
		b.WriteString("\x1b[30;47m")
		for x := 0; x < n; x++ {
			switch top, bottom := dark(x, y), dark(x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}
