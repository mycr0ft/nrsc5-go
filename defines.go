// Package nrsc5 is a pure-Go implementation of the NRSC-5 (HD Radio) receiver.
package nrsc5

// FFT length in samples
const (
	FFT_FM = 2048
	FFT_AM = 256
)

// Cyclic preflex length in samples
const (
	CP_FM = 112
	CP_AM = 14
)

const (
	FFTCP_FM = FFT_FM + CP_FM
	FFTCP_AM = FFT_AM + CP_AM
)

// OFDM symbols per L1 block
const BLKSZ = 32

// Symbols processed by each invocation of acquire.Process
const ACQUIRE_SYMBOLS = BLKSZ

// Index of first lower sideband subcarrier
const LB_START = (FFT_FM / 2) - 546

// Index of last upper sideband subcarrier
const UB_END = (FFT_FM / 2) + 546

// Index of AM carrier
const CENTER_AM = FFT_AM / 2

// Indexes of AM subcarriers
const (
	REF_INDEX_AM              = 1
	PIDS_INNER_INDEX_AM       = 27
	PIDS_OUTER_INDEX_AM       = 53
	INNER_PARTITION_START_AM  = 2
	MIDDLE_PARTITION_START_AM = 28
	OUTER_PARTITION_START_AM  = 57
	MAX_INDEX_AM              = 81
)

// AM service modes
const (
	SERVICE_MODE_MA1 = 1
	SERVICE_MODE_MA3 = 2
)

// Bits per P1 frame
const (
	P1_FRAME_LEN_FM = 146176
	P1_FRAME_LEN_AM = 3750
)

// Bits per encoded P1 frame
const (
	P1_FRAME_LEN_ENCODED_FM = P1_FRAME_LEN_FM * 5 / 2
	P1_FRAME_LEN_ENCODED_AM = P1_FRAME_LEN_AM * 12 / 5
)

// Bits per PIDS frame
const PIDS_FRAME_LEN = 80

// Bits per encoded PIDS frame
const (
	PIDS_FRAME_LEN_ENCODED_FM = PIDS_FRAME_LEN * 5 / 2
	PIDS_FRAME_LEN_ENCODED_AM = PIDS_FRAME_LEN * 3
)

// Bits per P3 frame
const (
	P3_FRAME_LEN_MP2      = 2304
	P3_FRAME_LEN_MP3_MP11 = 4608
	P3_FRAME_LEN_MA1      = 24000
	P3_FRAME_LEN_MA3      = 30000
)

// Bits per encoded P3 frame
const (
	P3_FRAME_LEN_ENCODED_FM  = P3_FRAME_LEN_MP3_MP11 * 2
	P3_FRAME_LEN_ENCODED_MA1 = P3_FRAME_LEN_MA1 * 3 / 2
	P3_FRAME_LEN_ENCODED_MA3 = P3_FRAME_LEN_MA3 * 12 / 5
)

// Bits per L2 PCI
const PCI_LEN = 24

// Bytes per L2 PDU (max)
const MAX_PDU_LEN = (P1_FRAME_LEN_FM - PCI_LEN) / 8

// Bytes per L2 PDU in P1 frame (AM)
const P1_PDU_LEN_AM = 466

// Number of programs (max)
const MAX_PROGRAMS = 8

// Number of streams per program (max)
const MAX_STREAMS = 2

// Number of audio packets in the elastic buffer
const ELASTIC_BUFFER_LEN = 64

// Number of subcarriers per AM partition
const PARTITION_WIDTH_AM = 25

// Number of subcarriers per FM partition
const PARTITION_WIDTH_FM = 19

// Number of data carriers per FM partition
const PARTITION_DATA_CARRIERS = 18

// Number of partitions in each Primary Main (PM) sideband
const PM_PARTITIONS = 10

// Size of one block in the PM interleaver matrix
const PM_BLOCK_SIZE = 2 * 2 * PM_PARTITIONS * PARTITION_DATA_CARRIERS * BLKSZ

// Modes
const (
	ModeFM = 0
	ModeAM = 1
)

// Sample rates
const (
	SAMPLE_RATE_CU8     = 1488375
	SAMPLE_RATE_CS16_FM = 744188
	SAMPLE_RATE_CS16_AM = 46512
	SAMPLE_RATE_AUDIO   = 44100
)

// Logical channels
type LogicalChannel int

const (
	P1LogicalChannel LogicalChannel = iota
	P3LogicalChannel
	P4LogicalChannel
)

// NUM_LOGICAL_CHANNELS is the number of logical channels.
const NUM_LOGICAL_CHANNELS = 3

// Sig service/component types.
const (
	SigServiceNone  = 0
	SigServiceData  = 1
	SigServiceAudio = 2

	SigComponentNone  = 0
	SigComponentData  = 1
	SigComponentAudio = 2

	HereImageTraffic = 8
	HereImageWeather = 13

	HereTrafficTiles = 9

	MIMEHereImage = 0xB7F03DFC

	AASTypeStream = 0
	AASTypePacket = 1
	AASTypeLOT    = 3
)

// U8_F converts unsigned 8-bit IQ sample to float
func U8_F(x byte) float32 {
	return (float32(x) - 127) / 128
}

// U8_Q15 converts unsigned 8-bit IQ sample to Q15 fixed point
func U8_Q15(x byte) int16 {
	return (int16(x) - 127) * 64
}

// CInt16 is a complex number in Q15 fixed point
type CInt16 struct {
	R, I int16
}

func Cq15ToCf(c CInt16) complex64 {
	return complex(float32(c.R)/32767.0, float32(c.I)/32767.0)
}

func Cq15ToCfConj(c CInt16) complex64 {
	return complex(float32(c.R)/32767.0, float32(c.I)/-32767.0)
}

// Norm2 returns the squared magnitude of a complex value
func Norm2(v complex64) float32 {
	return real(v)*real(v) + imag(v)*imag(v)
}

// FFTShift swaps the two halves of a spectrum
func FFTShift(x []complex64) {
	h := len(x) / 2
	for i := 0; i < h; i++ {
		x[i], x[i+h] = x[i+h], x[i]
	}
}
