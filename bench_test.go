package nrsc5

import (
	"math/rand"
	"testing"
)

func BenchmarkFFT2048(b *testing.B) {
	st := &acquire{fftin: make([]complex64, FFT_FM), fftout: make([]complex64, FFT_FM), fft: FFT_FM,
		fftPlanFM: newFFTPlan(FFT_FM), fftPlanAM: newFFTPlan(FFT_AM)}
	rng := rand.New(rand.NewSource(1))
	for i := range st.fftin {
		st.fftin[i] = complex(float32(rng.NormFloat64()), float32(rng.NormFloat64()))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.runFFT()
	}
}

func BenchmarkFirFM(b *testing.B) {
	f := newFirFilter(filterTapsFM)
	x := complex(float32(0.1), float32(0.2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.executeFir(x)
	}
}

func BenchmarkConvDecodePIDS(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	in := make([]int8, PIDS_FRAME_LEN*3)
	for i := range in {
		in[i] = int8(rng.Intn(2))*2 - 1
	}
	out := make([]uint8, PIDS_FRAME_LEN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ConvDecodePIDS(in, out)
	}
}
