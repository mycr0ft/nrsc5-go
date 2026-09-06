package nrsc5

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// refFir runs the C firdecim implementation on interleaved float IQ.
func refFir(t *testing.T, which string, in []complex64) []complex64 {
	t.Helper()
	raw := make([]byte, 8*len(in))
	for i, v := range in {
		binary.LittleEndian.PutUint32(raw[8*i:], math.Float32bits(real(v)))
		binary.LittleEndian.PutUint32(raw[8*i+4:], math.Float32bits(imag(v)))
	}
	cmd := exec.Command("/tmp/opencode/refbuild/ref_fir", which)
	cmd.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ref_fir failed: %v: %s", err, stderr.String())
	}
	nOut := len(out) / 8
	res := make([]complex64, nOut)
	for i := 0; i < nOut; i++ {
		re := math.Float32frombits(binary.LittleEndian.Uint32(out[8*i:]))
		im := math.Float32frombits(binary.LittleEndian.Uint32(out[8*i+4:]))
		res[i] = complex(re, im)
	}
	return res
}

func TestFirAgainstC(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := os.Stat("/tmp/opencode/refbuild/ref_fir"); err != nil {
		t.Skip("reference fir binary not built")
	}
	rng := rand.New(rand.NewSource(11))

	// FM matched filter: one output per input.
	n := 200
	in := make([]complex64, n)
	for i := range in {
		in[i] = complex(float32(rng.NormFloat64()), float32(rng.NormFloat64()))
	}
	want := refFir(t, "fm", in)

	f := newFirFilter(filterTapsFM)
	got := make([]complex64, n)
	for i, x := range in {
		got[i] = f.executeFir(x)
	}
	for i := range got {
		dr := real(got[i]) - real(want[i])
		di := imag(got[i]) - imag(want[i])
		if math.Abs(float64(dr)) > 1e-4 || math.Abs(float64(di)) > 1e-4 {
			t.Fatalf("FM fir mismatch at %d: got (%v,%v) want (%v,%v)", i, real(got[i]), imag(got[i]), real(want[i]), imag(want[i]))
		}
	}

	// AM decim halfband: two inputs per output.
	n = 200
	in = make([]complex64, 2*n)
	for i := range in {
		in[i] = complex(float32(rng.NormFloat64()), float32(rng.NormFloat64()))
	}
	want = refFir(t, "am", in)

	f = newFirFilter(decimTaps)
	got = make([]complex64, n)
	for i := 0; i < n; i++ {
		got[i] = f.executeHalfband(in[2*i], in[2*i+1])
	}
	for i := range got {
		dr := real(got[i]) - real(want[i])
		di := imag(got[i]) - imag(want[i])
		if math.Abs(float64(dr)) > 1e-4 || math.Abs(float64(di)) > 1e-4 {
			t.Fatalf("AM halfband mismatch at %d: got (%v,%v) want (%v,%v)", i, real(got[i]), imag(got[i]), real(want[i]), imag(want[i]))
		}
	}
}
