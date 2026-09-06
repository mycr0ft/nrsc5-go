package nrsc5

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// TestAMMA1InterleaverAgainstC compares the AM MA1 interleaver
// (decode.interleaverMA1, psmi != MA3) against the C reference.
func TestAMMA1InterleaverAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := os.Stat("/tmp/opencode/refbuild/ref_am"); err != nil {
		t.Skip("reference AM harness not built")
	}
	rng := rand.New(rand.NewSource(31))

	pl := make([]byte, PARTITION_WIDTH_AM*BLKSZ*8)
	pu := make([]byte, PARTITION_WIDTH_AM*BLKSZ*8)
	s := make([]byte, PARTITION_WIDTH_AM*BLKSZ*8)
	tt := make([]byte, PARTITION_WIDTH_AM*BLKSZ*8)
	for i := range pl {
		pl[i] = byte(rng.Intn(256))
		pu[i] = byte(rng.Intn(256))
		s[i] = byte(rng.Intn(256))
		tt[i] = byte(rng.Intn(256))
	}

	var stdin bytes.Buffer
	stdin.Write(pl)
	stdin.Write(pu)
	stdin.Write(s)
	stdin.Write(tt)
	cmd := exec.Command("/tmp/opencode/refbuild/ref_am", "ma1")
	cmd.Stdin = &stdin
	var stderr bytes.Buffer
	want, err := cmd.Output()
	if err != nil {
		t.Fatalf("ref_am failed: %v: %s", err, stderr.String())
	}
	if len(want) != (8*P1_FRAME_LEN_AM*3 + P3_FRAME_LEN_MA1*3) {
		t.Fatalf("unexpected C output length %d", len(want))
	}

	// Go side: build a decode with minimal input wiring
	radio := NewRadio()
	out := newOutput(radio)
	in := &input{radio: radio, output: out, sync: newSync(nil)}
	in.sync.input = in
	in.sync.psmi = SERVICE_MODE_MA1
	st := newDecode(in)
	st.bufferPL = [PARTITION_WIDTH_AM * BLKSZ * 8]byte{}
	copy(st.bufferPL[:], pl)
	copy(st.bufferPU[:], pu)
	copy(st.bufferS[:], s)
	copy(st.bufferT[:], tt)
	st.interleaverMA1()

	wantV1 := want[:8*P1_FRAME_LEN_AM*3]
	wantV3 := want[8*P1_FRAME_LEN_AM*3:]

	if !bytes.Equal(wantV1, toInt8Bytes(st.viterbiP1AM)) {
		diff, first := 0, -1
		for i := range wantV1 {
			if int8(wantV1[i]) != st.viterbiP1AM[i] {
				diff++
				if first < 0 {
					first = i
				}
			}
		}
		t.Errorf("viterbiP1AM differs: %d/%d, first at %d", diff, len(wantV1), first)
	}
	if !bytes.Equal(wantV3, toInt8Bytes(st.viterbiP3AM[:len(wantV3)])) {
		diff, first := 0, -1
		for i := range wantV3 {
			if int8(wantV3[i]) != st.viterbiP3AM[i] {
				diff++
				if first < 0 {
					first = i
				}
			}
		}
		t.Errorf("viterbiP3AM differs: %d/%d, first at %d", diff, len(wantV3), first)
	}
}
