package nrsc5

import "log"

// L2/L3 frame processing, ported from frame.c.

const (
	maxAASLen       = 8212
	rsBlockLen      = 255
	rsCodewordLen   = 96
	maxAudioPackets = 64
)

const (
	pciAudio         = 0x38D8D3
	pciAudioOpp      = 0xCE3634
	pciAudioFixed    = 0xE3634C
	pciAudioFixedOpp = 0x8D8D33
	pciFixed         = 0x3634CE
	pciReservedCW5   = 0x8D338D
	pciReservedCW6   = 0xD8D338
	pciReservedCW7   = 0x634CE3
	pciMaxErrors     = 4
	pciCount         = 8
)

var pciPossibilities = [pciCount]uint{
	pciAudio,
	pciAudioOpp,
	pciAudioFixed,
	pciAudioFixedOpp,
	pciFixed,
	pciReservedCW5,
	pciReservedCW6,
	pciReservedCW7,
}

// Packet shape / flags.
const (
	PacketFlagNone     = 0
	PacketFlagCRCError = 1 << 0

	PacketNone      = 0
	PacketFull      = 1
	PacketHalfFront = 2
	PacketHalfBack  = 3
)

// frameHeader (frame_header_t).
type frameHeader struct {
	codecMode      uint
	streamID       uint
	pduSeq         uint
	blendControl   uint
	perStreamDelay uint
	commonDelay    uint
	latency        uint
	pfirst         uint
	plast          uint
	seq            uint
	nop            uint
	hef            uint
	laLocation     uint
}

type hef struct {
	classInd        uint
	progNum         uint
	pduLen          uint
	progType        uint
	access          uint
	appliedServices uint
	pduMarker       uint
}

var crc8Tab = [256]byte{
	0, 0x31, 0x62, 0x53, 0xC4, 0xF5, 0xA6, 0x97, 0xB9,
	0x88, 0xDB, 0xEA, 0x7D, 0x4C, 0x1F, 0x2E, 0x43, 0x72,
	0x21, 0x10, 0x87, 0xB6, 0xE5, 0xD4, 0xFA, 0xCB, 0x98,
	0xA9, 0x3E, 0xF, 0x5C, 0x6D, 0x86, 0xB7, 0xE4, 0xD5,
	0x42, 0x73, 0x20, 0x11, 0x3F, 0xE, 0x5D, 0x6C, 0xFB,
	0xCA, 0x99, 0xA8, 0xC5, 0xF4, 0xA7, 0x96, 1, 0x30,
	0x63, 0x52, 0x7C, 0x4D, 0x1E, 0x2F, 0xB8, 0x89, 0xDA,
	0xEB, 0x3D, 0xC, 0x5F, 0x6E, 0xF9, 0xC8, 0x9B, 0xAA,
	0x84, 0xB5, 0xE6, 0xD7, 0x40, 0x71, 0x22, 0x13, 0x7E,
	0x4F, 0x1C, 0x2D, 0xBA, 0x8B, 0xD8, 0xE9, 0xC7, 0xF6,
	0xA5, 0x94, 3, 0x32, 0x61, 0x50, 0xBB, 0x8A, 0xD9,
	0xE8, 0x7F, 0x4E, 0x1D, 0x2C, 2, 0x33, 0x60, 0x51,
	0xC6, 0xF7, 0xA4, 0x95, 0xF8, 0xC9, 0x9A, 0xAB, 0x3C,
	0xD, 0x5E, 0x6F, 0x41, 0x70, 0x23, 0x12, 0x85, 0xB4,
	0xE7, 0xD6, 0x7A, 0x4B, 0x18, 0x29, 0xBE, 0x8F, 0xDC,
	0xED, 0xC3, 0xF2, 0xA1, 0x90, 7, 0x36, 0x65, 0x54,
	0x39, 8, 0x5B, 0x6A, 0xFD, 0xCC, 0x9F, 0xAE, 0x80,
	0xB1, 0xE2, 0xD3, 0x44, 0x75, 0x26, 0x17, 0xFC, 0xCD,
	0x9E, 0xAF, 0x38, 9, 0x5A, 0x6B, 0x45, 0x74, 0x27,
	0x16, 0x81, 0xB0, 0xE3, 0xD2, 0xBF, 0x8E, 0xDD, 0xEC,
	0x7B, 0x4A, 0x19, 0x28, 6, 0x37, 0x64, 0x55, 0xC2,
	0xF3, 0xA0, 0x91, 0x47, 0x76, 0x25, 0x14, 0x83, 0xB2,
	0xE1, 0xD0, 0xFE, 0xCF, 0x9C, 0xAD, 0x3A, 0xB, 0x58,
	0x69, 4, 0x35, 0x66, 0x57, 0xC0, 0xF1, 0xA2, 0x93,
	0xBD, 0x8C, 0xDF, 0xEE, 0x79, 0x48, 0x1B, 0x2A, 0xC1,
	0xF0, 0xA3, 0x92, 5, 0x34, 0x67, 0x56, 0x78, 0x49,
	0x1A, 0x2B, 0xBC, 0x8D, 0xDE, 0xEF, 0x82, 0xB3, 0xE0,
	0xD1, 0x46, 0x77, 0x24, 0x15, 0x3B, 0xA, 0x59, 0x68,
	0xFF, 0xCE, 0x9D, 0xAC,
}

var fcsTab = [256]uint16{
	0x0000, 0x1189, 0x2312, 0x329b, 0x4624, 0x57ad, 0x6536, 0x74bf,
	0x8c48, 0x9dc1, 0xaf5a, 0xbed3, 0xca6c, 0xdbe5, 0xe97e, 0xf8f7,
	0x1081, 0x0108, 0x3393, 0x221a, 0x56a5, 0x472c, 0x75b7, 0x643e,
	0x9cc9, 0x8d40, 0xbfdb, 0xae52, 0xdaed, 0xcb64, 0xf9ff, 0xe876,
	0x2102, 0x308b, 0x0210, 0x1399, 0x6726, 0x76af, 0x4434, 0x55bd,
	0xad4a, 0xbcc3, 0x8e58, 0x9fd1, 0xeb6e, 0xfae7, 0xc87c, 0xd9f5,
	0x3183, 0x200a, 0x1291, 0x0318, 0x77a7, 0x662e, 0x54b5, 0x453c,
	0xbdcb, 0xac42, 0x9ed9, 0x8f50, 0xfbef, 0xea66, 0xd8fd, 0xc974,
	0x4204, 0x538d, 0x6116, 0x709f, 0x0420, 0x15a9, 0x2732, 0x36bb,
	0xce4c, 0xdfc5, 0xed5e, 0xfcd7, 0x8868, 0x99e1, 0xab7a, 0xbaf3,
	0x5285, 0x430c, 0x7197, 0x601e, 0x14a1, 0x0528, 0x37b3, 0x263a,
	0xdecd, 0xcf44, 0xfddf, 0xec56, 0x98e9, 0x8960, 0xbbfb, 0xaa72,
	0x6306, 0x728f, 0x4014, 0x519d, 0x2522, 0x34ab, 0x0630, 0x17b9,
	0xef4e, 0xfec7, 0xcc5c, 0xddd5, 0xa96a, 0xb8e3, 0x8a78, 0x9bf1,
	0x7387, 0x620e, 0x5095, 0x411c, 0x35a3, 0x242a, 0x16b1, 0x0738,
	0xffcf, 0xee46, 0xdcdd, 0xcd54, 0xb9eb, 0xa862, 0x9af9, 0x8b70,
	0x8408, 0x9581, 0xa71a, 0xb693, 0xc22c, 0xd3a5, 0xe13e, 0xf0b7,
	0x0840, 0x19c9, 0x2b52, 0x3adb, 0x4e64, 0x5fed, 0x6d76, 0x7cff,
	0x9489, 0x8500, 0xb79b, 0xa612, 0xd2ad, 0xc324, 0xf1bf, 0xe036,
	0x18c1, 0x0948, 0x3bd3, 0x2a5a, 0x5ee5, 0x4f6c, 0x7df7, 0x6c7e,
	0xa50a, 0xb483, 0x8618, 0x9791, 0xe32e, 0xf2a7, 0xc03c, 0xd1b5,
	0x2942, 0x38cb, 0x0a50, 0x1bd9, 0x6f66, 0x7eef, 0x4c74, 0x5dfd,
	0xb58b, 0xa402, 0x9699, 0x8710, 0xf3af, 0xe226, 0xd0bd, 0xc134,
	0x39c3, 0x284a, 0x1ad1, 0x0b58, 0x7fe7, 0x6e6e, 0x5cf5, 0x4d7c,
	0xc60c, 0xd785, 0xe51e, 0xf497, 0x8028, 0x91a1, 0xa33a, 0xb2b3,
	0x4a44, 0x5bcd, 0x6956, 0x78df, 0x0c60, 0x1de9, 0x2f72, 0x3efb,
	0xd68d, 0xc704, 0xf59f, 0xe416, 0x90a9, 0x8120, 0xb3bb, 0xa232,
	0x5ac5, 0x4b4c, 0x79d7, 0x685e, 0x1ce1, 0x0d68, 0x3ff3, 0x2e7a,
	0xe70e, 0xf687, 0xc41c, 0xd595, 0xa12a, 0xb0a3, 0x8238, 0x93b1,
	0x6b46, 0x7acf, 0x4854, 0x59dd, 0x2d62, 0x3ceb, 0x0e70, 0x1ff9,
	0xf78f, 0xe606, 0xd49d, 0xc514, 0xb1ab, 0xa022, 0x92b9, 0x8330,
	0x7bc7, 0x6a4e, 0x58d5, 0x495c, 0x3de3, 0x2c6a, 0x1ef1, 0x0f78,
}

// validFCS16 is the good final FCS value.
const validFCS16 = 0xf0b8

func crc8(pkt []byte, cnt int) byte {
	crc := byte(0xFF)
	for i := 0; i < cnt; i++ {
		crc = crc8Tab[crc^pkt[i]]
	}
	return crc
}

func fcs16(cp []byte, length int) uint16 {
	crc := uint16(0xFFFF)
	for i := 0; i < length; i++ {
		crc = (crc >> 8) ^ fcsTab[(crc^uint16(cp[i]))&0xFF]
	}
	return crc
}

func hasAudio(pci uint) bool {
	return pci == pciAudio || pci == pciAudioOpp || pci == pciAudioFixed || pci == pciAudioFixedOpp
}

func hasFixed(pci uint) bool {
	return pci == pciAudioFixed || pci == pciAudioFixedOpp || pci == pciFixed
}

func (f *frame) fixHeader(buf []byte) bool {
	hdr := make([]byte, rsBlockLen)
	for i := 0; i < rsCodewordLen; i++ {
		hdr[rsBlockLen-i-1] = buf[i]
	}

	corrections := decodeRs(f.rsDec, hdr, nil, 0)

	if corrections == -1 {
		return false
	}

	for i := 0; i < rsBlockLen-rsCodewordLen; i++ {
		if hdr[i] != 0 {
			return false
		}
	}

	for i := 0; i < rsCodewordLen; i++ {
		buf[i] = hdr[rsBlockLen-i-1]
	}
	return true
}

func parseHeader(buf []byte, hdr *frameHeader) {
	hdr.codecMode = uint(buf[8] & 0xf)
	hdr.streamID = uint((buf[8] >> 4) & 0x3)
	hdr.pduSeq = uint(buf[8]>>6) | (uint(buf[9]&1) << 2)
	hdr.blendControl = uint((buf[9] >> 1) & 0x3)
	hdr.perStreamDelay = uint(buf[9] >> 3)
	hdr.commonDelay = uint(buf[10] & 0x3f)
	hdr.latency = uint(buf[10]>>6) | (uint(buf[11]&1) << 2)
	hdr.pfirst = uint((buf[11] >> 1) & 1)
	hdr.plast = uint((buf[11] >> 2) & 1)
	hdr.seq = uint(buf[11]>>3) | (uint(buf[12]&1) << 5)
	hdr.nop = uint((buf[12] >> 1) & 0x3f)
	hdr.hef = uint(buf[12] >> 7)
	hdr.laLocation = uint(buf[13])
}

func parseHEF(buf []byte, length uint, hef *hef) uint {
	bytePos := 0
	end := int(length)

	for {
		if bytePos >= end {
			return length
		}

		b := buf[bytePos]
		switch (b >> 4) & 0x7 {
		case 0:
			hef.classInd = uint(b & 0xf)
		case 1:
			hef.progNum = uint((b >> 1) & 0x7)
			if b&0x1 != 0 {
				if bytePos+2 >= end {
					return length
				}
				bytePos++
				hef.pduLen = uint(b&0x7f) << 7
				bytePos++
				hef.pduLen |= uint(buf[bytePos] & 0x7f)
			}
		case 2:
			if bytePos+1 >= end {
				return length
			}
			hef.access = uint((b >> 3) & 0x1)
			hef.progType = uint(b&0x1) << 7
			bytePos++
			hef.progType |= uint(buf[bytePos] & 0x7f)
		case 3:
			if b&0x8 != 0 {
				if bytePos+4 >= end {
					return length
				}
				bytePos += 4
			} else {
				if bytePos+3 >= end {
					return length
				}
				bytePos += 3
			}
		case 4:
			if b&0x8 != 0 {
				if bytePos+3 >= end {
					return length
				}
				hef.appliedServices = uint(b & 0x7)
				bytePos++
				hef.pduMarker = uint(buf[bytePos]&0x7f) << 14
				bytePos++
				hef.pduMarker |= uint(buf[bytePos]&0x7f) << 7
				bytePos++
				hef.pduMarker |= uint(buf[bytePos] & 0x7f)
			} else {
				if bytePos+1 >= end {
					return length
				}
				bytePos++
			}
		default:
			log.Printf("nrsc5: unknown header expansion ID")
		}
		bytePos++
		if buf[bytePos-1]&0x80 == 0 {
			break
		}
	}

	return uint(bytePos)
}

func calcLCBits(hdr *frameHeader) uint {
	switch hdr.codecMode {
	case 0:
		return 16
	case 1, 2, 3:
		if hdr.streamID == 0 {
			return 12
		}
		return 16
	case 10, 13:
		return 12
	default:
		log.Printf("nrsc5: unknown codec field (%d)", hdr.codecMode)
		return 16
	}
}

func calcAvgPackets(hdr *frameHeader) uint {
	switch hdr.codecMode {
	case 0:
		return 32
	case 1, 2, 3:
		if hdr.streamID == 0 {
			return 4
		}
		return 32
	case 10:
		if hdr.streamID == 0 {
			return 32
		}
		return 4
	case 13:
		return 4
	default:
		log.Printf("nrsc5: unknown codec field (%d)", hdr.codecMode)
		return 32
	}
}

func parseLocation(buf []byte, lcBits uint, i uint) uint {
	if lcBits == 16 {
		return uint(buf[2*i+1])<<8 | uint(buf[2*i])
	}
	if i%2 == 0 {
		return uint(buf[i/2*3+1]&0xf)<<8 | uint(buf[i/2*3])
	}
	return uint(buf[i/2*3+2])<<4 | uint(buf[i/2*3+1]>>4)
}

func unescapeHDLC(data []byte) int {
	p := 0

	for i := 0; i < len(data); i++ {
		if data[i] == 0x7D {
			i++
			if i < len(data) {
				data[p] = data[i] | 0x20
				p++
			}
		} else {
			data[p] = data[i]
			p++
		}
	}

	return p
}

func (f *frame) aasPush(psd []byte, length uint, lc LogicalChannel) {
	length = uint(unescapeHDLC(psd[:length]))

	if length == 0 {
		// empty frames are used as padding
	} else if fcs16(psd, int(length)) != validFCS16 {
		// occasional CRC errors are normal, because transmitters abandon
		// their HDLC frame mid-stream when switching to new PSD data
	} else if psd[0] != 0x21 {
		log.Printf("nrsc5: unknown AAS protocol %x", psd[0])
	} else {
		// remove protocol and fcs fields
		f.input.output.aasPush(psd[1:length-3], length-3)
	}
}

func (f *frame) parseHDLC(buffer []byte, bufidx *int, bufsz int, input []byte, lc LogicalChannel) {
	for i := 0; i < len(input); i++ {
		b := input[i]
		if b == 0x7E {
			if *bufidx >= 0 {
				f.aasPush(buffer[:*bufidx], uint(*bufidx), lc)
			}
			*bufidx = 0
		} else if *bufidx >= 0 {
			if *bufidx == bufsz {
				log.Printf("nrsc5: HDLC buffer overflow")
				*bufidx = -1
				continue
			}
			buffer[*bufidx] = b
			*bufidx++
		}
	}
}

func (f *frame) processFixedCCC(buf []byte, buflen uint, lc LogicalChannel) {
	cccData := &f.cccData[lc]
	buflen = uint(unescapeHDLC(buf[:buflen]))

	// padding
	if buflen == 0 {
		return
	}

	// ignore new CCC packets (XXX they shouldn't change)
	if cccData.fixedReady {
		return
	}

	if fcs16(buf[:buflen], int(buflen)) != validFCS16 {
		log.Printf("nrsc5: bad CCC checksum")
		return
	}

	for i := 0; i < 4; i++ {
		subch := &cccData.subchannel[i]
		subch.mode = 0
		subch.length = 0

		if 5+i*4 <= int(buflen) {
			mode := uint16(buf[1+i*4]) | uint16(buf[2+i*4])<<8
			length := uint16(buf[3+i*4]) | uint16(buf[4+i*4])<<8

			if mode == 0 {
				subch.mode = mode
				subch.length = length
				subch.blockIdx = 0
				subch.idx = -1
			} else {
				log.Printf("nrsc5: subchannel mode %04X not supported", mode)
			}
		}
	}

	cccData.fixedReady = true
}

// FIXME: We only support mode=0 (no FEC, no interleaving)
func (f *frame) processFixedBlock(i int, lc LogicalChannel) {
	cccData := &f.cccData[lc]
	subch := &cccData.subchannel[i]
	f.parseHDLC(subch.data[:], &subch.idx, maxAASLen, subch.blocks[4:255+4], lc)
}

func syncWidth(b byte) uint {
	if b == 0x00 {
		return 1
	}
	if b>>4 == b&0xf {
		return uint(b&0xf) * 2
	}
	return 0 // invalid
}

func (f *frame) processFixedData(length int, lc LogicalChannel) int {
	cccData := &f.cccData[lc]

	bbm := [4]byte{0x7D, 0x3A, 0xE2, 0x42}
	p := length - 1

	if cccData.syncCount < 2 {
		width := syncWidth(f.buffer[p])
		if width > 0 && cccData.syncWidth == width {
			cccData.syncCount++
		} else {
			cccData.syncCount = 0
		}
		cccData.syncWidth = width

		if cccData.syncCount < 2 {
			return p
		}
	}

	p -= int(cccData.syncWidth)
	f.parseHDLC(cccData.cccBuf[:], &cccData.cccIdx, 32, f.buffer[p:p+int(cccData.syncWidth)], lc)

	// wait until we have subchannel information
	if !cccData.fixedReady {
		return p
	}

	for i := 3; i >= 0; i-- {
		subch := &cccData.subchannel[i]
		length := int(subch.length)

		if length == 0 {
			continue
		}

		p -= length
		for j := 0; j < length; j++ {
			subch.blocks[subch.blockIdx] = f.buffer[p+j]
			subch.blockIdx++
			if subch.blockIdx == 4 && !bytesEqual4(subch.blocks[:4], bbm) {
				// mis-aligned, skip a byte
				copy(subch.blocks[:3], subch.blocks[1:4])
				subch.blockIdx--
			}

			if subch.blockIdx == 255+4 {
				// we have a complete block, deinterleave and process
				f.processFixedBlock(i, lc)
				subch.blockIdx = 0
			}
		}
	}

	return p
}

func (f *frame) process(length int, lc LogicalChannel, pci uint) {
	offset := 0
	audioEnd := length

	if hasFixed(pci) {
		audioEnd = f.processFixedData(length, lc)
	}

	if !hasAudio(pci) {
		return
	}

	for offset < audioEnd-rsCodewordLen {
		start := offset
		var hdr frameHeader
		var hefObj hef

		if !f.fixHeader(f.buffer[offset:]) {
			return
		}

		parseHeader(f.buffer[offset:], &hdr)
		offset += 14
		lcBits := calcLCBits(&hdr)
		locBytes := (lcBits*hdr.nop + 4) / 8
		if start+int(hdr.laLocation)+1 < offset+int(locBytes) || start+int(hdr.laLocation) >= audioEnd {
			return
		}

		locations := make([]uint16, maxAudioPackets)
		for j := uint(0); j < hdr.nop; j++ {
			locations[j] = uint16(parseLocation(f.buffer[offset:], lcBits, j))
			if j == 0 && int(locations[j]) <= int(hdr.laLocation) {
				return
			}
			if j > 0 && locations[j] <= locations[j-1] {
				return
			}
			if start+int(locations[j]) >= audioEnd {
				return
			}
		}
		offset += int(locBytes)

		if hdr.streamID >= MAX_STREAMS {
			log.Printf("nrsc5: invalid stream_id: %d", hdr.streamID)
			offset = start + int(uint(hdr.nop-1)) + 1
			continue
		}

		if hdr.hef != 0 {
			offset += int(parseHEF(f.buffer[offset:], uint(audioEnd-offset), &hefObj))
		}
		prog := hefObj.progNum
		service := &f.services[prog]

		if hdr.streamID == 0 &&
			(service.access != int(hefObj.access) ||
				service.typ != int(hefObj.progType) ||
				service.codecMode != int(hdr.codecMode) ||
				service.blendControl != int(hdr.blendControl) ||
				service.digitalAudioGain != int(hdr.perStreamDelay) ||
				service.commonDelay != int(hdr.commonDelay) ||
				service.latency != int(hdr.latency)) {
			service.access = int(hefObj.access)
			service.typ = int(hefObj.progType)
			service.codecMode = int(hdr.codecMode)
			service.blendControl = int(hdr.blendControl)
			service.digitalAudioGain = int(hdr.perStreamDelay)
			service.commonDelay = int(hdr.commonDelay)
			service.latency = int(hdr.latency)

			gain := service.digitalAudioGain
			if gain >= 16 {
				gain -= 32
			}
			f.input.radio.reportAudioService(prog,
				uint(service.access), uint(service.typ), uint(service.codecMode),
				uint(service.blendControl), gain,
				uint(service.commonDelay*4), uint(service.latency*2))
		}

		avg := calcAvgPackets(&hdr)
		seq := (ELASTIC_BUFFER_LEN + hdr.seq - hdr.pfirst) % ELASTIC_BUFFER_LEN

		outputOffset := (ELASTIC_BUFFER_LEN + (hdr.pduSeq * avg) - (hdr.latency * 2)) % ELASTIC_BUFFER_LEN
		if (ELASTIC_BUFFER_LEN+seq-outputOffset)%ELASTIC_BUFFER_LEN >= (ELASTIC_BUFFER_LEN / 2) {
			outputOffset = (outputOffset + (ELASTIC_BUFFER_LEN / 2)) % ELASTIC_BUFFER_LEN
		}

		f.input.output.align(prog, hdr.streamID, uint(outputOffset))

		f.parseHDLC(f.psdBuf[prog][:], &f.psdIdx[prog], maxAASLen,
			f.buffer[offset:start+int(hdr.laLocation)+1], lc)
		offset = start + int(hdr.laLocation) + 1

		for j := uint(0); j < hdr.nop; j++ {
			cnt := start + int(locations[j]) - offset
			c := crc8(f.buffer[offset:], cnt+1)

			ref := PacketRef{
				Program:  uint(prog),
				StreamID: hdr.streamID,
				Data:     f.buffer[offset : offset+cnt],
				Size:     uint(cnt),
				Seq:      uint(seq),
				Flags:    PacketFlagNone,
				Shape:    PacketFull,
			}

			if c != 0 {
				ref.Flags |= PacketFlagCRCError
			}
			if j == 0 && hdr.pfirst != 0 {
				ref.Shape = PacketHalfBack
			} else if j == hdr.nop-1 && hdr.plast != 0 {
				ref.Shape = PacketHalfFront
			}

			f.input.output.push(&ref)

			offset += cnt + 1
			seq = (seq + 1) % ELASTIC_BUFFER_LEN
		}
	}
}

func popCount(n uint) int {
	c := 0
	for ; n != 0; c++ {
		n &= n - 1
	}
	return c
}

func fuzzyPCI(pci uint, pciLen uint) (match uint, score int) {
	for i := 0; i < pciCount; i++ {
		s := popCount((pci ^ pciPossibilities[i]) >> (24 - pciLen))
		if s <= pciMaxErrors {
			return pciPossibilities[i], int(s)
		}
	}
	return 0, -1
}

func (f *frame) push(bits []byte, length int, lc LogicalChannel) {
	var start, offset, pciLen int
	var h, j int
	var header uint32
	var val byte
	ptr := 0

	switch length {
	case P1_FRAME_LEN_FM:
		start = P1_FRAME_LEN_FM - 30000
		offset = 1248
		pciLen = 24
	case P3_FRAME_LEN_MP3_MP11:
		start = 120
		offset = 184
		pciLen = 24
	case P3_FRAME_LEN_MP2:
		start = 120
		offset = 88
		pciLen = 24
	case P1_FRAME_LEN_AM:
		start = 120
		offset = 160
		pciLen = 22
	case P3_FRAME_LEN_MA1:
		start = 120
		offset = 992
		pciLen = 24
	case P3_FRAME_LEN_MA3:
		start = 120
		offset = 1240
		pciLen = 24
	default:
		log.Printf("nrsc5: unknown frame length: %d", length)
		return
	}

	for i := 0; i < length; i++ {
		// swap bit order
		byteStart := (i >> 3) << 3
		byteLen := 8
		if length-byteStart < 8 {
			byteLen = length - byteStart
		}
		bit := bits[byteStart+byteLen-1-(i&7)]

		if i >= start && (i-start)%offset == 0 && h < pciLen {
			header |= uint32(bit) << (23 - h)
			h++
		} else {
			val |= bit << (7 - j)
			j++
			if j == 8 {
				f.buffer[ptr] = val
				ptr++
				val = 0
				j = 0
			}
		}
	}

	pci, score := fuzzyPCI(uint(header), uint(pciLen))
	if score < 0 {
		if lc == P1LogicalChannel {
			f.input.setSyncState(syncStateNone)
		}
		return
	}

	f.process(ptr, lc, pci)
}

func (f *frame) reset() {
	for prog := 0; prog < MAX_PROGRAMS; prog++ {
		f.services[prog].access = -1
		f.services[prog].typ = -1
		f.services[prog].codecMode = -1
		f.services[prog].blendControl = -1
		f.services[prog].digitalAudioGain = -1
		f.services[prog].commonDelay = -1
		f.services[prog].latency = -1
	}

	for prog := 0; prog < MAX_PROGRAMS; prog++ {
		f.psdIdx[prog] = -1
	}

	for channel := 0; channel < int(NUM_LOGICAL_CHANNELS); channel++ {
		f.cccData[channel].fixedReady = false
		f.cccData[channel].syncWidth = 0
		f.cccData[channel].syncCount = 0
		f.cccData[channel].cccIdx = -1
	}
}

func newFrame(in *input) *frame {
	f := &frame{
		input: in,
		rsDec: initRs(8, 0x11d, 1, 1, 8),
	}
	f.reset()
	return f
}

// frame implements frame_t from frame.h.
type frame struct {
	input *input

	buffer   [MAX_PDU_LEN]byte
	services [MAX_PROGRAMS]audioService
	program  uint
	psdBuf   [MAX_PROGRAMS][maxAASLen]byte
	psdIdx   [MAX_PROGRAMS]int
	cccData  [NUM_LOGICAL_CHANNELS]cccData

	rsDec *rsState

	j int // bit accumulator index for push
}

type audioService struct {
	access           int
	typ              int
	codecMode        int
	blendControl     int
	digitalAudioGain int
	commonDelay      int
	latency          int
}

type fixedSubchannel struct {
	mode     uint16
	length   uint16
	blockIdx uint
	blocks   [255 + 4]byte
	idx      int
	data     [maxAASLen]byte
}

type cccData struct {
	syncWidth  uint
	syncCount  uint
	cccBuf     [32]byte
	cccIdx     int
	subchannel [4]fixedSubchannel
	fixedReady bool
}

// PacketRef mirrors packet_ref_t.
type PacketRef struct {
	Data     []byte
	Size     uint
	Program  uint
	StreamID uint
	Seq      uint
	Flags    uint
	Shape    uint
}

func bytesEqual4(a []byte, b [4]byte) bool {
	return a[0] == b[0] && a[1] == b[1] && a[2] == b[2] && a[3] == b[3]
}
