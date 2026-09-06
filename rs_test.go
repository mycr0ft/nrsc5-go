package nrsc5

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// rsRefBin is the C reference built from the original Karn code.
const rsRefBin = "/tmp/opencode/refbuild/ref_rs"

func rsRefAvailable() bool {
	_, err := os.Stat(rsRefBin)
	return err == nil
}

// rsRefEncode asks the C reference for parity bytes of a 247-byte virtual
// message block (leading zeros for the shortened code included).
func rsRefEncode(t *testing.T, msg []byte) []byte {
	t.Helper()
	cmd := exec.Command(rsRefBin, "enc")
	cmd.Stdin = bytes.NewReader(msg)
	var stderr bytes.Buffer
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("C encoder failed: %v: %s", err, stderr.String())
	}
	return out
}

// rsRefDecode asks the C reference to fix a 255-byte codeword; returns the
// corrected block and the reported correction count.
func rsRefDecode(t *testing.T, block []byte) ([]byte, int) {
	t.Helper()
	cmd := exec.Command(rsRefBin, "fix")
	cmd.Stdin = bytes.NewReader(block)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("C decoder failed: %v: %s", err, stderr.String())
	}
	var n int
	if _, err := fmt.Sscanf(stderr.String(), "corrections=%d", &n); err != nil {
		t.Fatalf("bad ref output %q", stderr.String())
	}
	return out, n
}

func TestRsGaloisField(t *testing.T) {
	rs := initRs(8, 0x11d, 1, 1, 8)
	if rs == nil {
		t.Fatal("initRs failed")
	}
	// Check GF(256) tables: alpha^i * alpha^j == alpha^(i+j).
	for i := 0; i < 255; i++ {
		for j := 0; j < 255; j++ {
			a := rs.alphaTo[i]
			b := rs.alphaTo[j]
			prod := gfMul(rs, a, b)
			want := rs.alphaTo[(i+j)%255]
			if prod != want {
				t.Fatalf("mul(%d^%d, %d^%d) = %d, want %d", i, j, a, b, prod, want)
			}
		}
	}
}

// gfMul multiplies two GF(256) elements using the codec's tables.
func gfMul(rs *rsState, a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return rs.alphaTo[(int(rs.indexOf[a])+int(rs.indexOf[b]))%255]
}

func TestRsRoundTrip(t *testing.T) {
	rs := initRs(8, 0x11d, 1, 1, 8)
	if rs == nil {
		t.Fatal("initRs failed")
	}
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 10; trial++ {
		// Build a 255-symbol virtual block: 16 zeros + 231 data + 8 parity
		// (systematic shortened RS code).
		block := make([]byte, 255)
		for i := 16; i < 247; i++ {
			block[i] = byte(rng.Intn(256))
		}
		parity := make([]byte, 8)
		encodeRs(rs, block[:247], parity)
		copy(block[247:], parity)

		// Corrupt up to 4 symbols (floor(8/2)).
		nerr := rng.Intn(4) + 1
		pos := rng.Perm(255)
		orig := make([]byte, 255)
		copy(orig, block)
		for i := 0; i < nerr; i++ {
			p := pos[i]
			var v byte
			for {
				v = byte(rng.Intn(256))
				if v != orig[p] {
					break
				}
			}
			block[p] = v
		}
		n := decodeRs(rs, block, nil, 0)
		if n < 0 {
			t.Fatalf("trial %d: decode failed (nerr=%d)", trial, nerr)
		}
		if !bytes.Equal(block, orig) {
			t.Errorf("trial %d: correction mismatch (nerr=%d, reported=%d)", trial, nerr, n)
		}
	}
}

func TestRsAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if !rsRefAvailable() {
		t.Skip("reference RS binary not built")
	}
	rng := rand.New(rand.NewSource(7))
	// Virtual message block: 16 zeros + 231 data symbols.
	msg := make([]byte, 247)
	for i := 16; i < 247; i++ {
		msg[i] = byte(rng.Intn(256))
	}
	cParity := rsRefEncode(t, msg)

	// Go encoder.
	rs := initRs(8, 0x11d, 1, 1, 8)
	parity := make([]byte, 8)
	encodeRs(rs, msg, parity)
	if !bytes.Equal(parity, cParity) {
		t.Errorf("Go parity != C parity: go=%x c=%x", parity, cParity)
	}

	// Full codeword through both decoders, with errors.
	block := make([]byte, 255)
	copy(block, msg)
	copy(block[247:], parity)
	for trial := 0; trial < 5; trial++ {
		orig := make([]byte, 255)
		copy(orig, block)
		noisy := make([]byte, 255)
		copy(noisy, block)
		nerr := rng.Intn(4) + 1
		for _, p := range rng.Perm(255)[:nerr] {
			v := byte(rng.Intn(256))
			if v != noisy[p] {
				noisy[p] = v
			} else {
				noisy[p] = v ^ 0x55
			}
		}
		goBlock := make([]byte, 255)
		copy(goBlock, noisy)
		goN := decodeRs(rs, goBlock, nil, 0)
		cBlock, cN := rsRefDecode(t, noisy)
		if goN != cN {
			t.Errorf("trial %d: correction count go=%d c=%d", trial, goN, cN)
		}
		if !bytes.Equal(goBlock, cBlock) {
			t.Errorf("trial %d: corrected blocks differ", trial)
		}
		if goN >= 0 && !bytes.Equal(goBlock, orig) {
			t.Errorf("trial %d: Go did not restore original", trial)
		}
	}
}
