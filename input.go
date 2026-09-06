package nrsc5

import (
	"math"
)

// Input chain, ported from input.c.

const amDecimStages = 5

// Sync states.
const (
	syncStateNone = iota
	syncStateCoarse
	syncStateFine
)

type input struct {
	radio  *Radio
	output *output

	decim [amDecimStages]*firFilter
	// stages[s][0/1] holds the two alternating inputs of each halfband stage
	stages [amDecimStages][2]complex64

	resampleInputSize uint
	offset            uint
	syncState         uint

	acq    *acquire
	decode *decode
	frame  *frame
	sync   *sync
}

func newInput(radio *Radio, out *output) *input {
	st := &input{
		radio:  radio,
		output: out,
	}
	for i := 0; i < amDecimStages; i++ {
		st.decim[i] = newFirFilter(decimTaps)
	}
	st.acq = newAcquire(st)
	st.decode = newDecode(st)
	st.frame = newFrame(st)
	st.sync = newSync(st)
	st.reset()
	return st
}

func (st *input) setMode() {
	st.acq.setMode(st.radio.mode)
	st.reset()
}

func (st *input) reset() {
	st.offset = 0
	if st.radio.mode == ModeFM {
		st.resampleInputSize = FFTCP_FM * 2
	} else {
		st.resampleInputSize = FFTCP_AM * 32
	}
	st.setSyncState(syncStateNone)
	for i := 0; i < amDecimStages; i++ {
		st.decim[i].reset()
	}
	st.acq.reset()
	st.decode.reset()
	st.frame.reset()
	st.sync.reset()
}

func (st *input) setSyncState(newState uint) {
	if st.syncState == newState {
		return
	}
	if st.syncState == syncStateFine {
		st.radio.reportLostSync()
	}
	if newState == syncStateFine {
		sampleRate := SAMPLE_RATE_CS16_FM
		if st.radio.mode == ModeAM {
			sampleRate = SAMPLE_RATE_CS16_AM
		}
		freqOffset := (st.acq.prevAngle - 2*float32(math.Pi)*float32(st.acq.cfo)) *
			float32(sampleRate) / (2 * float32(math.Pi) * float32(st.acq.fft))
		st.radio.reportSync(freqOffset, st.sync.psmi, st.sync.pli, st.sync.hppi, st.sync.aabi, st.sync.rdbi)
	}
	st.syncState = newState
}

func (st *input) push(buf []complex64) {
	consumed := 0
	for consumed < len(buf) {
		consumed += st.acq.push(buf[consumed:])
		st.acq.process()
	}
}

// decimateSamples converts cu8 input to complex floats with decimation.
// FM: 2:1 halfband; AM: 32:1 via five halfband stages.
func (st *input) decimateSamples(in []byte, out []complex64) int {
	avail := 0

	for i := 0; i < len(in); i += 4 {
		x0 := complex(U8_F(in[i]), U8_F(in[i+1]))
		x1 := complex(U8_F(in[i+2]), U8_F(in[i+3]))

		if st.radio.mode == ModeFM {
			out[avail] = st.decim[0].executeHalfband(x0, x1)
			avail++
		} else {
			st.stages[0][st.offset&1] = st.decim[0].executeHalfband(x0, x1)
			if st.offset&0x1 == 0x1 {
				st.stages[1][(st.offset>>1)&1] = st.decim[1].executeHalfband(st.stages[0][0], st.stages[0][1])
			}
			if st.offset&0x3 == 0x3 {
				st.stages[2][(st.offset>>2)&1] = st.decim[2].executeHalfband(st.stages[1][0], st.stages[1][1])
			}
			if st.offset&0x7 == 0x7 {
				st.stages[3][(st.offset>>3)&1] = st.decim[3].executeHalfband(st.stages[2][0], st.stages[2][1])
			}
			if st.offset&0xf == 0xf {
				out[avail] = st.decim[4].executeHalfband(st.stages[3][0], st.stages[3][1])
				avail++
			}
			st.offset++
		}
	}

	return avail
}

func (st *input) pushCU8(buf []byte) {
	st.radio.reportIQ(buf)

	out := make([]complex64, FFTCP_FM)
	consumed := 0
	for consumed < len(buf) {
		left := len(buf) - consumed
		min := st.resampleInputSize
		if uint(left) < min {
			min = uint(left)
		}
		avail := st.decimateSamples(buf[consumed:consumed+int(min)], out)
		st.push(out[:avail])
		consumed += int(min)
	}
}

func (st *input) pushCS16(buf []int16) {
	out := make([]complex64, FFTCP_FM)
	consumed := 0
	for consumed < len(buf) {
		left := len(buf) - consumed
		min := FFTCP_FM * 2
		if left < min {
			min = left
		}
		for i := 0; i < min/2; i++ {
			out[i] = Cq15ToCf(CInt16{R: buf[consumed+2*i], I: buf[consumed+2*i+1]})
		}
		st.push(out[:min/2])
		consumed += min
	}
}

func (st *input) pushCF32(buf []float32) {
	// buf holds interleaved re/im floats.
	n := len(buf) / 2
	out := make([]complex64, n)
	for i := 0; i < n; i++ {
		out[i] = complex(buf[2*i], buf[2*i+1])
	}
	st.push(out)
}
