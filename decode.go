package nrsc5

// L1 FEC pipeline, ported from decode.c.

const pmVSize = 20

// 1012s.pdf figure 10-4
var (
	blDelay = [3]int{2, 1, 5}
	mlDelay = [3]int{11, 6, 7}
	buDelay = [3]int{10, 8, 9}
	muDelay = [3]int{4, 3, 0}
	elDelay = [2]int{0, 1}
	euDelay = [4]int{2, 3, 5, 4}
)

var pmV = [pmVSize]int8{
	10, 2, 18, 6, 14, 8, 16, 0, 12, 4,
	11, 3, 19, 7, 15, 9, 17, 1, 13, 5,
}

// 1012s.pdf figure 10-5
var (
	pidsIlDelay = [12]int{0, 1, 12, 13, 6, 5, 18, 17, 11, 7, 23, 19}
	pidsIuDelay = [12]int{2, 4, 14, 16, 3, 8, 15, 20, 9, 10, 21, 22}
)

// decode implements the L1 FEC pipeline (ported from decode.c).
type decode struct {
	input *input

	bufferPM  [PM_BLOCK_SIZE * 16]int8
	idxPM     uint
	startedPM bool

	bufferPU        [PARTITION_WIDTH_AM * BLKSZ * 8]byte
	bufferPL        [PARTITION_WIDTH_AM * BLKSZ * 8]byte
	bufferS         [PARTITION_WIDTH_AM * BLKSZ * 8]byte
	bufferT         [PARTITION_WIDTH_AM * BLKSZ * 8]byte
	amErrors        uint
	amDiversityWait uint

	bl  []byte
	bu  []byte
	ml  []byte
	mu  []byte
	el  []byte
	eu  []byte
	ebl []byte
	ebu []byte
	eml []byte
	emu []byte

	p1AM []byte
	p3AM []byte

	viterbiP1     []int8
	scramblerP1   []byte
	viterbiPIDS   []int8
	scramblerPIDS []byte

	interleaverPX1 interleaverIV
	interleaverPX2 interleaverIV

	viterbiP3   []int8
	viterbiP4   []int8
	scramblerP3 []byte
	scramblerP4 []byte

	viterbiP1AM   []int8
	scramblerP1AM []byte
	viterbiP3AM   []int8
	scramblerP3AM []byte

	pids *pids
}

type interleaverIV struct {
	buffer   []int8
	internal []int8
	i        uint
	pt       [4]uint
	ready    bool
	started  bool
}

const diversityDelayAM = 18000 * 3

func newDecode(in *input) *decode {
	st := &decode{
		input: in,
		pids:  newPids(in),
	}
	st.bl = make([]byte, 18000)
	st.bu = make([]byte, 18000)
	st.ml = make([]byte, 18000+diversityDelayAM)
	st.mu = make([]byte, 18000+diversityDelayAM)
	st.el = make([]byte, 12000)
	st.eu = make([]byte, 24000)
	st.ebl = make([]byte, 18000)
	st.ebu = make([]byte, 18000)
	st.eml = make([]byte, 18000+diversityDelayAM)
	st.emu = make([]byte, 18000+diversityDelayAM)

	st.p1AM = make([]byte, 8*P1_FRAME_LEN_ENCODED_AM)
	st.p3AM = make([]byte, P3_FRAME_LEN_ENCODED_MA3)

	st.viterbiP1 = make([]int8, P1_FRAME_LEN_FM*3)
	st.scramblerP1 = make([]byte, P1_FRAME_LEN_FM)
	st.viterbiPIDS = make([]int8, PIDS_FRAME_LEN*3)
	st.scramblerPIDS = make([]byte, PIDS_FRAME_LEN)
	st.interleaverPX1.buffer = make([]int8, P3_FRAME_LEN_MP3_MP11*2)
	st.interleaverPX1.internal = make([]int8, P3_FRAME_LEN_MP3_MP11*32)
	st.interleaverPX2.buffer = make([]int8, P3_FRAME_LEN_MP3_MP11*2)
	st.interleaverPX2.internal = make([]int8, P3_FRAME_LEN_MP3_MP11*32)
	st.viterbiP3 = make([]int8, P3_FRAME_LEN_MP3_MP11*3)
	st.viterbiP4 = make([]int8, P3_FRAME_LEN_MP3_MP11*3)
	st.scramblerP3 = make([]byte, P3_FRAME_LEN_MP3_MP11)
	st.scramblerP4 = make([]byte, P3_FRAME_LEN_MP3_MP11)
	st.viterbiP1AM = make([]int8, 8*P1_FRAME_LEN_AM*3)
	st.scramblerP1AM = make([]byte, P1_FRAME_LEN_AM)
	st.viterbiP3AM = make([]int8, P3_FRAME_LEN_MA3*3)
	st.scramblerP3AM = make([]byte, P3_FRAME_LEN_MA3)
	return st
}

func (st *decode) reset() {
	st.startedPM = false
	st.amErrors = 0
	st.amDiversityWait = 4
	st.interleaverIVReset(&st.interleaverPX1)
	st.interleaverIVReset(&st.interleaverPX2)
	st.pids.init(st.input)
}

func (st *decode) interleaverIVReset(interleaver *interleaverIV) {
	interleaver.i = 0
	interleaver.pt = [4]uint{}
	interleaver.started = false
	interleaver.ready = false
}

// bitMap implements the AM bit mapping (decode.c bit_map).
func bitMap(matrix []byte, b, k, p int) int {
	col := (9 * k) % 25
	row := (11*col + 16*(k/25) + 11*(k/50)) % 32
	return int(matrix[PARTITION_WIDTH_AM*(b*BLKSZ+row)+col]>>p) & 1
}

func (st *decode) interleaverMA1() {
	for n := 0; n < 18000; n++ {
		b := n / 2250
		k := (n + n/750 + 1) % 750
		p := n % 3
		st.bl[n] = byte(bitMap(st.bufferPL[:], b, k, p))

		b = (3*n + 3) % 8
		k = (n + n/3000 + 3) % 750
		p = 3 + (n % 3)
		st.ml[diversityDelayAM+n] = byte(bitMap(st.bufferPL[:], b, k, p))

		b = n / 2250
		k = (n + n/750) % 750
		p = n % 3
		st.bu[n] = byte(bitMap(st.bufferPU[:], b, k, p))

		b = (3 * n) % 8
		k = (n + n/3000 + 2) % 750
		p = 3 + (n % 3)
		st.mu[diversityDelayAM+n] = byte(bitMap(st.bufferPU[:], b, k, p))
	}

	if st.input.sync.psmi != SERVICE_MODE_MA3 {
		for n := 0; n < 12000; n++ {
			b := (3*n + n/3000) % 8
			k := (n + (n / 6000)) % 750
			p := n % 2
			st.el[n] = byte(bitMap(st.bufferT[:], b, k, p))
		}
		for n := 0; n < 24000; n++ {
			b := (3*n + n/3000 + 2*(n/12000)) % 8
			k := (n + (n / 6000)) % 750
			p := n % 4
			st.eu[n] = byte(bitMap(st.bufferS[:], b, k, p))
		}
	} else {
		for n := 0; n < 18000; n++ {
			b := (3*n + 3) % 8
			k := (n + n/3000 + 3) % 750
			p := n % 3
			st.ebl[n] = byte(bitMap(st.bufferT[:], b, k, p))

			b = (3*n + 3) % 8
			k = (n + n/3000 + 3) % 750
			p = 3 + (n % 3)
			st.eml[diversityDelayAM+n] = byte(bitMap(st.bufferT[:], b, k, p))

			b = (3 * n) % 8
			k = (n + n/3000 + 2) % 750
			p = n % 3
			st.ebu[n] = byte(bitMap(st.bufferS[:], b, k, p))

			b = (3 * n) % 8
			k = (n + n/3000 + 2) % 750
			p = 3 + (n % 3)
			st.emu[diversityDelayAM+n] = byte(bitMap(st.bufferS[:], b, k, p))
		}
	}

	for i := 0; i < 6000; i++ {
		for j := 0; j < 3; j++ {
			st.p1AM[i*12+blDelay[j]] = st.bl[i*3+j]
			st.p1AM[i*12+mlDelay[j]] = st.ml[i*3+j]
			st.p1AM[i*12+buDelay[j]] = st.bu[i*3+j]
			st.p1AM[i*12+muDelay[j]] = st.mu[i*3+j]
		}
		if st.input.sync.psmi != SERVICE_MODE_MA3 {
			for j := 0; j < 2; j++ {
				st.p3AM[i*6+elDelay[j]] = st.el[i*2+j]
			}
			for j := 0; j < 4; j++ {
				st.p3AM[i*6+euDelay[j]] = st.eu[i*4+j]
			}
		} else {
			for j := 0; j < 3; j++ {
				st.p3AM[i*12+blDelay[j]] = st.ebl[i*3+j]
				st.p3AM[i*12+mlDelay[j]] = st.eml[i*3+j]
				st.p3AM[i*12+buDelay[j]] = st.ebu[i*3+j]
				st.p3AM[i*12+muDelay[j]] = st.emu[i*3+j]
			}
		}
	}

	copy(st.ml, st.ml[18000:])
	copy(st.mu, st.mu[18000:])
	if st.input.sync.psmi == SERVICE_MODE_MA3 {
		copy(st.eml, st.eml[18000:])
		copy(st.emu, st.emu[18000:])
	}

	offset := 0
	for i := 0; i < 8*P1_FRAME_LEN_AM*3; i++ {
		switch i % 15 {
		case 1, 4, 7:
			st.viterbiP1AM[i] = 0
		default:
			if st.p1AM[offset] != 0 {
				st.viterbiP1AM[i] = 1
			} else {
				st.viterbiP1AM[i] = -1
			}
			offset++
		}
	}

	offset = 0
	if st.input.sync.psmi != SERVICE_MODE_MA3 {
		for i := 0; i < P3_FRAME_LEN_MA1*3; i++ {
			switch i % 6 {
			case 1, 4, 5:
				st.viterbiP3AM[i] = 0
			default:
				if st.p3AM[offset] != 0 {
					st.viterbiP3AM[i] = 1
				} else {
					st.viterbiP3AM[i] = -1
				}
				offset++
			}
		}
	} else {
		for i := 0; i < P3_FRAME_LEN_MA3*3; i++ {
			switch i % 15 {
			case 1, 4, 7:
				st.viterbiP3AM[i] = 0
			default:
				if st.p3AM[offset] != 0 {
					st.viterbiP3AM[i] = 1
				} else {
					st.viterbiP3AM[i] = -1
				}
				offset++
			}
		}
	}
}

// bitErrors calculates the number of bit errors by re-encoding and
// comparing to the input.
func bitErrors(coded []int8, decoded []byte, k int, frameLen uint, gens [4]uint, puncture []uint8, punctureLen int) int {
	var r uint16
	errors := 0

	// tail biting
	for i := uint(0); i < uint(k-1); i++ {
		r = (r >> 1) | (uint16(decoded[frameLen-uint(k-1)+i]) << (k - 1))
	}

	for i, j := uint(0), uint(0); i < frameLen; i, j = i+1, j+3 {
		// shift in new bit
		r = (r >> 1) | (uint16(decoded[i]) << (k - 1))

		ru := uint(r)
		if puncture[j%uint(punctureLen)] != 0 && ((coded[j] > 0) != (parity(ru&gens[0]) == 1)) {
			errors++
		}
		if puncture[(j+1)%uint(punctureLen)] != 0 && ((coded[j+1] > 0) != (parity(ru&gens[1]) == 1)) {
			errors++
		}
		if puncture[(j+2)%uint(punctureLen)] != 0 && ((coded[j+2] > 0) != (parity(ru&gens[2]) == 1)) {
			errors++
		}
	}

	return errors
}

func bitErrors25FM(coded []int8, decoded []byte, length int) int {
	puncture := []uint8{1, 1, 1, 1, 1, 0}
	return bitErrors(coded, decoded, 7, uint(length), [4]uint{0133, 0171, 0165}, puncture, 6)
}

func bitErrorsE1(coded []int8, decoded []byte, length int) int {
	puncture := []uint8{1, 0, 1, 1, 0, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1}
	return bitErrors(coded, decoded, 9, uint(length), [4]uint{0561, 0657, 0711}, puncture, 15)
}

func bitErrorsE2(coded []int8, decoded []byte, length int) int {
	puncture := []uint8{1, 0, 1, 1, 0, 0}
	return bitErrors(coded, decoded, 9, uint(length), [4]uint{0561, 0753, 0711}, puncture, 6)
}

func descramble(buf []byte, length uint) {
	val := uint32(0x3ff)
	for i := uint(0); i < length; i += 8 {
		for j := 0; j < 8; j++ {
			bit := ((val >> 9) ^ val) & 1
			val |= bit << 11
			val >>= 1
			buf[i+uint(j)] ^= byte(bit)
		}
	}
}

// interleaverI is 1012s.pdf interleaver type I (PM P1 frames).
func interleaverI(in []int8, viterbi []int8, J, B, C, M int, V []int8, lengthV uint, N uint) {
	out := 0
	for i := uint(0); i < N; i++ {
		partition := int(V[((i+uint(2*(M/4)))/uint(M))%lengthV])
		var block uint
		if M == 1 {
			block = ((i / uint(J)) + uint(partition*7)) % uint(B)
		} else {
			block = (i + (i / (uint(J) * uint(B)))) % uint(B)
		}
		k := i / (uint(J) * uint(B))
		row := (k * 11) % 32
		column := (k*11 + k/(32*9)) % uint(C)
		viterbi[out] = in[(block*32+row)*uint(J*C)+uint(partition)*uint(C)+column]
		out++
		if out%6 == 5 { // depuncture, [1, 1, 1, 1, 1, 0]
			viterbi[out] = 0
			out++
		}
	}
}

// interleaverII is 1012s.pdf interleaver type II (PM PIDS frames).
func interleaverII(in []int8, viterbi []int8, bc uint, J, B, C int, V []int8, lengthV uint, b int, I0 int) {
	out := 0
	for i := uint(int(bc) * b); i < uint((int(bc)+1)*b); i++ {
		partition := int(V[i%lengthV])
		block := i / uint(b)
		k := uint(((int(i) / J) % (b / J)) + (I0 / (J * B)))
		row := (k * 11) % 32
		column := (k*11 + k/(32*9)) % uint(C)
		viterbi[out] = in[(block*32+row)*uint(J*C)+uint(partition)*uint(C)+column]
		out++
		if out%6 == 5 { // depuncture, [1, 1, 1, 1, 1, 0]
			viterbi[out] = 0
			out++
		}
	}
}

// interleaverIV is 1012s.pdf interleaver type IV (PX1/PX2 P3 frames).
func (st *decode) interleaverIV(interleaver *interleaverIV, viterbi []int8, frameLen uint) {
	var J, B, C, M, N uint
	if frameLen == P3_FRAME_LEN_MP3_MP11 {
		J, B, C, M, N = 4, 32, 36, 2, 147456
	} else {
		J, B, C, M, N = 2, 32, 36, 4, 73728
	}
	bkBits := uint(32 * C)
	bkAdj := uint(32*C - 1)

	if interleaver.i == N {
		interleaver.i = 0
		interleaver.pt = [4]uint{}
		interleaver.ready = true
	}

	out := 0
	for i := uint(0); i < frameLen*2; i++ {
		partition := ((interleaver.i + 2*(M/4)) / M) % J
		pti := interleaver.pt[partition]
		interleaver.pt[partition]++
		block := (pti + (partition * 7) - (bkAdj * (pti / bkBits))) % B
		row := ((11 * pti) % bkBits) / C
		column := (pti * 11) % C
		viterbi[out] = interleaver.internal[(block*32+row)*(J*C)+partition*C+column]
		out++
		if out%6 == 1 || out%6 == 4 { // depuncture, [1, 0, 1, 1, 0, 1]
			viterbi[out] = 0
			out++
		}

		interleaver.internal[interleaver.i] = interleaver.buffer[i]
		interleaver.i++
	}
}

func (st *decode) pushPM(sbit []int8, bc uint) {
	copy(st.bufferPM[PM_BLOCK_SIZE*bc:], sbit[:PM_BLOCK_SIZE])
	st.processPIDS(bc)

	if bc == 0 {
		st.startedPM = true
	}

	if st.startedPM && bc == 15 {
		st.processP1()
	}
}

func (st *decode) processP1() {
	const J = 20
	const B = 16
	const C = 36
	const M = 1
	interleaverI(st.bufferPM[:], st.viterbiP1, J, B, C, M, pmV[:], pmVSize, P1_FRAME_LEN_ENCODED_FM)

	ConvDecodeP1(st.viterbiP1, st.scramblerP1)
	st.input.radio.reportBER(float32(bitErrors25FM(st.viterbiP1, st.scramblerP1, P1_FRAME_LEN_FM)) / P1_FRAME_LEN_ENCODED_FM)
	descramble(st.scramblerP1, P1_FRAME_LEN_FM)
	st.input.frame.push(st.scramblerP1, P1_FRAME_LEN_FM, P1LogicalChannel)
}

func (st *decode) processPIDS(bc uint) {
	const J = 20
	const B = 16
	const C = 36
	interleaverII(st.bufferPM[:], st.viterbiPIDS, bc, J, B, C, pmV[:], pmVSize, PIDS_FRAME_LEN_ENCODED_FM, P1_FRAME_LEN_ENCODED_FM)

	ConvDecodePIDS(st.viterbiPIDS, st.scramblerPIDS)
	descramble(st.scramblerPIDS, PIDS_FRAME_LEN)
	st.pids.framePush(st.scramblerPIDS)
}

func (st *decode) processPIDSAM(sbit []byte) {
	il := make([]byte, 120)
	iu := make([]byte, 120)

	// 1012s.pdf section 10.4
	for n := 0; n < 120; n++ {
		p := n % 4

		k := (n + (n / 60) + 11) % 30
		row := (11*(k+(k/15)) + 3) % 32
		il[n] = (sbit[row*2] >> p) & 1

		k = (n + (n / 60)) % 30
		row = (11*(k+(k/15)) + 3) % 32
		iu[n] = (sbit[row*2+1] >> p) & 1
	}

	// 1012s.pdf figure 10-5
	pids1Disabled := st.input.sync.psmi == 1 && st.input.sync.rdbi != 0
	for i := 0; i < 10; i++ {
		for j := 0; j < 12; j++ {
			if pids1Disabled {
				st.viterbiPIDS[i*24+pidsIlDelay[j]] = 0
			} else if il[i*12+j] != 0 {
				st.viterbiPIDS[i*24+pidsIlDelay[j]] = 1
			} else {
				st.viterbiPIDS[i*24+pidsIlDelay[j]] = -1
			}
			if iu[i*12+j] != 0 {
				st.viterbiPIDS[i*24+pidsIuDelay[j]] = 1
			} else {
				st.viterbiPIDS[i*24+pidsIuDelay[j]] = -1
			}
		}
	}

	ConvDecodeE2E3(st.viterbiPIDS, st.scramblerPIDS, PIDS_FRAME_LEN)
	descramble(st.scramblerPIDS, PIDS_FRAME_LEN)
	st.pids.framePush(st.scramblerPIDS)
}

func (st *decode) pushPX1(sbit []int8, length uint, bc uint) {
	if bc%2 == 0 {
		st.interleaverPX1.started = true
	}

	if st.interleaverPX1.started {
		copy(st.interleaverPX1.buffer[length*(bc%2):], sbit[:length])

		if bc%2 == 1 {
			st.interleaverIV(&st.interleaverPX1, st.viterbiP3, length)

			if st.interleaverPX1.ready {
				ConvDecodeP3P4(st.viterbiP3, st.scramblerP3, int(length))
				descramble(st.scramblerP3, length)
				st.input.frame.push(st.scramblerP3, int(length), P3LogicalChannel)
			}
		}
	}
}

func (st *decode) pushPX2(sbit []int8, length uint, bc uint) {
	if bc%2 == 0 {
		st.interleaverPX2.started = true
	}

	if st.interleaverPX2.started {
		copy(st.interleaverPX2.buffer[length*(bc%2):], sbit[:length])

		if bc%2 == 1 {
			st.interleaverIV(&st.interleaverPX2, st.viterbiP4, length)

			if st.interleaverPX2.ready {
				ConvDecodeP3P4(st.viterbiP4, st.scramblerP4, int(length))
				descramble(st.scramblerP4, length)
				st.input.frame.push(st.scramblerP4, int(length), P4LogicalChannel)
			}
		}
	}
}

func (st *decode) pushPLPUT(symPL, symPU, symS, symT []byte, bc uint) {
	copy(st.bufferPL[bc*BLKSZ*PARTITION_WIDTH_AM:], symPL[:BLKSZ*PARTITION_WIDTH_AM])
	copy(st.bufferPU[bc*BLKSZ*PARTITION_WIDTH_AM:], symPU[:BLKSZ*PARTITION_WIDTH_AM])
	copy(st.bufferS[bc*BLKSZ*PARTITION_WIDTH_AM:], symS[:BLKSZ*PARTITION_WIDTH_AM])
	copy(st.bufferT[bc*BLKSZ*PARTITION_WIDTH_AM:], symT[:BLKSZ*PARTITION_WIDTH_AM])

	st.processP1P3AM(bc)
}

func (st *decode) processP1P3AM(bc uint) {
	if bc == 0 {
		st.amErrors = 0
	}

	if st.amDiversityWait == 0 {
		ConvDecodeE1(st.viterbiP1AM[bc*P1_FRAME_LEN_AM*3:], st.scramblerP1AM, P1_FRAME_LEN_AM)
		st.amErrors += uint(bitErrorsE1(st.viterbiP1AM[bc*P1_FRAME_LEN_AM*3:], st.scramblerP1AM, P1_FRAME_LEN_AM))
		descramble(st.scramblerP1AM, P1_FRAME_LEN_AM)
		st.input.frame.push(st.scramblerP1AM, P1_FRAME_LEN_AM, P1LogicalChannel)

		if bc == 7 {
			totalFrameLength := uint(8 * P1_FRAME_LEN_ENCODED_AM)

			if st.input.sync.rdbi == 0 {
				if st.input.sync.psmi != SERVICE_MODE_MA3 {
					totalFrameLength += P3_FRAME_LEN_ENCODED_MA1
					ConvDecodeE2E3(st.viterbiP3AM, st.scramblerP3AM, P3_FRAME_LEN_MA1)
					st.amErrors += uint(bitErrorsE2(st.viterbiP3AM, st.scramblerP3AM, P3_FRAME_LEN_MA1))
					descramble(st.scramblerP3AM, P3_FRAME_LEN_MA1)
					st.input.frame.push(st.scramblerP3AM, P3_FRAME_LEN_MA1, P3LogicalChannel)
				} else {
					totalFrameLength += P3_FRAME_LEN_ENCODED_MA3
					ConvDecodeE1(st.viterbiP3AM, st.scramblerP3AM, P3_FRAME_LEN_MA3)
					st.amErrors += uint(bitErrorsE1(st.viterbiP3AM, st.scramblerP3AM, P3_FRAME_LEN_MA3))
					descramble(st.scramblerP3AM, P3_FRAME_LEN_MA3)
					st.input.frame.push(st.scramblerP3AM, P3_FRAME_LEN_MA3, P3LogicalChannel)
				}
			}

			st.input.radio.reportBER(float32(st.amErrors) / float32(totalFrameLength))
		}
	}

	if bc == 7 {
		st.interleaverMA1()

		if st.amDiversityWait > 0 {
			st.amDiversityWait--
		}
	}
}
