package nrsc5

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// convEncode is a software tail-biting encoder matching the decoder's
// convention (conv_dec.c): the register holds [u_i at bit k-1 (MSB),
// u_{i-1} at bit k-2, ..., u_{i-(k-1)} at bit 0]; outputs are
// parity(reg & gen) mapped to NRZ (+1 = bit 1).
func convEncode(bits []uint8, k int, gens [3]uint) []int8 {
	n := len(bits)
	// Tail-biting init: register holds u_{n-1} (newest) .. u_{n-k+1} (oldest).
	var s uint
	for j := 1; j <= k-1; j++ {
		s |= uint(bits[n-j]) << (k - 1 - j)
	}
	out := make([]int8, 0, n*3)
	for i := 0; i < n; i++ {
		reg := s | uint(bits[i])<<(k-1)
		for g := 0; g < 3; g++ {
			out = append(out, int8(parity(reg&gens[g])*2-1))
		}
		s = (s >> 1) | uint(bits[i])<<(k-2)
	}
	return out
}

// refDecode runs the original C decoder (built from conv_dec.c) on soft
// inputs and returns its decoded bits, or skips if the binary is absent.
func refDecode(t *testing.T, which string, length int, in []int8) []uint8 {
	t.Helper()
	const refBin = "/tmp/opencode/refbuild/ref_conv"
	if _, err := os.Stat(refBin); err != nil {
		t.Skip("reference decoder not built")
	}
	raw := make([]byte, len(in))
	for i, v := range in {
		raw[i] = byte(v)
	}
	cmd := exec.Command(refBin, "x", fmt.Sprint(length), which)
	cmd.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("reference decoder failed: %v: %s", err, stderr.String())
	}
	if len(stdout) != length {
		t.Fatalf("reference decoder output %d bytes, want %d", len(stdout), length)
	}
	return stdout
}

var convCases = []struct {
	name   string
	k      int
	gens   [3]uint
	decode func(in []int8, out []uint8) (int, error)
	length int
}{
	{"p1", 7, [3]uint{0133, 0171, 0165}, ConvDecodeP1, P1_FRAME_LEN_FM},
	{"pids", 7, [3]uint{0133, 0171, 0165}, ConvDecodePIDS, PIDS_FRAME_LEN},
	{"p3", 7, [3]uint{0133, 0171, 0165}, func(in []int8, out []uint8) (int, error) { return ConvDecodeP3P4(in, out, P3_FRAME_LEN_MP2) }, P3_FRAME_LEN_MP2},
	{"e1", 9, [3]uint{0561, 0657, 0711}, func(in []int8, out []uint8) (int, error) { return ConvDecodeE1(in, out, P1_FRAME_LEN_AM) }, P1_FRAME_LEN_AM},
	{"e2e3", 9, [3]uint{0561, 0753, 0711}, func(in []int8, out []uint8) (int, error) { return ConvDecodeE2E3(in, out, PIDS_FRAME_LEN) }, PIDS_FRAME_LEN},
}

// TestConvRoundTrip verifies the decoder recovers encoder output exactly
// on a clean channel.
func TestConvRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, c := range convCases {
		bits := make([]uint8, c.length)
		for i := range bits {
			bits[i] = uint8(rng.Intn(2))
		}
		enc := convEncode(bits, c.k, c.gens)
		out := make([]uint8, c.length)
		if _, err := c.decode(enc, out); err != nil {
			t.Fatalf("%s: decode error: %v", c.name, err)
		}
		if !bytes.Equal(bits, out) {
			diff := 0
			for i := range bits {
				if bits[i] != out[i] {
					diff++
				}
			}
			t.Errorf("%s: round-trip mismatch on clean input (%d/%d bits)", c.name, diff, c.length)
		}
	}
}

// TestConvAgainstC compares the Go decoder byte-for-byte against the
// original C decoder on random noisy soft inputs.
func TestConvAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C comparison in short mode")
	}
	rng := rand.New(rand.NewSource(42))
	for _, c := range convCases {
		// The full P1 frame is large; a few trials suffice.
		trials := 3
		length := c.length
		if length > 4096 {
			trials = 1
		}
		for trial := 0; trial < trials; trial++ {
			bits := make([]uint8, length)
			for i := range bits {
				bits[i] = uint8(rng.Intn(2))
			}
			enc := convEncode(bits, c.k, c.gens)

			// Add noise: flip sign of ~7% of soft bits.
			noisy := make([]int8, len(enc))
			copy(noisy, enc)
			for i := range noisy {
				if rng.Float64() < 0.07 {
					noisy[i] = -noisy[i]
				}
			}

			refWhich := c.name
			if refWhich == "p3" {
				refWhich = "p3p4"
			}
			refOut := refDecode(t, refWhich, length, noisy)

			goOut := make([]uint8, length)
			if _, err := c.decode(noisy, goOut); err != nil {
				t.Fatalf("%s: decode error: %v", c.name, err)
			}

			if !bytes.Equal(refOut, goOut) {
				diff := 0
				for i := range refOut {
					if refOut[i] != goOut[i] {
						diff++
					}
				}
				t.Errorf("%s trial %d: Go output differs from C in %d/%d bits", c.name, trial, diff, length)
			}
		}
	}
}
