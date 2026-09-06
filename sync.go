package nrsc5

import "math"

// OFDM sync/demod, ported from sync.c.

const maxPartitions = 14
const middleRefSC = 30 // midpoint of Table 11-3 in 1011s.pdf

// Table 6-4 in 1011s.pdf
var compatibilityMode = [64]int{
	0, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
	6, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
	6, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
	6, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
}

func gray4(f float32) byte {
	switch {
	case f < -1:
		return 0
	case f < 0:
		return 2
	case f < 1:
		return 3
	default:
		return 1
	}
}

func gray8(f float32) byte {
	switch {
	case f < -3:
		return 0
	case f < -2:
		return 4
	case f < -1:
		return 6
	case f < 0:
		return 2
	case f < 1:
		return 3
	case f < 2:
		return 7
	case f < 3:
		return 5
	default:
		return 1
	}
}

func demod(x, mult float32) int8 {
	clamped := x
	if clamped > 1 {
		clamped = 1
	}
	if clamped < -1 {
		clamped = -1
	}
	return int8(math.Round(float64(clamped * mult)))
}

func qpsk(cf complex64) byte {
	var b byte
	if real(cf) >= 0 {
		b |= 1
	}
	if imag(cf) >= 0 {
		b |= 2
	}
	return b
}

func qam16(cf complex64) byte {
	return gray4(real(cf)) | gray4(imag(cf))<<2
}

func qam64(cf complex64) byte {
	return gray8(real(cf)) | gray8(imag(cf))<<3
}

// adjust_ref performs Costas loop adjustment on a reference subcarrier.
func (st *sync) adjustRef(ref int, cfo int) {
	cfoFreq := 2 * float32(math.Pi) * float32(cfo) * CP_FM / FFT_FM

	// differentially-encoded sync & parity bits
	var sync = [BLKSZ]int8{
		-1, 1, -1, -1, -1, 1, 1, 0, 1, -1, 0, 0, 0, -1, -1, 0,
		0, 0, 0, 0, -1, 1, -1, 0, 0, 0, 0, 0, 0, 0, 0, -1,
	}

	for n := 0; n < BLKSZ; n++ {
		error := cargs(st.buffer[ref][n]*st.buffer[ref][n]*cexpj(-2*st.costasPhase[ref])) * 0.5

		st.phases[ref][n] = st.costasPhase[ref]
		st.buffer[ref][n] *= cexpj(-st.costasPhase[ref])

		st.costasFreq[ref] += st.beta * error
		if st.costasFreq[ref] > 0.5 {
			st.costasFreq[ref] = 0.5
		}
		if st.costasFreq[ref] < -0.5 {
			st.costasFreq[ref] = -0.5
		}
		st.costasPhase[ref] += st.costasFreq[ref] + cfoFreq + st.alpha*error
		if st.costasPhase[ref] > float32(math.Pi) {
			st.costasPhase[ref] -= 2 * float32(math.Pi)
		}
		if st.costasPhase[ref] < -float32(math.Pi) {
			st.costasPhase[ref] += 2 * float32(math.Pi)
		}
	}

	// compare to sync & parity bits
	x := float32(0)
	for n := 0; n < BLKSZ; n++ {
		x += real(st.buffer[ref][n]) * float32(sync[n])
	}
	if x < 0 {
		// adjust phase by pi to compensate
		for n := 0; n < BLKSZ; n++ {
			st.phases[ref][n] += float32(math.Pi)
			st.buffer[ref][n] *= -1
		}
		st.costasPhase[ref] += float32(math.Pi)
	}
}

func (st *sync) resetRef(ref int) {
	for n := 0; n < BLKSZ; n++ {
		st.buffer[ref][n] *= cexpj(st.phases[ref][n])
	}
}

func decodeDBPSK(buf []complex64, data []byte, size int) {
	prev := byte(0)
	for n := 0; n < size; n++ {
		var bit byte
		if real(buf[n]) > 0 {
			bit = 1
		}
		data[n] = bit ^ prev
		prev = bit
	}
}

func fuzzyMatch(needle []int8, data []byte, size int) int {
	for n := 0; n < size; n++ {
		i := 0
		for ; i < len(needle); i++ {
			// ignore don't care bits
			if needle[i] < 0 {
				continue
			}
			// test if bit is correct
			if needle[i] != int8(data[(n+i)%size]) {
				break
			}
		}
		if i == len(needle) {
			return n
		}
	}
	return -1
}

func (st *sync) decodeRefFM(ref int, rsid int) (bc int, psmi int, ok bool) {
	needle := []int8{
		0, 1, 0, 0, 0, 1, 1, -1, 1, 0, int8(rsid >> 1), int8((rsid >> 1) ^ (rsid & 1)), -1, 0, 0, -1,
		-1, -1, -1, -1, 0, 1, 0, -1, -1, -1, -1, -1, -1, -1, -1, 0,
	}
	var data [BLKSZ]byte

	for n := 0; n < BLKSZ; n++ {
		if needle[n] >= 0 {
			var bit byte
			if real(st.buffer[ref][n]) > 0 {
				bit = 1
			}
			if needle[n] != int8(bit) {
				return 0, 0, false
			}
		}
	}

	decodeDBPSK(st.buffer[ref][:], data[:], BLKSZ)
	bc = (int(data[16]) << 3) | (int(data[17]) << 2) | (int(data[18]) << 1) | int(data[19])
	psmi = (int(data[25]) << 5) | (int(data[26]) << 4) | (int(data[27]) << 3) | (int(data[28]) << 2) | (int(data[29]) << 1) | int(data[30])
	return bc, psmi, true
}

func (st *sync) findRefFM(ref int, rsid int) int {
	needle := []int8{
		0, 1, 0, 0, 0, 1, 1, -1, 1, 0, int8(rsid >> 1), int8((rsid >> 1) ^ (rsid & 1)), -1, 0, 0, -1,
		-1, -1, -1, -1, 0, 1, 0, -1, -1, -1, -1, -1, -1, -1, -1, 0,
	}
	var data [BLKSZ]byte

	for n := 0; n < BLKSZ; n++ {
		if real(st.buffer[ref][n]) <= 0 {
			data[n] = 0
		} else {
			data[n] = 1
		}
	}

	match := fuzzyMatch(needle, data[:], BLKSZ)
	if match >= 0 {
		return match
	}

	for n := 0; n < BLKSZ; n++ {
		data[n] ^= 1
	}
	return fuzzyMatch(needle, data[:], BLKSZ)
}

func (st *sync) findBlockAM(ref int) int {
	needle := []int8{
		0, 1, 1, 0, 0, 1, 0, -1, -1, 1, -1, -1, -1, -1, 0, -1, -1, -1, -1, -1, -1, 1, 1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
	}
	var data [BLKSZ]byte

	for n := 0; n < BLKSZ; n++ {
		if imag(st.buffer[ref][n]) <= 0 {
			data[n] = 0
		} else {
			data[n] = 1
		}
		if needle[n] >= 0 && data[n] != byte(needle[n]) {
			return -1
		}
	}

	// parity checks
	if data[7]^data[8] != 0 {
		return -1
	}
	if data[10]^data[11]^data[12]^data[13] != 0 {
		return -1
	}
	if data[15]^data[16]^data[17]^data[18]^data[19]^data[20] != 0 {
		return -1
	}
	if data[23]^data[24]^data[25]^data[26]^data[27]^data[28]^data[29]^data[30]^data[31] != 0 {
		return -1
	}

	bc := (int(data[17]) << 2) | (int(data[18]) << 1) | int(data[19])
	if bc == 0 {
		st.psmi = (int(data[26]) << 4) | (int(data[27]) << 3) | (int(data[28]) << 2) | (int(data[29]) << 1) | int(data[30])
		st.pli = int(data[7])
		st.hppi = int(data[11])
		st.aabi = int(data[12])
		st.rdbi = int(data[15])
	}
	return bc
}

func (st *sync) findRefAM(ref int) int {
	needle := []int8{
		0, 1, 1, 0, 0, 1, 0, -1, -1, 1, -1, -1, -1, -1, 0, -1, -1, -1, -1, -1, -1, 1, 1,
	}
	var data [BLKSZ]byte

	for n := 0; n < BLKSZ; n++ {
		if imag(st.buffer[ref][n]) <= 0 {
			data[n] = 0
		} else {
			data[n] = 1
		}
	}
	return fuzzyMatch(needle, data[:], BLKSZ)
}

func (st *sync) calcSmag(ref int) float32 {
	sum := float32(0)
	// phase was already corrected, so imaginary component is zero
	for n := 0; n < BLKSZ; n++ {
		sum += absf(real(st.buffer[ref][n]))
	}
	return sum / BLKSZ
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func (st *sync) adjustData(lower, upper int) {
	smag0 := st.calcSmag(lower)
	smag19 := st.calcSmag(upper)

	for n := 0; n < BLKSZ; n++ {
		upperPhase := cexpj(st.phases[upper][n])
		lowerPhase := cexpj(st.phases[lower][n])

		for k := 1; k < PARTITION_WIDTH_FM; k++ {
			// average phase difference
			c := complex(float32(PARTITION_WIDTH_FM), float32(PARTITION_WIDTH_FM)) /
				(complex(float32(k)*smag19, 0)*upperPhase + complex(float32(PARTITION_WIDTH_FM-k)*smag0, 0)*lowerPhase)
			// adjust sample
			st.buffer[lower+k][n] *= c
		}
	}
}

func phaseDiff(a, b float32) float32 {
	diff := a - b
	pi := float32(math.Pi)
	for diff > pi/2 {
		diff -= pi
	}
	for diff < -pi/2 {
		diff += pi
	}
	return diff
}

func (st *sync) detectCFO() {
	for cfo := -2 * PARTITION_WIDTH_FM; cfo < 2*PARTITION_WIDTH_FM; cfo++ {
		bestOffset := -1
		bestCount := 0
		var offsetCount [BLKSZ]int

		for i := 0; i <= PM_PARTITIONS; i++ {
			st.adjustRef(cfo+LB_START+i*PARTITION_WIDTH_FM, cfo)
			offset := st.findRefFM(cfo+LB_START+i*PARTITION_WIDTH_FM, (middleRefSC-i)&0x3)
			st.resetRef(cfo + LB_START + i*PARTITION_WIDTH_FM)
			if offset >= 0 {
				offsetCount[offset]++
			}

			st.adjustRef(cfo+UB_END-i*PARTITION_WIDTH_FM, cfo)
			offset = st.findRefFM(cfo+UB_END-i*PARTITION_WIDTH_FM, (middleRefSC-i)&0x3)
			st.resetRef(cfo + UB_END - i*PARTITION_WIDTH_FM)
			if offset >= 0 {
				offsetCount[offset]++
			}
		}

		for offset := 0; offset < BLKSZ; offset++ {
			if offsetCount[offset] > bestCount {
				bestOffset = offset
				bestCount = offsetCount[offset]
			}
		}

		if bestOffset >= 0 && bestCount >= 3 {
			// At least three offsets matched, so this is likely the correct CFO.
			st.input.acq.setKeepExtra(((BLKSZ - bestOffset) % BLKSZ) * FFTCP_FM)
			st.input.acq.cfoAdjust(cfo)

			// Wait until the buffers have cleared before measuring again.
			st.cfoWait = 8
			break
		}
	}
}

func (st *sync) processFM() {
	var partitionsPerBand int

	switch compatibilityMode[st.psmi] {
	case 2:
		partitionsPerBand = 11
	case 3:
		partitionsPerBand = 12
	case 5, 6, 11:
		partitionsPerBand = 14
	default:
		partitionsPerBand = 10
	}

	for i := 0; i < partitionsPerBand*PARTITION_WIDTH_FM+1; i += PARTITION_WIDTH_FM {
		st.adjustRef(LB_START+i, 0)
		st.adjustRef(UB_END-i, 0)
	}

	// check if we now have synchronization
	if st.input.syncState == syncStateCoarse {
		goodRefs := 0
		var seenBC [16]int
		var seenPsmi [64]int
		for i := 0; i <= partitionsPerBand; i++ {
			bc, psmi, ok := st.decodeRefFM(LB_START+i*PARTITION_WIDTH_FM, (middleRefSC-i)&0x3)
			if ok {
				goodRefs++
				seenBC[bc]++
				seenPsmi[psmi]++
			}
			bc, psmi, ok = st.decodeRefFM(UB_END-i*PARTITION_WIDTH_FM, (middleRefSC-i)&0x3)
			if ok {
				goodRefs++
				seenBC[bc]++
				seenPsmi[psmi]++
			}
		}

		if goodRefs >= 4 {
			majorityBC := -1
			for bc := 0; bc < 16; bc++ {
				if seenBC[bc] > goodRefs/2 {
					majorityBC = bc
				}
			}

			majorityPsmi := -1
			for psmi := 0; psmi < 16; psmi++ {
				if seenPsmi[psmi] > goodRefs/2 {
					majorityPsmi = psmi
				}
			}

			if majorityBC >= 0 && majorityPsmi >= 0 {
				st.bc = majorityBC
				st.psmi = majorityPsmi

				st.input.setSyncState(syncStateFine)

				st.input.decode.reset()

				st.input.frame.reset()
			}
		} else if st.cfoWait == 0 {
			st.detectCFO()
		} else {
			// Decrease wait counter.
			st.cfoWait--
		}
	}

	// if we are still synchronized
	if st.input.syncState == syncStateFine {
		samperr := float32(0)
		angle := float32(0)
		sumXY := float32(0)
		sumX2 := float32(0)
		for i := 0; i < partitionsPerBand*PARTITION_WIDTH_FM; i += PARTITION_WIDTH_FM {
			st.adjustData(LB_START+i, LB_START+i+PARTITION_WIDTH_FM)
			st.adjustData(UB_END-i-PARTITION_WIDTH_FM, UB_END-i)

			samperr += phaseDiff(st.phases[LB_START+i][0], st.phases[LB_START+i+PARTITION_WIDTH_FM][0])
			samperr += phaseDiff(st.phases[UB_END-i-PARTITION_WIDTH_FM][0], st.phases[UB_END-i][0])
		}
		samperr = samperr / float32(partitionsPerBand*2) * FFT_FM / PARTITION_WIDTH_FM / (2 * float32(math.Pi))

		for i := 0; i < partitionsPerBand*PARTITION_WIDTH_FM+1; i += PARTITION_WIDTH_FM {
			x := float32(LB_START + i - (FFT_FM / 2))
			y := st.costasFreq[LB_START+i]
			angle += y
			sumXY += x * y
			sumX2 += x * x

			x = float32(UB_END - i - (FFT_FM / 2))
			y = st.costasFreq[UB_END-i]
			angle += y
			sumXY += x * y
			sumX2 += x * x
		}
		samperr -= (sumXY / sumX2) * FFT_FM / (2 * float32(math.Pi)) * ACQUIRE_SYMBOLS
		st.samperr = int(math.Round(float64(samperr)))

		angle /= float32(partitionsPerBand+1) * 2
		st.angle = angle
		for i := 0; i < partitionsPerBand*PARTITION_WIDTH_FM+1; i += PARTITION_WIDTH_FM {
			st.costasFreq[LB_START+i] -= angle
			st.costasFreq[UB_END-i] -= angle
		}

		// Calculate modulation error
		errorLB := float32(0)
		errorUB := float32(0)
		for n := 0; n < BLKSZ; n++ {
			for i := 0; i < partitionsPerBand*PARTITION_WIDTH_FM; i += PARTITION_WIDTH_FM {
				for j := 1; j < PARTITION_WIDTH_FM; j++ {
					c := st.buffer[LB_START+i+j][n]
					ideal := complex(idealsgn(real(c)), idealsgn(imag(c)))
					errorLB += Norm2(ideal - c)

					c = st.buffer[UB_END-i-PARTITION_WIDTH_FM+j][n]
					ideal = complex(idealsgn(real(c)), idealsgn(imag(c)))
					errorUB += Norm2(ideal - c)
				}
			}
		}

		st.errorLB += errorLB
		st.errorUB += errorUB

		// Display average MER for each sideband
		st.merCnt++
		if st.merCnt == 16 {
			signal := float32(2 * BLKSZ * (partitionsPerBand * PARTITION_DATA_CARRIERS) * st.merCnt)
			merDbLB := 10 * log10f(signal/st.errorLB)
			merDbUB := 10 * log10f(signal/st.errorUB)

			st.input.radio.reportMER(merDbLB, merDbUB)

			st.merCnt = 0
			st.errorLB = 0
			st.errorUB = 0
		}

		// Soft demod based on MER for each sideband
		merLB := 2.0 * BLKSZ * float32(partitionsPerBand*PARTITION_DATA_CARRIERS) / errorLB
		merUB := 2.0 * BLKSZ * float32(partitionsPerBand*PARTITION_DATA_CARRIERS) / errorUB
		multLB := clampf(merLB*10, 1, 127)
		multUB := clampf(merUB*10, 1, 127)

		bufferPM := make([]int8, PM_BLOCK_SIZE)
		bufferPX1 := make([]int8, P3_FRAME_LEN_MP3_MP11)
		bufferPX2 := make([]int8, P3_FRAME_LEN_MP3_MP11)
		outPM, outPX1, outPX2 := 0, 0, 0

		for n := 0; n < BLKSZ; n++ {
			var c complex64
			for i := LB_START; i < LB_START+(PM_PARTITIONS*PARTITION_WIDTH_FM); i += PARTITION_WIDTH_FM {
				for j := 1; j < PARTITION_WIDTH_FM; j++ {
					c = st.buffer[i+j][n]
					bufferPM[outPM] = demod(real(c), multLB)
					outPM++
					bufferPM[outPM] = demod(imag(c), multLB)
					outPM++
				}
			}
			for i := UB_END - (PM_PARTITIONS * PARTITION_WIDTH_FM); i < UB_END; i += PARTITION_WIDTH_FM {
				for j := 1; j < PARTITION_WIDTH_FM; j++ {
					c = st.buffer[i+j][n]
					bufferPM[outPM] = demod(real(c), multUB)
					outPM++
					bufferPM[outPM] = demod(imag(c), multUB)
					outPM++
				}
			}
			if compatibilityMode[st.psmi] == 2 {
				for j := 1; j < PARTITION_WIDTH_FM; j++ {
					c = st.buffer[LB_START+(PM_PARTITIONS*PARTITION_WIDTH_FM)+j][n]
					bufferPX1[outPX1] = demod(real(c), multLB)
					outPX1++
					bufferPX1[outPX1] = demod(imag(c), multLB)
					outPX1++
				}
				for j := 1; j < PARTITION_WIDTH_FM; j++ {
					c = st.buffer[UB_END-(PM_PARTITIONS+1)*PARTITION_WIDTH_FM+j][n]
					bufferPX1[outPX1] = demod(real(c), multUB)
					outPX1++
					bufferPX1[outPX1] = demod(imag(c), multUB)
					outPX1++
				}
			}
			if compatibilityMode[st.psmi] == 3 || compatibilityMode[st.psmi] == 11 {
				for i := LB_START + (PM_PARTITIONS * PARTITION_WIDTH_FM); i < LB_START+(PM_PARTITIONS+2)*PARTITION_WIDTH_FM; i += PARTITION_WIDTH_FM {
					for j := 1; j < PARTITION_WIDTH_FM; j++ {
						c = st.buffer[i+j][n]
						bufferPX1[outPX1] = demod(real(c), multLB)
						outPX1++
						bufferPX1[outPX1] = demod(imag(c), multLB)
						outPX1++
					}
				}
				for i := UB_END - (PM_PARTITIONS+2)*PARTITION_WIDTH_FM; i < UB_END-(PM_PARTITIONS*PARTITION_WIDTH_FM); i += PARTITION_WIDTH_FM {
					for j := 1; j < PARTITION_WIDTH_FM; j++ {
						c = st.buffer[i+j][n]
						bufferPX1[outPX1] = demod(real(c), multUB)
						outPX1++
						bufferPX1[outPX1] = demod(imag(c), multUB)
						outPX1++
					}
				}
			}
			if compatibilityMode[st.psmi] == 11 {
				for i := LB_START + (PM_PARTITIONS+2)*PARTITION_WIDTH_FM; i < LB_START+(PM_PARTITIONS+4)*PARTITION_WIDTH_FM; i += PARTITION_WIDTH_FM {
					for j := 1; j < PARTITION_WIDTH_FM; j++ {
						c = st.buffer[i+j][n]
						bufferPX2[outPX2] = demod(real(c), multLB)
						outPX2++
						bufferPX2[outPX2] = demod(imag(c), multLB)
						outPX2++
					}
				}
				for i := UB_END - (PM_PARTITIONS+4)*PARTITION_WIDTH_FM; i < UB_END-(PM_PARTITIONS+2)*PARTITION_WIDTH_FM; i += PARTITION_WIDTH_FM {
					for j := 1; j < PARTITION_WIDTH_FM; j++ {
						c = st.buffer[i+j][n]
						bufferPX2[outPX2] = demod(real(c), multUB)
						outPX2++
						bufferPX2[outPX2] = demod(imag(c), multUB)
						outPX2++
					}
				}
			}
		}

		st.input.decode.pushPM(bufferPM, uint(st.bc))
		if outPX1 > 0 {
			st.input.decode.pushPX1(bufferPX1[:outPX1], uint(outPX1), uint(st.bc))
		}
		if outPX2 > 0 {
			st.input.decode.pushPX2(bufferPX2[:outPX2], uint(outPX2), uint(st.bc))
		}

		st.bc = (st.bc + 1) % 16
	}
}

func idealsgn(x float32) float32 {
	if x >= 0 {
		return 1
	}
	return -1
}

func log10f(x float32) float32 {
	return float32(math.Log10(float64(x)))
}

func clampf(x, lo, hi float32) float32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func (st *sync) processAM() {
	for i := REF_INDEX_AM; i <= MAX_INDEX_AM; i++ {
		for n := 0; n < BLKSZ; n++ {
			st.buffer[CENTER_AM-i][n] = -cmplxConj(st.buffer[CENTER_AM-i][n])
		}
	}

	if st.psmi != SERVICE_MODE_MA3 {
		for i := REF_INDEX_AM; i <= PIDS_OUTER_INDEX_AM; i++ {
			for n := 0; n < BLKSZ; n++ {
				st.buffer[CENTER_AM+i][n] += st.buffer[CENTER_AM-i][n]
			}
		}
	}

	if st.input.syncState == syncStateCoarse && st.cfoWait == 0 {
		offset := st.findRefAM(CENTER_AM + REF_INDEX_AM)
		if offset > 0 {
			st.input.acq.setKeepExtra(((BLKSZ - offset) % BLKSZ) * FFTCP_AM)
			st.cfoWait = 8
		}
	} else {
		st.cfoWait--
	}

	if st.input.syncState == syncStateCoarse {
		bc := st.findBlockAM(CENTER_AM + REF_INDEX_AM)

		if bc == -1 {
			st.offsetHistory = 0
		} else {
			st.offsetHistory = (st.offsetHistory << 4) | uint32(bc)
		}

		if st.offsetHistory&0xffff == 0x5670 {
			st.bc = 0
			st.input.setSyncState(syncStateFine)
			st.input.decode.reset()
			st.input.frame.reset()
			st.offsetHistory = 0
		}
	}

	if st.input.syncState == syncStateFine {
		pids1Index := PIDS_INNER_INDEX_AM
		if st.psmi == SERVICE_MODE_MA3 {
			pids1Index = -PIDS_INNER_INDEX_AM
		}
		pids2Index := PIDS_OUTER_INDEX_AM
		if st.psmi == SERVICE_MODE_MA3 {
			pids2Index = PIDS_INNER_INDEX_AM
		}

		pids1Mult := complex(float32(2*1.5), float32(2*-0.5)) /
			(st.buffer[CENTER_AM+pids1Index][8] + st.buffer[CENTER_AM+pids1Index][24])
		pids2Mult := complex(float32(2*1.5), float32(2*-0.5)) /
			(st.buffer[CENTER_AM+pids2Index][8] + st.buffer[CENTER_AM+pids2Index][24])
		pids := make([]byte, 2*BLKSZ)
		pidsOut := 0

		for n := 0; n < BLKSZ; n++ {
			st.buffer[CENTER_AM+pids1Index][n] *= pids1Mult
			pids[pidsOut] = qam16(st.buffer[CENTER_AM+pids1Index][n])
			pidsOut++

			st.buffer[CENTER_AM+pids2Index][n] *= pids2Mult
			pids[pidsOut] = qam16(st.buffer[CENTER_AM+pids2Index][n])
			pidsOut++
		}

		st.input.decode.processPIDSAM(pids)

		var plMult [PARTITION_WIDTH_AM]complex64
		var puMult [PARTITION_WIDTH_AM]complex64
		var sMult [PARTITION_WIDTH_AM]complex64
		var tMult [PARTITION_WIDTH_AM]complex64

		primaryIndex := OUTER_PARTITION_START_AM
		if st.psmi == SERVICE_MODE_MA3 {
			primaryIndex = INNER_PARTITION_START_AM
		}
		secondaryIndex := MIDDLE_PARTITION_START_AM
		tertiaryIndex := INNER_PARTITION_START_AM
		if st.psmi == SERVICE_MODE_MA3 {
			tertiaryIndex = MIDDLE_PARTITION_START_AM
		}

		samperr := float32(0)
		for col := 0; col < PARTITION_WIDTH_AM; col++ {
			train1 := (5 + 11*col) % 32
			train2 := (21 + 11*col) % 32

			plMult[col] = complex(float32(2*2.5), float32(2*-2.5)) /
				(st.buffer[CENTER_AM-primaryIndex-col][train1] + st.buffer[CENTER_AM-primaryIndex-col][train2])
			puMult[col] = complex(float32(2*2.5), float32(2*-2.5)) /
				(st.buffer[CENTER_AM+primaryIndex+col][train1] + st.buffer[CENTER_AM+primaryIndex+col][train2])
			if st.psmi != SERVICE_MODE_MA3 {
				sMult[col] = complex(float32(2*1.5), float32(2*-0.5)) /
					(st.buffer[CENTER_AM+secondaryIndex+col][train1] + st.buffer[CENTER_AM+secondaryIndex+col][train2])
				tMult[col] = complex(float32(2*-0.5), float32(2*0.5)) /
					(st.buffer[CENTER_AM+tertiaryIndex+col][train1] + st.buffer[CENTER_AM+tertiaryIndex+col][train2])
			} else {
				sMult[col] = complex(float32(2*2.5), float32(2*-2.5)) /
					(st.buffer[CENTER_AM+secondaryIndex+col][train1] + st.buffer[CENTER_AM+secondaryIndex+col][train2])
				tMult[col] = complex(float32(2*2.5), float32(2*-2.5)) /
					(st.buffer[CENTER_AM-tertiaryIndex-col][train1] + st.buffer[CENTER_AM-tertiaryIndex-col][train2])
			}

			if col > 0 {
				samperr += phaseDiff(cargs(plMult[col]), cargs(plMult[col-1]))
				samperr += phaseDiff(cargs(puMult[col]), cargs(puMult[col-1]))
			}
		}
		samperr = samperr / float32(2*(PARTITION_WIDTH_AM-1)) * FFT_AM / (2 * float32(math.Pi))
		st.samperr = int(math.Round(float64(samperr)))

		pl := make([]byte, BLKSZ*PARTITION_WIDTH_AM)
		pu := make([]byte, BLKSZ*PARTITION_WIDTH_AM)
		s := make([]byte, BLKSZ*PARTITION_WIDTH_AM)
		t := make([]byte, BLKSZ*PARTITION_WIDTH_AM)

		for n := 0; n < BLKSZ; n++ {
			for col := 0; col < PARTITION_WIDTH_AM; col++ {
				st.buffer[CENTER_AM-primaryIndex-col][n] *= plMult[col]
				st.buffer[CENTER_AM+primaryIndex+col][n] *= puMult[col]
				st.buffer[CENTER_AM+secondaryIndex+col][n] *= sMult[col]
				if st.psmi != SERVICE_MODE_MA3 {
					st.buffer[CENTER_AM+tertiaryIndex+col][n] *= tMult[col]
				} else {
					st.buffer[CENTER_AM-tertiaryIndex-col][n] *= tMult[col]
				}

				if st.psmi != SERVICE_MODE_MA3 {
					pl[n*PARTITION_WIDTH_AM+col] = qam64(st.buffer[CENTER_AM-primaryIndex-col][n])
					pu[n*PARTITION_WIDTH_AM+col] = qam64(st.buffer[CENTER_AM+primaryIndex+col][n])
					s[n*PARTITION_WIDTH_AM+col] = qam16(st.buffer[CENTER_AM+secondaryIndex+col][n])
					t[n*PARTITION_WIDTH_AM+col] = qpsk(st.buffer[CENTER_AM+tertiaryIndex+col][n])
				} else {
					pl[n*PARTITION_WIDTH_AM+col] = qam64(st.buffer[CENTER_AM-primaryIndex-col][n])
					pu[n*PARTITION_WIDTH_AM+col] = qam64(st.buffer[CENTER_AM+primaryIndex+col][n])
					s[n*PARTITION_WIDTH_AM+col] = qam64(st.buffer[CENTER_AM+secondaryIndex+col][n])
					t[n*PARTITION_WIDTH_AM+col] = qam64(st.buffer[CENTER_AM-tertiaryIndex-col][n])
				}
			}
		}

		st.input.decode.pushPLPUT(pl, pu, s, t, uint(st.bc))

		st.bc = (st.bc + 1) % 8
	}
}

func (st *sync) adjust(sampleAdj int) {
	for i := 0; i < maxPartitions*PARTITION_WIDTH_FM+1; i++ {
		st.costasPhase[LB_START+i] -= float32(sampleAdj) * float32(LB_START+i-(FFT_FM/2)) * 2 * float32(math.Pi) / FFT_FM
		st.costasPhase[UB_END-i] -= float32(sampleAdj) * float32(UB_END-i-(FFT_FM/2)) * 2 * float32(math.Pi) / FFT_FM
	}
}

func (st *sync) push(fftout []complex64) {
	if st.input.radio.mode == ModeFM {
		for i := 0; i < maxPartitions*PARTITION_WIDTH_FM+1; i++ {
			st.buffer[LB_START+i][st.idx] = fftout[LB_START+i]
			st.buffer[UB_END-i][st.idx] = fftout[UB_END-i]
		}
	} else {
		for i := CENTER_AM - MAX_INDEX_AM; i <= CENTER_AM+MAX_INDEX_AM; i++ {
			st.buffer[i][st.idx] = fftout[i]
		}
	}

	st.idx++
	if st.idx == BLKSZ {
		st.idx = 0

		if st.input.radio.mode == ModeFM {
			st.processFM()
		} else {
			st.processAM()
		}
	}
}

func (st *sync) reset() {
	for i := 0; i < FFT_FM; i++ {
		st.costasFreq[i] = 0
		st.costasPhase[i] = 0
	}

	st.idx = 0
	st.psmi = 1
	st.pli = -1
	st.hppi = -1
	st.aabi = -1
	st.rdbi = -1
	st.cfoWait = 0
	st.offsetHistory = 0
	st.merCnt = 0
	st.errorLB = 0
	st.errorUB = 0
}

func newSync(input *input) *sync {
	const loopBW = 0.05
	const damping = 0.70710678
	denom := float32(1 + (2 * damping * loopBW) + (loopBW * loopBW))
	st := &sync{
		input: input,
		alpha: float32(4*damping*loopBW) / denom,
		beta:  float32(4*loopBW*loopBW) / denom,
	}
	st.reset()
	return st
}

// sync is ported from sync_t in sync.h.
type sync struct {
	input *input
	// buffer[subcarrier][symbol]
	buffer        [FFT_FM][BLKSZ]complex64
	phases        [FFT_FM][BLKSZ]float32
	idx           uint
	psmi          int
	pli           int
	hppi          int
	aabi          int
	rdbi          int
	cfoWait       int
	bc            int
	offsetHistory uint32
	samperr       int
	angle         float32

	alpha       float32
	beta        float32
	costasFreq  [FFT_FM]float32
	costasPhase [FFT_FM]float32

	merCnt  int
	errorLB float32
	errorUB float32
}
