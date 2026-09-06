package nrsc5

import "fmt"

// Viterbi decoder for the NRSC-5 convolutional codes, ported from
// conv_dec.c / conv_gen.h (originally from Ettus Research, GPLv3).

// Termination types
const (
	convTermFlush       = iota // zero flush termination
	convTermTailBitting        // tail biting termination
)

const tailBitingExtra = 32

// lteConvCode describes a convolutional code.
//
//	n    - Rate 2, 3, 4 (1/2, 1/3, 1/4)
//	k    - Constraint length (7 or 9)
//	len  - Number of information bits
//	rgen - Recursive generator polynomial in octal (0 for non-recursive)
//	gen  - Generator polynomials in octal
//	term - Termination type
type lteConvCode struct {
	n    int
	k    int
	len  int
	rgen uint
	gen  [4]uint
	term int
}

// convDecode performs Viterbi decoding of a tail-biting convolutional code.
// in contains 3 soft bits (NRZ-mapped: >0 is 1, <0 is 0) per input bit;
// out receives len decoded bits (0 or 1).
// Returns the difference between the two best final path metrics
// (a confidence measure, not an error count).
func convDecode(in []int8, out []uint8, code lteConvCode) (int, error) {
	k := code.k
	var ns int
	switch k {
	case 7:
		ns = 64
	case 9:
		ns = 256
	default:
		return 0, fmt.Errorf("nrsc5: unsupported constraint length %d", k)
	}
	if code.n != 3 {
		return 0, fmt.Errorf("nrsc5: unsupported code rate 1/%d", code.n)
	}

	// Tail biting decoders run through the trellis an extra
	// TAIL_BITING_EXTRA cycles on each end.
	decLen := code.len
	if code.term != convTermTailBitting {
		decLen += k - 1
	} else {
		decLen += 2 * tailBitingExtra
	}

	// Trellis outputs (NRZ-mapped) and input bit per state.
	outputs := make([]int16, ns*4)
	vals := make([]uint8, ns)
	for state := 0; state < ns; state++ {
		var o [4]int16
		if code.rgen != 0 {
			genRecStateInfo(code, &vals[state], uint(state), o[:])
		} else {
			genStateInfo(code, &vals[state], uint(state), o[:])
		}
		copy(outputs[4*state:4*state+4], o[:])
	}

	// Sums is the accumulated path metric per state.
	sums := make([]int16, ns)

	// Paths holds the butterfly decision for each time step.
	paths := make([][]int16, decLen)
	paths[0] = make([]int16, ns*decLen)
	for i := 1; i < decLen; i++ {
		paths[i] = paths[0][i*ns : (i+1)*ns]
	}

	// Normalization interval.
	intrvl := (1<<15)/(code.n*127) - k

	// Propagate through the trellis.
	j := 0
	if code.term == convTermTailBitting {
		j = code.len - tailBitingExtra
	}
	for i := 0; i < decLen; i, j = i+1, j+1 {
		if code.term == convTermTailBitting && j == code.len {
			j = 0
		}
		seq := in[3*j : 3*j+3]
		metrics := make([]int16, ns/2)
		for s := 0; s < ns/2; s++ {
			metrics[s] = int16(seq[0])*outputs[4*s+0] +
				int16(seq[1])*outputs[4*s+1] +
				int16(seq[2])*outputs[4*s+2]
		}

		// ACS butterfly for states 0..ns/2-1. State pairs (2s, 2s+1)
		// transition to states s and s+ns/2.
		newSums := make([]int16, ns)
		for s := 0; s < ns/2; s++ {
			state0 := sums[2*s]
			state1 := sums[2*s+1]

			sum0 := int(state0) + int(metrics[s])
			sum1 := int(state1) - int(metrics[s])
			sum2 := int(state0) - int(metrics[s])
			sum3 := int(state1) + int(metrics[s])

			if sum0 > sum1 {
				newSums[s] = int16(sum0)
				paths[i][s] = -1
			} else {
				newSums[s] = int16(sum1)
				paths[i][s] = 0
			}

			if sum2 > sum3 {
				newSums[s+ns/2] = int16(sum2)
				paths[i][s+ns/2] = -1
			} else {
				newSums[s+ns/2] = int16(sum3)
				paths[i][s+ns/2] = 0
			}
		}

		if i%intrvl == 0 {
			min := newSums[0]
			for i2 := 1; i2 < ns; i2++ {
				if newSums[i2] < min {
					min = newSums[i2]
				}
			}
			for i2 := 0; i2 < ns; i2++ {
				newSums[i2] -= min
			}
		}

		copy(sums, newSums)
	}

	// Traceback.
	state := 0
	var maxVal, maxPrev int
	if code.term == convTermTailBitting {
		maxVal, maxPrev = -(1 << 30), -(1 << 30)
		for i := 0; i < ns; i++ {
			s := int(sums[i])
			if s > maxVal {
				maxPrev = maxVal
				maxVal = s
				state = i
			}
		}
		if maxVal < 0 {
			return 0, fmt.Errorf("nrsc5: trellis overflow")
		}
		// Discard the extra TAIL_BITING_EXTRA transitions at the end.
		for i := decLen - 1; i >= code.len+tailBitingExtra; i-- {
			path := int(paths[i][state]) + 1
			state = int(vstateLshift(uint(state), k, path))
		}
	} else {
		for i := decLen - 1; i >= code.len; i-- {
			path := int(paths[i][state]) + 1
			state = int(vstateLshift(uint(state), k, path))
		}
	}

	// Traceback through the data portion, writing decoded bits.
	off := 0
	if code.term == convTermTailBitting {
		off = tailBitingExtra
	}
	if code.rgen != 0 {
		for i := code.len - 1; i >= 0; i-- {
			path := int(paths[i+off][state]) + 1
			out[i] = uint8(path) ^ vals[state]
			state = int(vstateLshift(uint(state), k, path))
		}
	} else {
		for i := code.len - 1; i >= 0; i-- {
			path := int(paths[i+off][state]) + 1
			out[i] = vals[state]
			state = int(vstateLshift(uint(state), k, path))
		}
	}

	return maxVal - maxPrev, nil
}

// vstateLshift left shifts a state register and inserts val as LSB.
func vstateLshift(reg uint, k, val int) uint {
	var mask uint
	switch k {
	case 5:
		mask = 0x0e
	case 7:
		mask = 0x3e
	case 9:
		mask = 0xfe
	}
	return ((reg << 1) & mask) | uint(val)
}

// genStateInfo populates non-recursive trellis state info.
func genStateInfo(code lteConvCode, val *uint8, reg uint, out []int16) {
	// Previous '0' state
	prev := vstateLshift(reg, code.k, 0)

	// Compute output and unpack to NRZ
	*val = uint8((reg >> (code.k - 2)) & 0x01)
	prev |= uint(*val) << (code.k - 1)

	for i := 0; i < code.n; i++ {
		out[i] = int16(parity(prev&code.gen[i])*2 - 1)
	}
}

// genRecStateInfo populates recursive trellis state info.
func genRecStateInfo(code lteConvCode, val *uint8, reg uint, out []int16) {
	// Previous '0' state
	prev := vstateLshift(reg, code.k, 0)

	// Compute recursive input value
	rec := (reg >> (code.k - 2)) & 0x01

	if parity(prev&code.rgen) == int(rec) {
		*val = 0
	} else {
		*val = 1
	}

	// Compute outputs and unpack to NRZ
	prev |= rec << (code.k - 1)

	var mask uint
	switch code.k {
	case 5:
		mask = 0x0f
	case 7:
		mask = 0x3f
	default:
		mask = 0xff
	}

	for i := 0; i < code.n; i++ {
		if code.gen[i]&mask != 0 {
			out[i] = int16(parity(prev&code.gen[i])*2 - 1)
		} else {
			out[i] = int16(*val*2 - 1)
		}
	}
}

func parity(x uint) int {
	x ^= x >> 32
	x ^= x >> 16
	x ^= x >> 8
	x ^= x >> 4
	x ^= x >> 2
	x ^= x >> 1
	return int(x & 1)
}

// Convolutional code parameters used by NRSC-5.
var (
	convCodeK7   = lteConvCode{n: 3, k: 7, gen: [4]uint{0133, 0171, 0165}, term: convTermTailBitting}
	convCodeE1   = lteConvCode{n: 3, k: 9, gen: [4]uint{0561, 0657, 0711}, term: convTermTailBitting}
	convCodeE2E3 = lteConvCode{n: 3, k: 9, gen: [4]uint{0561, 0753, 0711}, term: convTermTailBitting}
)

// ConvDecodeP1 decodes the FM P1 frame (rate 1/3, K=7, tail biting).
func ConvDecodeP1(in []int8, out []uint8) (int, error) {
	code := convCodeK7
	code.len = P1_FRAME_LEN_FM
	return convDecode(in, out, code)
}

// ConvDecodePIDS decodes the FM PIDS frame (rate 1/3, K=7, tail biting).
func ConvDecodePIDS(in []int8, out []uint8) (int, error) {
	code := convCodeK7
	code.len = PIDS_FRAME_LEN
	return convDecode(in, out, code)
}

// ConvDecodeP3P4 decodes FM P3/P4 frames (rate 1/3, K=7, tail biting).
func ConvDecodeP3P4(in []int8, out []uint8, length int) (int, error) {
	code := convCodeK7
	code.len = length
	return convDecode(in, out, code)
}

// ConvDecodeE1 decodes AM P1 frames (rate 1/3, K=9, tail biting).
func ConvDecodeE1(in []int8, out []uint8, length int) (int, error) {
	code := convCodeE1
	code.len = length
	return convDecode(in, out, code)
}

// ConvDecodeE2E3 decodes AM PIDS/P3 frames (rate 1/3, K=9, tail biting).
func ConvDecodeE2E3(in []int8, out []uint8, length int) (int, error) {
	code := convCodeE2E3
	code.len = length
	return convDecode(in, out, code)
}
