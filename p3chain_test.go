package nrsc5

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// TestP3ChainAgainstC compares the PX1 interleaver (streaming type IV) +
// P3 conv decode + descramble against the C reference. The harness runs
// the decoder loop until the interleaver is ready, mirroring decode.c.
func TestP3ChainAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := os.Stat("/tmp/opencode/refbuild/ref_p3"); err != nil {
		t.Skip("reference P3 chain not built")
	}
	rng := rand.New(rand.NewSource(13))

	// soft bits: two halves per pair, repeated until interleaver ready
	buf := make([]int8, P3_FRAME_LEN_MP2*2)
	for i := range buf {
		buf[i] = int8(rng.Intn(2))*2 - 1
	}

	cmd := exec.Command("/tmp/opencode/refbuild/ref_p3")
	cmd.Stdin = bytes.NewReader(toInt8Bytes(buf))
	var stderr bytes.Buffer
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("ref_p3 failed: %v: %s", err, stderr.String())
	}

	// Go side: replicate decode.pushPX1 flow until ready, then decode.
	st := &decode{pids: newPids(nil)}
	st.interleaverPX1.buffer = make([]int8, P3_FRAME_LEN_MP3_MP11*2)
	st.interleaverPX1.internal = make([]int8, P3_FRAME_LEN_MP3_MP11*32)
	st.viterbiP3 = make([]int8, P3_FRAME_LEN_MP3_MP11*3)
	st.scramblerP3 = make([]byte, P3_FRAME_LEN_MP3_MP11)

	for p := 0; p < 73728/(2*P3_FRAME_LEN_MP2); p++ {
		copy(st.interleaverPX1.buffer[:P3_FRAME_LEN_MP2], buf[:P3_FRAME_LEN_MP2])
		st.interleaverIV(&st.interleaverPX1, st.viterbiP3, P3_FRAME_LEN_MP2)
		copy(st.interleaverPX1.buffer[P3_FRAME_LEN_MP2:P3_FRAME_LEN_MP2*2], buf[P3_FRAME_LEN_MP2:])
		st.interleaverIV(&st.interleaverPX1, st.viterbiP3, P3_FRAME_LEN_MP2)
	}

	if !st.interleaverPX1.ready {
		t.Fatal("interleaver not ready after expected pushes")
	}

	ConvDecodeP3P4(st.viterbiP3, st.scramblerP3, P3_FRAME_LEN_MP2)
	descramble(st.scramblerP3, P3_FRAME_LEN_MP2)

	got := st.scramblerP3[:P3_FRAME_LEN_MP2/8]
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
		t.Fatalf("P3 chain differs: %d/%d bytes, first at %d", diff, len(want), first)
	}
}
