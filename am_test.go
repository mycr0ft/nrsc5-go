package nrsc5

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// TestAMPIDSInterleaverAgainstC compares the AM PIDS bit-mapping
// (decode_process_pids_am, 1012s.pdf section 10.4 / figure 10-5)
// against the C reference.
func TestAMPIDSInterleaverAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := os.Stat("/tmp/opencode/refbuild/ref_am"); err != nil {
		t.Skip("reference AM harness not built")
	}
	rng := rand.New(rand.NewSource(21))

	// qam16 symbols: 2*BLKSZ bytes
	sbit := make([]byte, 2*BLKSZ)
	for i := range sbit {
		sbit[i] = byte(rng.Intn(256))
	}

	cmd := exec.Command("/tmp/opencode/refbuild/ref_am", "pidsam")
	cmd.Stdin = bytes.NewReader(sbit)
	var stderr bytes.Buffer
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("ref_am failed: %v: %s", err, stderr.String())
	}

	// Go: replicate the mapping (from decode.processPIDSAM, without the
	// pids1_disabled branch which the harness also skips)
	il := make([]byte, 120)
	iu := make([]byte, 120)
	for n := 0; n < 120; n++ {
		p := n % 4

		k := (n + (n / 60) + 11) % 30
		row := (11*(k+(k/15)) + 3) % 32
		il[n] = (sbit[row*2] >> p) & 1

		k = (n + (n / 60)) % 30
		row = (11*(k+(k/15)) + 3) % 32
		iu[n] = (sbit[row*2+1] >> p) & 1
	}

	got := make([]int8, PIDS_FRAME_LEN*3)
	for i := 0; i < 10; i++ {
		for j := 0; j < 12; j++ {
			if il[i*12+j] != 0 {
				got[i*24+pidsIlDelay[j]] = 1
			} else {
				got[i*24+pidsIlDelay[j]] = -1
			}
			if iu[i*12+j] != 0 {
				got[i*24+pidsIuDelay[j]] = 1
			} else {
				got[i*24+pidsIuDelay[j]] = -1
			}
		}
	}

	if !bytes.Equal(want, toInt8Bytes(got)) {
		diff, first := 0, -1
		for i := range want {
			if int8(want[i]) != got[i] {
				diff++
				if first < 0 {
					first = i
				}
			}
		}
		t.Fatalf("AM PIDS interleaver differs: %d/%d, first at %d", diff, len(want), first)
	}
}
