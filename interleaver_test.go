package nrsc5

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// TestInterleaverAgainstC compares the Go PM interleavers against the
// original C implementation on random soft-bit input.
func TestInterleaverAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := os.Stat("/tmp/opencode/refbuild/ref_intl"); err != nil {
		t.Skip("reference interleaver not built")
	}
	rng := rand.New(rand.NewSource(5))

	// buffer_pm: 16 blocks * 32 rows * 20 partitions * 36 columns
	bufPM := make([]int8, PM_BLOCK_SIZE*16)
	for i := range bufPM {
		bufPM[i] = int8(rng.Intn(2))*2 - 1
	}

	// Interleaver I (P1)
	inBytes := toInt8Bytes(bufPM)
	cmd := exec.Command("/tmp/opencode/refbuild/ref_intl", "i")
	cmd.Stdin = bytes.NewReader(inBytes)
	var stderr bytes.Buffer
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("ref_intl i failed: %v: %s", err, stderr.String())
	}

	got := make([]int8, P1_FRAME_LEN_ENCODED_FM+P1_FRAME_LEN_ENCODED_FM/5)
	interleaverI(bufPM, got, 20, 16, 36, 1, pmV[:], pmVSize, P1_FRAME_LEN_ENCODED_FM)
	if !bytes.Equal(want, toInt8Bytes(got)) {
		for i := range want {
			if int8(want[i]) != got[i] {
				t.Fatalf("interleaver I differs at %d: c=%d go=%d", i, want[i], got[i])
			}
		}
	}

	// Interleaver II (PIDS) for each bc
	for bc := 0; bc < 16; bc++ {
		cmd := exec.Command("/tmp/opencode/refbuild/ref_intl", "ii", itoa(bc))
		cmd.Stdin = bytes.NewReader(inBytes)
		var stderr bytes.Buffer
		want, err := cmd.Output()
		if err != nil {
			t.Fatalf("ref_intl ii failed: %v: %s", err, stderr.String())
		}
		got := make([]int8, PIDS_FRAME_LEN_ENCODED_FM+PIDS_FRAME_LEN_ENCODED_FM/5)
		interleaverII(bufPM, got, uint(bc), 20, 16, 36, pmV[:], pmVSize, PIDS_FRAME_LEN_ENCODED_FM, P1_FRAME_LEN_ENCODED_FM)
		if !bytes.Equal(want, toInt8Bytes(got)) {
			for i := range want {
				if int8(want[i]) != got[i] {
					t.Fatalf("interleaver II bc=%d differs at %d: c=%d go=%d", bc, i, want[i], got[i])
				}
			}
		}
	}
}

func toInt8Bytes(in []int8) []byte {
	out := make([]byte, len(in))
	for i, v := range in {
		out[i] = byte(v)
	}
	return out
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
