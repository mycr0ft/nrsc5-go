package nrsc5

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// TestP1ChainAgainstC compares interleaver I + conv decode + descramble
// against the C reference on the same buffer_pm input.
func TestP1ChainAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := os.Stat("/tmp/opencode/refbuild/ref_p1"); err != nil {
		t.Skip("reference P1 chain not built")
	}
	rng := rand.New(rand.NewSource(9))

	bufPM := make([]int8, PM_BLOCK_SIZE*16)
	for i := range bufPM {
		bufPM[i] = int8(rng.Intn(2))*2 - 1
	}

	cmd := exec.Command("/tmp/opencode/refbuild/ref_p1")
	cmd.Stdin = bytes.NewReader(toInt8Bytes(bufPM))
	var stderr bytes.Buffer
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("ref_p1 failed: %v: %s", err, stderr.String())
	}

	viterbi := make([]int8, P1_FRAME_LEN_ENCODED_FM+P1_FRAME_LEN_ENCODED_FM/5)
	interleaverI(bufPM, viterbi, 20, 16, 36, 1, pmV[:], pmVSize, P1_FRAME_LEN_ENCODED_FM)
	scrambler := make([]byte, P1_FRAME_LEN_FM)
	ConvDecodeP1(viterbi, scrambler)
	descramble(scrambler, P1_FRAME_LEN_FM)
	got := scrambler[:P1_FRAME_LEN_FM/8]

	if !bytes.Equal(want, got) {
		diff := 0
		first := -1
		for i := range want {
			if want[i] != got[i] {
				diff++
				if first < 0 {
					first = i
				}
			}
		}
		t.Fatalf("P1 chain differs: %d/%d bytes, first at %d", diff, len(want), first)
	}
}
