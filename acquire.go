package nrsc5

import "math"

// FIR filter (complex float), ported from firdecim_cf32.c.
//
// The C code stores taps in reversed order and computes a symmetric dot
// product over a sliding window. We replicate the exact arithmetic:
//
//	dotprod_32:        y = sum_{i=1..15} (a[i]+a[32-i])*b[i] + a[16]*b[16]
//	dotprod_halfband4: y = sum_{i=0,2,4,6} (a[i]+a[14-i])*b[i] + a[7]
//
// where a is the window of the last N pushed samples (a[N-1] newest) and
// b is the reversed tap array. For the 32-tap FM filter, ntaps=32 and
// the window is the last 32 samples. For the AM decim halfband, ntaps=15
// (7 supplied taps reversed, even indices used, center gain 1.0).
type firFilter struct {
	taps   []float32
	window []complex64
	idx    int
	ntaps  int
}

const firWindowSize = 2048

func firReversedTaps(taps []float32) []float32 {
	// C: q->ntaps = (ntaps == 32) ? 32 : 15; taps array is reversed into
	// a buffer of ntaps entries (only the supplied taps are initialized).
	ntaps := len(taps)
	if ntaps != 32 {
		ntaps = 15
	}
	out := make([]float32, ntaps)
	for i := 0; i < len(taps); i++ {
		out[i] = taps[len(taps)-1-i]
	}
	return out
}

func newFirFilter(taps []float32) *firFilter {
	q := &firFilter{
		taps:   firReversedTaps(taps),
		window: make([]complex64, firWindowSize),
	}
	q.reset()
	return q
}

func (q *firFilter) reset() {
	q.idx = len(q.taps) - 1
}

func (q *firFilter) push(x complex64) {
	if q.idx == firWindowSize {
		n := len(q.taps) - 1
		copy(q.window[:n], q.window[q.idx-n:])
		q.idx = n
	}
	q.window[q.idx] = x
	q.idx++
}

// executeFir processes one input sample (the 32-tap FM matched filter).
func (q *firFilter) executeFir(x complex64) complex64 {
	q.push(x)
	a := q.window[q.idx-len(q.taps):]
	b := q.taps
	var sum complex64
	var i int
	for i = 1; i < 16; i++ {
		sum += (a[i] + a[32-i]) * complex(b[i], 0)
	}
	sum += a[i] * complex(b[i], 0)
	return sum
}

// executeHalfband processes two input samples (AM decimator stage),
// returning one output sample.
func (q *firFilter) executeHalfband(x0, x1 complex64) complex64 {
	q.push(x0)
	a := q.window[q.idx-len(q.taps):]
	b := q.taps
	var sum complex64
	for i := 0; i < 7; i += 2 {
		sum += (a[i] + a[14-i]) * complex(b[i], 0)
	}
	sum += a[7]
	q.push(x1)
	return sum
}

// Filter taps from acquire.c (RRC matched filter, FM) and input.c.
var filterTapsFM = []float32{
	-0.000685643230099231,
	0.005636964458972216,
	0.009015781804919243,
	-0.015486305579543114,
	-0.035108357667922974,
	0.017446253448724747,
	0.08155813068151474,
	0.007995186373591423,
	-0.13311293721199036,
	-0.0727422907948494,
	0.15914097428321838,
	0.16498781740665436,
	-0.1324498951435089,
	-0.2484012246131897,
	0.051773931831121445,
	0.2821577787399292,
	0.051773931831121445,
	-0.2484012246131897,
	-0.1324498951435089,
	0.16498781740665436,
	0.15914097428321838,
	-0.0727422907948494,
	-0.13311293721199036,
	0.007995186373591423,
	0.08155813068151474,
	0.017446253448724747,
	-0.035108357667922974,
	-0.015486305579543114,
	0.009015781804919243,
	0.005636964458972216,
	-0.000685643230099231,
	0,
}

var filterTapsAM = []float32{
	-0.00038464731187559664,
	-0.00021618751634377986,
	0.0026779419276863337,
	-0.00029802651260979474,
	-0.0012626448879018426,
	-0.0013182522961869836,
	-0.012252614833414555,
	0.015980124473571777,
	0.037112727761268616,
	-0.05451361835002899,
	-0.05804193392395973,
	0.11320608854293823,
	0.055298302322626114,
	-0.16878043115139008,
	-0.022917453199625015,
	0.19178225100040436,
	-0.022917453199625015,
	-0.16878043115139008,
	0.055298302322626114,
	0.11320608854293823,
	-0.05804193392395973,
	-0.05451361835002899,
	0.037112727761268616,
	0.015980124473571777,
	-0.012252614833414555,
	-0.0013182522961869836,
	-0.0012626448879018426,
	-0.00029802651260979474,
	0.0026779419276863337,
	-0.00021618751634377986,
	-0.00038464731187559664,
	0,
}

// Decimation filter for AM (five halfband stages, 2:1 each).
var decimTaps = []float32{
	0.6062333583831787,
	0,
	-0.13481467962265015,
	0,
	0.032919470220804214,
	0,
	-0.00410953676328063,
}

const acquireFilterDelay = 15

const (
	decimationFactorFM = 2
	decimationFactorAM = 32
)

// acquire implements acquire.c.
type acquire struct {
	input *input

	filterFM *firFilter
	filterAM *firFilter

	inBuffer  []complex64
	buffer    []complex64
	sums      []complex64
	fftin     []complex64
	fftout    []complex64
	fftPlanFM *fftPlan
	fftPlanAM *fftPlan
	shape     []float32
	shapeFM   [FFTCP_FM]float32
	shapeAM   [FFTCP_AM]float32

	idx       int
	prevAngle float32
	phase     complex64
	keepExtra int
	cfo       int

	mode  int
	fft   int
	fftcp int
	cp    int
}

func newAcquire(input *input) *acquire {
	st := &acquire{
		input:     input,
		mode:      ModeFM,
		fft:       FFT_FM,
		fftcp:     FFTCP_FM,
		cp:        CP_FM,
		inBuffer:  make([]complex64, FFTCP_FM*(ACQUIRE_SYMBOLS+1)),
		buffer:    make([]complex64, FFTCP_FM*(ACQUIRE_SYMBOLS+1)),
		sums:      make([]complex64, FFTCP_FM),
		fftin:     make([]complex64, FFT_FM),
		fftout:    make([]complex64, FFT_FM),
		fftPlanFM: newFFTPlan(FFT_FM),
		fftPlanAM: newFFTPlan(FFT_AM),
	}
	st.filterFM = newFirFilter(filterTapsFM)
	st.filterAM = newFirFilter(filterTapsAM)

	// Pulse shaping window function for FM.
	for i := 0; i < FFTCP_FM; i++ {
		switch {
		case i < CP_FM:
			st.shapeFM[i] = float32(math.Sin(math.Pi / 2 * float64(i) / CP_FM))
		case i < FFT_FM:
			st.shapeFM[i] = 1
		default:
			st.shapeFM[i] = float32(math.Cos(math.Pi / 2 * float64(i-FFT_FM) / CP_FM))
		}
	}
	// Pulse shaping window function for AM.
	for i := 0; i < FFTCP_AM; i++ {
		switch {
		case i < CP_AM:
			st.shapeAM[i] = float32(math.Sin(math.Pi / 2 * float64(i) / CP_AM))
		case i < FFT_AM:
			st.shapeAM[i] = 1
		default:
			st.shapeAM[i] = float32(math.Cos(math.Pi / 2 * float64(i-FFT_AM) / CP_AM))
		}
	}
	st.shape = st.shapeFM[:]
	st.reset()
	return st
}

func (st *acquire) setMode(mode int) {
	st.mode = mode
	if st.mode == ModeFM {
		st.fft = FFT_FM
		st.fftcp = FFTCP_FM
		st.cp = CP_FM
		st.shape = st.shapeFM[:]
	} else {
		st.fft = FFT_AM
		st.fftcp = FFTCP_AM
		st.cp = CP_AM
		st.shape = st.shapeAM[:]
	}
}

func (st *acquire) reset() {
	st.filterFM.reset()
	st.filterAM.reset()
	st.idx = 0
	st.prevAngle = 0
	st.phase = 1
	st.keepExtra = 0
	st.cfo = 0
}

func (st *acquire) setKeepExtra(extra int) {
	st.keepExtra = extra
}

func (st *acquire) cfoAdjust(cfo int) {
	st.cfo += cfo
}

func (st *acquire) push(buf []complex64) int {
	size := st.fftcp * (ACQUIRE_SYMBOLS + 1)
	needed := size - st.idx

	pushed := len(buf)
	if pushed > needed {
		pushed = needed
	}
	copy(st.inBuffer[st.idx:], buf[:pushed])
	st.idx += pushed
	return pushed
}

func cexpj(angle float32) complex64 {
	return complex(float32(math.Cos(float64(angle))), float32(math.Sin(float64(angle))))
}

func cargs(v complex64) float32 {
	return float32(math.Atan2(float64(imag(v)), float64(real(v))))
}

func cabss(v complex64) float32 {
	return float32(math.Hypot(float64(real(v)), float64(imag(v))))
}

// fft executes a forward DFT (fftin -> fftout).
func (st *acquire) runFFT() {
	if st.fft == FFT_AM {
		st.fftPlanAM.forward(st.fftin, st.fftout)
	} else {
		st.fftPlanFM.forward(st.fftin, st.fftout)
	}
}

// FFT twiddle tables (precomputed at init).
var (
	fftTwidFM = computeTwiddles(FFT_FM)
	fftTwidAM = computeTwiddles(FFT_AM)
)

// computeTwiddles precomputes e^{-2*pi*i*k/n} for k in [0, n/2).
func computeTwiddles(n int) []complex64 {
	t := make([]complex64, n/2)
	for k := 0; k < n/2; k++ {
		angle := -2 * float32(math.Pi) * float32(k) / float32(n)
		t[k] = cexpj(angle)
	}
	return t
}

// fftWork is a reusable scratch buffer for the FFT.
type fftPlan struct {
	n       int
	twiddle []complex64
	tmp     []complex64
}

func newFFTPlan(n int) *fftPlan {
	return &fftPlan{n: n, twiddle: computeTwiddles(n), tmp: make([]complex64, n)}
}

// fftForward computes an in-order forward DFT of size n into out.
func (p *fftPlan) forward(in, out []complex64) {
	n := p.n
	copy(p.tmp, in[:n])
	bitReverse(p.tmp, n)
	for length := 2; length <= n; length <<= 1 {
		half := length / 2
		step := n / length
		for base := 0; base < n; base += length {
			for k := 0; k < half; k++ {
				w := p.twiddle[k*step]
				e := base + k
				m := e + half
				t := w * p.tmp[m]
				u := p.tmp[e]
				p.tmp[e] = u + t
				p.tmp[m] = u - t
			}
		}
	}
	copy(out[:n], p.tmp)
}

// ifftForward computes an inverse DFT scaled by 1/n.
func fftInverse(in, out []complex64, n int) {
	fftForwardConj(in, out, n)
	inv := float32(1) / float32(n)
	for i := range out[:n] {
		out[i] *= complex(inv, 0)
	}
}

func fftForwardConj(in, out []complex64, n int) {
	tmp := make([]complex64, n)
	copy(tmp, in)
	bitReverse(tmp, n)
	for length := 2; length <= n; length <<= 1 {
		step := n / length
		for base := 0; base < n; base += length {
			for k := 0; k < length/2; k++ {
				angle := 2 * float32(math.Pi) * float32(k*step) / float32(n)
				w := cexpj(angle)
				e := base + k
				m := e + length/2
				t := w * tmp[m]
				u := tmp[e]
				tmp[e] = u + t
				tmp[m] = u - t
			}
		}
	}
	copy(out, tmp)
}

func bitReverse(x []complex64, n int) {
	j := 0
	for i := 1; i < n-1; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
}

func (st *acquire) process() {
	if st.idx != st.fftcp*(ACQUIRE_SYMBOLS+1) {
		return
	}

	st.input.output.advance()

	var samperr int
	var angle float32

	if st.input.syncState == syncStateFine {
		samperr = st.fftcp/2 + st.input.sync.samperr
		st.input.sync.samperr = 0

		angleDiff := -st.input.sync.angle
		st.input.sync.angle = 0
		angle = st.prevAngle + angleDiff
		st.prevAngle = angle
	} else {
		var y complex64
		for i := 0; i < st.fftcp*(ACQUIRE_SYMBOLS+1); i++ {
			if st.mode == ModeFM {
				y = st.filterFM.executeFir(st.inBuffer[i])
				st.buffer[i] = cmplxConj(y)
			} else {
				st.buffer[i] = st.filterAM.executeFir(st.inBuffer[i])
			}
		}

		for i := range st.sums[:st.fftcp] {
			st.sums[i] = 0
		}
		for i := 0; i < st.fftcp; i++ {
			for j := 0; j < ACQUIRE_SYMBOLS; j++ {
				st.sums[i] += st.buffer[i+j*st.fftcp] * cmplxConj(st.buffer[i+j*st.fftcp+st.fft])
			}
		}

		maxMag := float32(-1.0)
		var maxV complex64
		for i := 0; i < st.fftcp; i++ {
			var v complex64
			for j := 0; j < st.cp; j++ {
				v += st.sums[(i+j)%st.fftcp] * complex(st.shape[j]*st.shape[j+st.fft], 0)
			}
			mag := Norm2(v)
			if mag > maxMag {
				maxMag = mag
				maxV = v
				samperr = (i + st.fftcp - acquireFilterDelay) % st.fftcp
			}
		}

		angleDiff := cargs(maxV * cexpj(-st.prevAngle))
		angleFactor := float32(1.0)
		if st.prevAngle != 0 {
			angleFactor = 0.25
		}
		angle = st.prevAngle + angleDiff*angleFactor
		st.prevAngle = angle
		st.input.setSyncState(syncStateCoarse)
	}

	for i := 0; i < st.fftcp*(ACQUIRE_SYMBOLS+1); i++ {
		if st.mode == ModeFM {
			st.buffer[i] = cmplxConj(st.inBuffer[i])
		} else {
			st.buffer[i] = st.inBuffer[i]
		}
	}

	st.input.sync.adjust(st.fftcp/2 - samperr)
	angle -= 2 * float32(math.Pi) * float32(st.cfo)

	st.phase *= cexpj(-(float32(st.fftcp/2-samperr) * angle / float32(st.fft)))

	phaseIncrement := cexpj(angle / float32(st.fft))

	if st.mode == ModeAM {
		var sumY, sumXY, sumX2 float32
		var lastCarrier complex64
		tempPhase := st.phase
		var magSums [FFT_AM]float32

		for i := 0; i < ACQUIRE_SYMBOLS; i++ {
			offset := (FFT_AM - CP_AM) / 2
			for j := 0; j < st.fftcp; j++ {
				sample := tempPhase * st.buffer[i*st.fftcp+j+samperr]
				switch {
				case j < st.cp:
					st.fftin[(j+offset)%st.fft] = complex(st.shape[j], 0) * sample
				case j < st.fft:
					st.fftin[(j+offset)%st.fft] = sample
				default:
					st.fftin[(j+offset)%st.fft] += complex(st.shape[j], 0) * sample
				}
				tempPhase *= phaseIncrement
			}
			tempPhase /= complex(cabss(tempPhase), 0)

			st.runFFT()
			FFTShift(st.fftout[:st.fft])

			x := float32(st.fftcp * (i - (ACQUIRE_SYMBOLS-1)/2))
			var y float32
			if i == 0 {
				y = cargs(st.fftout[CENTER_AM])
			} else {
				y += cargs(st.fftout[CENTER_AM] / lastCarrier)
			}
			lastCarrier = st.fftout[CENTER_AM]

			sumY += y
			sumXY += x * y
			sumX2 += x * x

			if st.input.syncState != syncStateFine {
				for j := CENTER_AM - PIDS_OUTER_INDEX_AM; j <= CENTER_AM+PIDS_OUTER_INDEX_AM; j++ {
					magSums[j] += cabss(st.fftout[j])
				}
			}
		}

		if st.input.syncState != syncStateFine {
			maxMag := float32(-1.0)
			maxIndex := -1
			for j := CENTER_AM - PIDS_OUTER_INDEX_AM; j <= CENTER_AM+PIDS_OUTER_INDEX_AM; j++ {
				if magSums[j] > maxMag {
					maxMag = magSums[j]
					maxIndex = j
				}
			}
			st.cfoAdjust(maxIndex - CENTER_AM)
		}

		phaseIncrement *= cexpj(-sumXY / sumX2)
		// TODO: Investigate why 0.06 is needed below (from C source).
		st.phase *= cexpj(-sumY/ACQUIRE_SYMBOLS + (sumXY/sumX2)*ACQUIRE_SYMBOLS*float32(st.fftcp)/2 - 0.06)
	}

	for i := 0; i < ACQUIRE_SYMBOLS; i++ {
		offset := 0
		if st.mode == ModeAM {
			offset = (FFT_AM - CP_AM) / 2
		}
		for j := 0; j < st.fftcp; j++ {
			sample := st.phase * st.buffer[i*st.fftcp+j+samperr]
			switch {
			case j < st.cp:
				st.fftin[(j+offset)%st.fft] = complex(st.shape[j], 0) * sample
			case j < st.fft:
				st.fftin[(j+offset)%st.fft] = sample
			default:
				st.fftin[(j+offset)%st.fft] += complex(st.shape[j], 0) * sample
			}
			st.phase *= phaseIncrement
		}
		st.phase /= complex(cabss(st.phase), 0)

		st.runFFT()
		FFTShift(st.fftout[:st.fft])
		st.input.sync.push(st.fftout[:st.fft])
	}

	keep := st.fftcp + (st.fftcp/2 - samperr) + st.keepExtra
	st.keepExtra = 0
	copy(st.inBuffer, st.inBuffer[st.idx-keep:st.idx])
	st.idx = keep
}

func cmplxConj(v complex64) complex64 {
	return complex(real(v), -imag(v))
}
