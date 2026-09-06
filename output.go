package nrsc5

import (
	"log"
	"time"
)

// Output stage: audio packets, ID3, SIG/AAS ports, LOT files. Ported from
// output.c and here_images.c.

const (
	audioFrameBytes  = 8192
	maxSigServices   = 16
	maxSigComponents = 8
	maxLotFiles      = 12
	lotFragmentSize  = 256
	maxFileBytes     = 65536
	maxLotFragments  = maxFileBytes / lotFragmentSize
)

const nrsc5AudioFrameSamples = 2048

// LOT file state.
type aasFile struct {
	timestamp  uint
	name       string
	mime       uint32
	expiryUTC  time.Time
	lot        uint16
	size       uint32
	bytesSoFar uint32
	fragments  [maxLotFragments][]byte
	data       []byte
}

// sigComponent mirrors sig_component_t.
type sigComponent struct {
	typ byte
	id  byte

	serviceExt   *SigService
	componentExt *SigComponent

	// data component
	port            uint16
	serviceDataType uint16
	dataType        byte
	mime            uint32
	lotFiles        [maxLotFiles]*aasFile

	// audio component
	audioPort uint8
	audioType uint8
	audioMime uint32
}

// sigService mirrors sig_service_t.
type sigService struct {
	typ       byte
	number    uint16
	name      string
	component [maxSigComponents]sigComponent
}

// packet mirrors packet_t.
type packet struct {
	size  uint
	flags uint
	shape uint
	data  [MAX_PDU_LEN]byte
}

type elasticBuffer struct {
	packets     [ELASTIC_BUFFER_LEN]packet
	audioOffset int
}

// output mirrors output_t.
type output struct {
	radio *Radio

	elastic [MAX_PROGRAMS][MAX_STREAMS]elasticBuffer
	aac     [MAX_PROGRAMS]*hdcDecoder

	sigBytes      []byte
	sigLen        uint
	services      [maxSigServices]sigService
	lotLRUCounter uint

	hereImages hereImages
}

func newOutput(radio *Radio) *output {
	o := &output{radio: radio}
	o.hereImages.init(radio)
	o.reset()
	return o
}

func isCompletePkt(pkt *packet) bool {
	return pkt.shape == PacketFull
}

func isCRCOk(pkt *packet) bool {
	return pkt.flags&PacketFlagCRCError == 0
}

func (o *output) align(program uint, streamID uint, offset uint) {
	elastic := &o.elastic[program][streamID]
	elastic.audioOffset = int(offset)
}

func (o *output) push(ref *PacketRef) {
	elastic := &o.elastic[ref.Program][ref.StreamID]
	pkt := &elastic.packets[ref.Seq]

	if ref.StreamID != 0 {
		return // TODO: Process enhanced stream
	}

	if pkt.shape == PacketFull {
		log.Printf("nrsc5: packet %d already exists in elastic buffer for program %d, stream %d. Overwriting.",
			ref.Seq, ref.Program, ref.StreamID)
	}

	if ref.Shape == PacketHalfBack && pkt.shape == PacketHalfFront {
		pkt.flags |= ref.Flags
		pkt.shape = PacketFull

		if isCRCOk(pkt) {
			copy(pkt.data[pkt.size:], ref.Data[:ref.Size])
			pkt.size += ref.Size
		} else {
			pkt.size = 0
		}
	} else {
		if ref.Shape == PacketHalfBack {
			return
		}

		pkt.flags = ref.Flags
		pkt.shape = ref.Shape

		if isCRCOk(pkt) {
			copy(pkt.data[:], ref.Data[:ref.Size])
			pkt.size = ref.Size
		} else {
			pkt.size = 0
		}
	}
}

func pktReset(pkt *packet) {
	pkt.size = 0
	pkt.flags = PacketFlagNone
	pkt.shape = PacketNone
}

func (o *output) advance() {
	audioFrames := 2
	if o.radio.mode == ModeAM {
		audioFrames = 4
	}

	for program := 0; program < MAX_PROGRAMS; program++ {
		elastic := &o.elastic[program][0] // TODO: Process enhanced stream

		if elastic.audioOffset == -1 {
			continue
		}

		for frame := 0; frame < audioFrames; frame++ {
			pkt := &elastic.packets[elastic.audioOffset]
			producedAudio := false

			if isCompletePkt(pkt) {
				o.radio.reportHDC(uint(program), pkt.data[:pkt.size], pkt.flags)
			}

			if isCompletePkt(pkt) && isCRCOk(pkt) {
				if o.aac[program] == nil {
					o.aac[program] = newHDCDecoder()
				}
				if samples := o.aac[program].decode(pkt.data[:pkt.size]); samples != nil {
					o.radio.reportAudio(uint(program), samples)
					producedAudio = true
				}
			} else {
				// Reset decoder. Missing packets.
				if o.aac[program] != nil {
					o.aac[program].close()
					o.aac[program] = nil
				}
			}

			pktReset(pkt)

			if !producedAudio {
				o.radio.reportAudio(uint(program), make([]int16, nrsc5AudioFrameSamples*2))
			}

			elastic.audioOffset = (elastic.audioOffset + 1) % ELASTIC_BUFFER_LEN
		}
	}
}

func aasFreeLot(file *aasFile) {
	*file = aasFile{}
}

func (o *output) aasReset() {
	o.sigBytes = nil
	o.sigLen = 0

	for i := 0; i < maxSigServices; i++ {
		service := &o.services[i]

		for j := 0; j < maxSigComponents; j++ {
			component := &service.component[j]

			if component.typ == SigComponentData {
				for k := 0; k < maxLotFiles; k++ {
					if component.lotFiles[k] != nil {
						aasFreeLot(component.lotFiles[k])
						component.lotFiles[k] = nil
					}
				}
			}
		}
	}
	o.lotLRUCounter = 1
}

func (o *output) reset() {
	o.aasReset()

	for i := 0; i < MAX_PROGRAMS; i++ {
		for j := 0; j < MAX_STREAMS; j++ {
			for k := 0; k < ELASTIC_BUFFER_LEN; k++ {
				pktReset(&o.elastic[i][j].packets[k])
			}
			o.elastic[i][j].audioOffset = -1
		}
		if o.aac[i] != nil {
			o.aac[i].close()
			o.aac[i] = nil
		}
	}

	o.hereImages.reset()
}

func id3Length(buf []byte) uint {
	return uint(buf[0]&0x7f)<<21 | uint(buf[1]&0x7f)<<14 | uint(buf[2]&0x7f)<<7 | uint(buf[3]&0x7f)
}

func id3EncodeUTF8(enc byte, buf []byte, length uint) string {
	if enc == 0 {
		return iso8859ToUTF8(buf[:length])
	} else if enc == 1 {
		return ucs2ToUTF8(buf[:length])
	}
	log.Printf("nrsc5: invalid encoding: %d", enc)
	return ""
}

func id3Text(buf []byte, frameLen uint) string {
	if frameLen > 0 {
		return id3EncodeUTF8(buf[0], buf[1:], frameLen-1)
	}
	return ""
}

func memchrEnc(enc int, buf []byte, length uint) int {
	if enc == 0 {
		for i := uint(0); i < length; i++ {
			if buf[i] == 0 {
				return int(i)
			}
		}
		return -1
	} else if enc == 1 {
		for i := uint(0); i+1 < length; i += 2 {
			if buf[i] == 0 && buf[i+1] == 0 {
				return int(i)
			}
		}
		return -1
	}
	log.Printf("nrsc5: invalid encoding: %d", enc)
	return -1
}

func parseDigits(p []byte, n int) int {
	value := 0
	for i := 0; i < n; i++ {
		if p[i] < '0' || p[i] > '9' {
			return -1
		}
		value = value*10 + int(p[i]-'0')
	}
	return value
}

func (o *output) id3(program uint, buf []byte, length uint) {
	var title, artist, album, genre, ufidOwner, ufidID string
	var xhdrMime uint32
	xhdrParam, xhdrLot := -1, -1
	var comments []ID3Comment
	var price, url, seller, desc string
	var until time.Time
	receivedAs := byte(0)

	off := uint(0)
	var id3Len uint

	if length < 10 || string(buf[:5]) != "ID3\x03\x00" || buf[5] != 0 {
		return
	}
	id3Len = id3Length(buf[6:]) + 10
	if id3Len > length {
		return
	}
	off += 10

	for off+10 <= id3Len {
		tag := buf[off : off+10]
		data := buf[off+10:]
		frameLen := uint(tag[4])<<24 | uint(tag[5])<<16 | uint(tag[6])<<8 | uint(tag[7])
		if off+10+frameLen > id3Len {
			break
		}

		switch string(tag[:4]) {
		case "TIT2":
			title = id3Text(data, frameLen)
		case "TPE1":
			artist = id3Text(data, frameLen)
		case "TALB":
			album = id3Text(data, frameLen)
		case "TCON":
			genre = id3Text(data, frameLen)
		case "UFID":
			delim := -1
			for i := uint(0); i < frameLen; i++ {
				if data[i] == 0 {
					delim = int(i)
					break
				}
			}
			if delim >= 0 {
				ufidOwner = string(data[:delim])
				ufidID = string(data[delim+1 : frameLen])
			}
		case "COMR":
			pos := 0
			end := int(frameLen)
			if pos+1 > end {
				log.Printf("nrsc5: bad COMR tag (frame_len %d)", frameLen)
				break
			}
			enc := data[0]
			pos++

			delim := [4]int{}
			delimEnd := [4]int{}
			year, mon, mday := 0, 0, 0
			i := 0
			ok := true
			for i = 0; i < 4; i++ {
				delimEnc := 0
				if i >= 2 {
					delimEnc = int(enc)
				}
				delimLen := 1
				if delimEnc == 1 {
					delimLen = 2
				}
				delim[i] = memchrEnc(delimEnc, data[pos:], uint(end-pos))
				if delim[i] < 0 {
					log.Printf("nrsc5: bad COMR tag (frame_len %d)", frameLen)
					ok = false
					break
				}
				delim[i] += pos
				delimEnd[i] = delim[i] + delimLen
				pos = delimEnd[i]

				if i == 0 {
					if pos+8 > end {
						log.Printf("nrsc5: bad COMR tag (frame_len %d)", frameLen)
						ok = false
						break
					}

					year = parseDigits(data[delim[0]+1:], 4) - 1900
					mon = parseDigits(data[delim[0]+5:], 2) - 1
					mday = parseDigits(data[delim[0]+7:], 2)

					if year < 0 || mon < 0 || mday < 0 {
						log.Printf("nrsc5: failed to parse valid_until on COMR tag")
						ok = false
						break
					}

					pos += 8
				} else if i == 1 {
					if pos+1 > end {
						log.Printf("nrsc5: bad COMR tag (frame_len %d)", frameLen)
						ok = false
						break
					}

					pos++
				}
			}

			if ok && i == 4 {
				price = string(data[1 : delim[0]+1])
				until = time.Date(year+1900, time.Month(mon+1), mday, 0, 0, 0, 0, time.UTC)
				url = string(data[delimEnd[0]+8 : delimEnd[0]+8+0])
				// url extends until delim[1]
				url = string(data[delimEnd[0]+8 : delim[1]])
				receivedAs = data[delimEnd[1]]
				seller = id3EncodeUTF8(enc, data[delimEnd[1]+1:], uint(delim[2]-(delimEnd[1]+1)))
				desc = id3EncodeUTF8(enc, data[delimEnd[2]:], uint(delim[3]-delimEnd[2]))
			}
		case "COMM":
			if frameLen < 5 {
				log.Printf("nrsc5: bad COMM tag (frame_len %d)", frameLen)
			} else {
				enc := data[0]
				delimRel := memchrEnc(int(enc), data[4:], frameLen-4)
				if delimRel >= 0 {
					delim := 4 + delimRel
					endText := delim
					if enc == 1 {
						endText += 2
					} else {
						endText++
					}

					comments = append(comments, ID3Comment{
						Lang:             string(data[1:4]),
						ShortContentDesc: id3EncodeUTF8(enc, data[4:], uint(delim-4)),
						FullText:         id3EncodeUTF8(enc, data[endText:], frameLen-uint(endText)),
					})
				}
			}
		case "XHDR":
			if frameLen < 6 {
				log.Printf("nrsc5: bad XHDR tag (frame_len %d)", frameLen)
			} else {
				xhdrMime = uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
				xhdrParam = int(data[4])
				extlen := data[5]
				if 6+uint(extlen) != frameLen {
					log.Printf("nrsc5: bad XHDR tag (frame_len %d, extlen %d)", frameLen, extlen)
				} else if xhdrParam == 0 && extlen == 2 {
					xhdrLot = int(data[6]) | int(data[7])<<8
				} else if xhdrParam == 1 && extlen == 0 {
					xhdrLot = -1
				} else {
					log.Printf("nrsc5: unhandled XHDR param (frame_len %d, param %d, extlen %d)", frameLen, xhdrParam, extlen)
				}
			}
		default:
			log.Printf("nrsc5: %s tag", string(tag[:4]))
		}

		off += 10 + frameLen
	}

	o.radio.reportID3(&ID3Event{
		Program:     program,
		Title:       title,
		Artist:      artist,
		Album:       album,
		Genre:       genre,
		UFIDOwner:   ufidOwner,
		UFIDID:      ufidID,
		XHRDMime:    xhdrMime,
		XHRDParam:   xhdrParam,
		XHRDLot:     xhdrLot,
		Price:       price,
		ContactURL:  url,
		Seller:      seller,
		Description: desc,
		ReceivedAs:  receivedAs,
		ValidUntil:  until,
		Comments:    comments,
	})
}

func findComponent(service *sigService, componentID byte) int {
	componentIdx := 0
	for componentIdx = 0; componentIdx < maxSigComponents; componentIdx++ {
		if service.component[componentIdx].typ == SigComponentNone {
			break // reached a free slot in the component list
		}

		if service.component[componentIdx].id == componentID {
			log.Printf("nrsc5: duplicate SIG component: service %d, component %d", service.number, componentID)
			break
		}
	}

	return componentIdx
}

func (o *output) parseSIG(buf []byte, length uint) {
	p := 0
	var service *sigService

	if o.sigBytes != nil {
		if uint(len(buf[:length])) == o.sigLen && bytesEqual(buf[:length], o.sigBytes) {
			// previously parsed SIG table has not changed
			return
		}
		o.aasReset()
	}

	o.sigBytes = append([]byte(nil), buf[:length]...)
	o.sigLen = length

	services := &o.services

	for p < int(length) {
		typ := buf[p]
		p++
		switch typ & 0xF0 {
		case 0x40:
			serviceNumber := uint16(buf[p]) | uint16(buf[p+1])<<8
			serviceIdx := 0

			for serviceIdx = 0; serviceIdx < maxSigServices; serviceIdx++ {
				if services[serviceIdx].typ == SigServiceNone {
					break // reached a free slot in the service list
				}

				if services[serviceIdx].number == serviceNumber {
					log.Printf("nrsc5: duplicate SIG service: %d", serviceNumber)
					services[serviceIdx] = sigService{}
					break
				}
			}

			if serviceIdx == maxSigServices {
				log.Printf("nrsc5: too many SIG services")
				p = int(length)
				break
			}

			service = &services[serviceIdx]
			if typ == 0x40 {
				service.typ = SigServiceAudio
			} else {
				service.typ = SigServiceData
			}
			service.number = serviceNumber

			p += 3
		case 0x60:
			// length (1-byte) value (length - 1)
			l := buf[p]
			p++
			if service == nil {
				log.Printf("nrsc5: invalid SIG data (%02X)", typ)
				p = int(length)
				break
			} else if typ == 0x69 {
				service.name = iso8859ToUTF8(buf[p+1 : p+int(l)-1])
			} else if typ == 0x67 {
				componentID := buf[p]
				componentIdx := findComponent(service, componentID)

				if componentIdx == maxSigComponents {
					log.Printf("nrsc5: too many SIG components")
					p = int(length)
					break
				}

				comp := &service.component[componentIdx]
				comp.typ = SigComponentData
				comp.id = componentID
				comp.port = uint16(buf[p+1]) | uint16(buf[p+2])<<8
				comp.serviceDataType = uint16(buf[p+3]) | uint16(buf[p+4])<<8
				comp.dataType = buf[p+5]
				comp.mime = uint32(buf[p+8]) | uint32(buf[p+9])<<8 | uint32(buf[p+10])<<16 | uint32(buf[p+11])<<24
			} else if typ == 0x66 {
				componentID := buf[p]
				componentIdx := findComponent(service, componentID)

				if componentIdx == maxSigComponents {
					log.Printf("nrsc5: too many SIG components")
					p = int(length)
					break
				}

				comp := &service.component[componentIdx]
				comp.typ = SigComponentAudio
				comp.id = componentID
				comp.audioPort = buf[p+1]
				comp.audioType = buf[p+2]
				comp.audioMime = uint32(buf[p+7]) | uint32(buf[p+8])<<8 | uint32(buf[p+9])<<16 | uint32(buf[p+10])<<24
			}
			p += int(l) - 1
		default:
			log.Printf("nrsc5: unexpected byte %02X", buf[p])
			p = int(length)
		}
	}

	// build report
	var sigServices []SigService
	for i := 0; i < maxSigServices; i++ {
		svc := &services[i]
		if svc.typ == SigServiceNone {
			break
		}
		s := SigService{
			Number: uint(svc.number),
			Name:   svc.name,
			Type:   uint(svc.typ),
		}
		for j := 0; j < maxSigComponents; j++ {
			comp := &svc.component[j]
			if comp.typ == SigComponentNone {
				break
			}
			c := SigComponent{
				ID:   uint(comp.id),
				Type: uint(comp.typ),
			}
			if comp.typ == SigComponentAudio {
				c.AudioComponent = &SigAudioComponent{
					Port: comp.audioPort,
					Type: comp.audioType,
					MIME: comp.audioMime,
				}
			} else {
				c.DataComponent = &SigDataComponent{
					Port:            comp.port,
					ServiceDataType: comp.serviceDataType,
					Type:            comp.dataType,
					MIME:            comp.mime,
				}
			}
			s.Components = append(s.Components, c)
		}
	}
	o.radio.reportSIG(sigServices)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (o *output) findPort(portID uint16) *sigComponent {
	for i := 0; i < maxSigServices; i++ {
		service := &o.services[i]
		if service.typ == SigServiceNone {
			break
		}

		for j := 0; j < maxSigComponents; j++ {
			component := &service.component[j]
			if component.typ == SigComponentNone {
				break
			}

			if component.typ == SigComponentData && component.port == portID {
				return component
			}
		}
	}
	return nil
}

func findLot(component *sigComponent, lot uint16) *aasFile {
	for i := 0; i < maxLotFiles; i++ {
		file := component.lotFiles[i]
		if file == nil || file.timestamp == 0 {
			continue
		}
		if file.lot == lot {
			return file
		}
	}
	return nil
}

func findFreeLot(component *sigComponent) *aasFile {
	minTimestamp := ^uint(0)
	minIdx := 0

	for i := 0; i < maxLotFiles; i++ {
		file := component.lotFiles[i]
		if file == nil {
			component.lotFiles[i] = &aasFile{}
			file = component.lotFiles[i]
		}
		timestamp := file.timestamp
		if timestamp == 0 {
			return file
		}
		if timestamp < minTimestamp {
			minTimestamp = timestamp
			minIdx = i
		}
	}

	file := component.lotFiles[minIdx]
	aasFreeLot(file)
	return file
}

func (o *output) processPort(portID uint16, seq uint16, buf []byte, length uint) {
	if o.services[0].typ == SigServiceNone {
		// Wait until we receive SIG data.
		return
	}

	component := o.findPort(portID)
	if component == nil {
		log.Printf("nrsc5: port %04X not defined in SIG table", portID)
		return
	}

	switch component.dataType {
	case AASTypeStream:
		o.radio.reportStream(seq, length, buf, component)
		if component.mime == MIMEHereImage {
			o.hereImages.push(seq, length, buf)
		}
	case AASTypePacket:
		o.radio.reportPacket(seq, length, buf, component)
	case AASTypeLOT:
		if length < 8 {
			log.Printf("nrsc5: bad fragment (port %04X, len %d)", portID, length)
			return
		}
		hdrlen := int(buf[0])
		repeat := buf[1]
		lot := uint16(buf[2]) | uint16(buf[3])<<8
		fragSeq := uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16 | uint32(buf[7])<<24
		if hdrlen < 8 || hdrlen > int(length) {
			log.Printf("nrsc5: wrong header len (port %04X, len %d, hdrlen %d)", portID, length, hdrlen)
			return
		}
		buf = buf[8:]
		length -= 8
		hdrlen -= 8

		if fragSeq >= maxLotFragments {
			log.Printf("nrsc5: sequence too large (%d)", fragSeq)
			return
		}

		file := findLot(component, lot)
		if file == nil {
			file = findFreeLot(component)
			file.lot = lot
		}
		file.timestamp = o.lotLRUCounter
		o.lotLRUCounter++

		newData := false

		if hdrlen > 0 {
			if hdrlen < 16 {
				log.Printf("nrsc5: header is too short (port %04X, len %d, hdrlen %d)", portID, length, hdrlen)
				return
			}

			version := uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24
			if version != 1 {
				log.Printf("nrsc5: unknown LOT version: %d", version)
			}

			year := int(buf[7]<<4|buf[6]>>4) - 1900
			mon := int(buf[6]&0xf) - 1
			mday := int(buf[5] >> 3)
			hour := int((buf[5]&0x7)<<2 | buf[4]>>6)
			min := int(buf[4] & 0x3f)

			size := uint32(buf[8]) | uint32(buf[9])<<8 | uint32(buf[10])<<16 | uint32(buf[11])<<24
			mime := uint32(buf[12]) | uint32(buf[13])<<8 | uint32(buf[14])<<16 | uint32(buf[15])<<24
			buf = buf[16:]
			length -= 16
			hdrlen -= 16
			// Everything after the fixed header is the filename.

			if file.name != "" {
				name := string(buf[:hdrlen])
				if name != file.name || size != file.size || mime != file.mime ||
					year != file.expiryUTC.Year()-1900 || mon != int(file.expiryUTC.Month())-1 ||
					mday != file.expiryUTC.Day() || hour != file.expiryUTC.Hour() || min != file.expiryUTC.Minute() {
					// Reset, since metadata has changed
					oldLot := file.lot
					*file = aasFile{}
					file.lot = oldLot
					file.timestamp = o.lotLRUCounter
					newData = true
				}
			} else {
				// Metadata received for the first time
				newData = true
			}

			file.name = string(buf[:hdrlen])
			file.size = size
			file.mime = mime
			file.expiryUTC = time.Date(year+1900, time.Month(mon+1), mday, hour, min, 0, 0, time.UTC)

			buf = buf[hdrlen:]
			length -= uint(hdrlen)
			hdrlen = 0

			if newData {
				o.radio.reportLOTLot(lotEventHeader, file)
			}
		}

		isDuplicate := true
		if file.fragments[fragSeq] == nil {
			newData = true
			isDuplicate = false
			fragment := make([]byte, lotFragmentSize)
			if uint(length) > lotFragmentSize {
				log.Printf("nrsc5: fragment too large (%d)", length)
				return
			}
			copy(fragment, buf[:length])
			file.fragments[fragSeq] = fragment
			file.bytesSoFar += uint32(length)
		}
		o.radio.reportLOTFragment(file, uint(fragSeq), repeat, isDuplicate, uint(length), file.bytesSoFar, buf[:length])

		if newData && file.size != 0 {
			complete := true
			numFragments := (file.size + lotFragmentSize - 1) / lotFragmentSize
			for i := uint32(0); i < numFragments; i++ {
				if file.fragments[i] == nil {
					complete = false
					break
				}
			}
			if complete {
				data := make([]byte, numFragments*lotFragmentSize)
				for i := uint32(0); i < numFragments; i++ {
					copy(data[i*lotFragmentSize:(i+1)*lotFragmentSize], file.fragments[i])
				}
				file.data = data
				o.radio.reportLOTLot(lotEventComplete, file)
			}
		}
	default:
		log.Printf("nrsc5: unknown port type %d", component.dataType)
	}
}

// LOT event types.
const (
	lotEventHeader   = 0
	lotEventComplete = 1
)

func (o *output) aasPush(buf []byte, length uint) {
	port := uint16(buf[0]) | uint16(buf[1])<<8
	seq := uint16(buf[2]) | uint16(buf[3])<<8
	if port == 0x5100 || (port >= 0x5201 && port <= 0x5207) {
		// PSD ports
		o.id3(uint(port&0x7), buf[4:], uint(len(buf))-4)
	} else if port == 0x20 {
		// Station Information Guide
		o.parseSIG(buf[4:], uint(len(buf))-4)
	} else if port >= 0x401 && port <= 0x50FF {
		o.processPort(port, seq, buf[4:], uint(len(buf))-4)
	} else {
		log.Printf("nrsc5: unknown AAS port %04X, seq %04X, length %d", port, seq, length)
	}
}

// hereImages implements here_images.c.
type hereImages struct {
	radio         *Radio
	expectedSeq   int
	syncState     uint32
	payloadLen    int
	buffer        [2048]byte
	bufferIdx     int
	lastTimestamp [1 + HereTrafficTiles]uint32
}

func (h *hereImages) init(radio *Radio) {
	h.radio = radio
}

func (h *hereImages) reset() {
	h.expectedSeq = -1
	h.lastTimestamp = [1 + HereTrafficTiles]uint32{}
}

func (h *hereImages) processPacket() {
	if h.payloadLen < 28 {
		log.Printf("nrsc5: HERE Image frame too short")
		return
	}

	imageType := int(h.buffer[0] >> 4)
	seq := int(h.buffer[0] & 0x0f)

	if imageType != HereImageTraffic && imageType != HereImageWeather {
		log.Printf("nrsc5: unknown HERE Image type: %d", imageType)
		return
	}

	n1 := int(h.buffer[2])<<8 | int(h.buffer[3])
	n2 := int(h.buffer[4])<<8 | int(h.buffer[5])
	timestamp := uint32(h.buffer[9])<<24 | uint32(h.buffer[10])<<16 | uint32(h.buffer[11])<<8 | uint32(h.buffer[12])

	lat1 := int(h.buffer[14]&0x7f)<<18 | int(h.buffer[15])<<10 | int(h.buffer[16])<<2 | int(h.buffer[17]>>6)
	if h.buffer[14]&0x80 != 0 {
		lat1 = -lat1
	}

	lon1 := int(h.buffer[17]&0x1f)<<20 | int(h.buffer[18])<<12 | int(h.buffer[19])<<4 | int(h.buffer[20]>>4)
	if h.buffer[17]&0x20 != 0 {
		lon1 = -lon1
	}

	lat2 := int(h.buffer[20]&0x07)<<22 | int(h.buffer[21])<<14 | int(h.buffer[22])<<6 | int(h.buffer[23]>>2)
	if h.buffer[20]&0x08 != 0 {
		lat2 = -lat2
	}

	lon2 := int(h.buffer[23]&0x01)<<24 | int(h.buffer[24])<<16 | int(h.buffer[25])<<8 | int(h.buffer[26])
	if h.buffer[23]&0x02 != 0 {
		lon2 = -lon2
	}

	filenameLen := int(h.buffer[27])

	if h.payloadLen < 34+filenameLen {
		log.Printf("nrsc5: HERE Image frame too short")
		return
	}

	fileLen := int(h.buffer[32+filenameLen])<<8 | int(h.buffer[33+filenameLen])

	if h.payloadLen < 34+filenameLen+fileLen {
		log.Printf("nrsc5: HERE Image frame too short")
		return
	}

	timestampIndex := 0
	if imageType == HereImageTraffic {
		if n1 >= 1 && n1 <= HereTrafficTiles {
			timestampIndex = n1
		} else {
			log.Printf("nrsc5: invalid traffic tile number: %d", n1)
			return
		}
	}

	if h.lastTimestamp[timestampIndex] != timestamp {
		h.radio.reportHereImage(imageType, seq, n1, n2, timestamp,
			float32(lat1)/100000, float32(lon1)/100000,
			float32(lat2)/100000, float32(lon2)/100000,
			string(h.buffer[28:28+filenameLen]), uint(fileLen),
			h.buffer[34+filenameLen:34+filenameLen+fileLen])
		h.lastTimestamp[timestampIndex] = timestamp
	}
}

func (h *hereImages) push(seq uint16, length uint, buf []byte) {
	if int(seq) != h.expectedSeq {
		h.buffer = [2048]byte{}
		h.payloadLen = -1
		h.syncState = 0
	}

	for offset := uint(0); offset < length; offset++ {
		h.syncState <<= 8
		h.syncState |= uint32(buf[offset])

		if h.payloadLen == -1 { // waiting for sync
			if (h.syncState>>16)&0xffffffff == 0xfff7fff7&0xffffffff {
				h.payloadLen = int(h.syncState & 0xffff)
				h.bufferIdx = 0
			}
		} else {
			h.buffer[h.bufferIdx] = buf[offset]
			h.bufferIdx++
			if h.bufferIdx == h.payloadLen+2 {
				h.processPacket()
				h.payloadLen = -1
			}
		}
	}

	h.expectedSeq = (int(seq) + 1) & 0xffff
}
