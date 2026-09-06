package nrsc5

import (
	"log"
	"math"
)

// PIDS/SIS decoding, ported from pids.c.

const (
	alertTimeoutLimit = 16
	pidsTypeSIS       = 0
	pidsTypeLLDS      = 1

	sisMsgIDStationID                 = 0
	sisMsgIDStationNameShort          = 1
	sisMsgIDStationNameLong           = 2
	sisMsgIDStationLocation           = 4
	sisMsgIDStationMessage            = 5
	sisMsgIDServiceInformation        = 6
	sisMsgIDParameterMessage          = 7
	sisMsgIDUniversalShortStationName = 8
	sisMsgIDEmergencyAlertsMessage    = 9
	sisMsgIDAdvServiceInformation     = 10

	sisServiceCategoryAudio = 0
	sisServiceCategoryData  = 1

	sisEALocationFormatSame = 0
	sisEALocationFormatFIPS = 1
	sisEALocationFormatZIP  = 2
)

const (
	maxLongNameLen              = 56
	maxLongNameFrames           = 8
	maxMessageLen               = 190
	maxMessageFrames            = 32
	maxAudioServices            = 8
	maxDataServices             = 16
	numParameters               = 13
	maxUniversalShortNameLen    = 12
	maxUniversalShortNameFrames = 2
	maxSloganLen                = 95
	maxSloganFrames             = 16
	maxAlertLen                 = 381
	maxAlertFrames              = 64
	maxAlertCNTLen              = 63
	maxAlertLocations           = 31
)

const (
	encodingISO8859_1 = 0
	encodingUCS2      = 4
)

type asd struct {
	access   int
	typ      int
	soundExp int
}

type dsd struct {
	access   int
	typ      int
	mimeType int
}

var pidsChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ ?-*$ "

var payloadSizes = []int{
	32, 22, 58, 32, 27, 58, 27, 22,
	58, 58, 27, -1, -1, -1, -1, -1,
}

func crc12(bits []byte) uint16 {
	const poly = 0xD010
	reg := uint16(0x0000)

	for i := 67; i >= 0; i-- {
		lowbit := reg & 1
		reg >>= 1
		reg ^= uint16(bits[i]) << 15
		if lowbit != 0 {
			reg ^= poly
		}
	}
	for i := 0; i < 16; i++ {
		lowbit := reg & 1
		reg >>= 1
		if lowbit != 0 {
			reg ^= poly
		}
	}
	reg ^= 0x955
	return reg & 0xfff
}

func checkCRC12(bits []byte) bool {
	expectedCRC := uint16(0)
	for i := 68; i < 80; i++ {
		expectedCRC <<= 1
		expectedCRC |= uint16(bits[i])
	}
	return expectedCRC == crc12(bits)
}

func crc7(alert []byte, length int) int {
	const poly = 0x09
	reg := 0x42

	for byteIndex := length - 1; byteIndex >= 0; byteIndex-- {
		for bitIndex := 6; bitIndex >= 0; bitIndex-- {
			bit := (alert[byteIndex] >> bitIndex) & 1
			if bitIndex == 0 && byteIndex > 0 {
				bit ^= (alert[byteIndex-1] >> 7) & 1
			}

			reg <<= 1
			reg ^= int(bit)
			if reg&0x80 != 0 {
				reg ^= 0x80 | poly
			}
		}
	}

	for bitIndex := 6; bitIndex >= 0; bitIndex-- {
		reg <<= 1
		if reg&0x80 != 0 {
			reg ^= 0x80 | poly
		}
	}

	return reg
}

func controlDataCRC(controlData []byte, length int) int {
	const poly = 0xD010
	reg := 0x7E1B

	for byteIndex := length - 1; byteIndex >= 1; byteIndex-- {
		for bitIndex := 0; bitIndex < 8; bitIndex++ {
			bit := (controlData[byteIndex] >> bitIndex) & 1
			if byteIndex == 1 || (byteIndex == 2 && bitIndex < 4) {
				bit = 0 // skip CRC bits
			}

			lowbit := reg & 1
			reg >>= 1
			reg ^= int(bit) << 15
			if lowbit != 0 {
				reg ^= poly
			}
		}
	}

	for bitIndex := 0; bitIndex < 16; bitIndex++ {
		lowbit := reg & 1
		reg >>= 1
		if lowbit != 0 {
			reg ^= poly
		}
	}

	return reg & 0x0fff
}

func decodeInt(bits []byte, off *int, length uint) uint {
	var result uint
	for i := uint(0); i < length; i++ {
		result <<= 1
		result |= uint(bits[*off])
		*off++
	}
	return result
}

func decodeIntReverse(bits []byte, off *int, length uint) uint {
	var result uint
	for i := uint(0); i < length; i++ {
		result |= uint(bits[*off]) << i
		*off++
	}
	return result
}

func decodeSignedInt(bits []byte, off *int, length uint) int {
	result := int(decodeInt(bits, off, length))
	if result&(1<<(length-1)) != 0 {
		return result - (1 << length)
	}
	return result
}

func decodeChar5(bits []byte, off *int) byte {
	return pidsChars[decodeInt(bits, off, 5)]
}

func decodeChar7(bits []byte, off *int) byte {
	return byte(decodeInt(bits, off, 7))
}

func decodeLocations(bits []byte, length int, locations []int, locationFormat, numLocations int) bool {
	off := 0
	previousLocation := 0
	fullLen, compressedLen := 0, 0

	switch locationFormat {
	case sisEALocationFormatSame:
		fullLen, compressedLen = 20, 14
	case sisEALocationFormatFIPS, sisEALocationFormatZIP:
		fullLen, compressedLen = 17, 10
	default:
		log.Printf("nrsc5: invalid location format: %d", locationFormat)
		return false
	}

	for i := 0; i < numLocations; i++ {
		if off+1 > length {
			log.Printf("nrsc5: invalid location data")
			return false
		}

		if i == 0 || bits[off] != 0 {
			off++
			// Full-length location
			if off+fullLen > length {
				log.Printf("nrsc5: invalid location data")
				return false
			}
			locations[i] = int(decodeIntReverse(bits, &off, uint(fullLen)))
		} else {
			off++
			// Compressed location
			if off+compressedLen > length {
				log.Printf("nrsc5: invalid location data")
				return false
			}
			newDigits := int(decodeIntReverse(bits, &off, uint(compressedLen)))
			oldDigits := (previousLocation % 100000) - (previousLocation % 1000)
			locations[i] = ((newDigits / 1000) * 100000) + (newDigits % 1000) + oldDigits
		}

		previousLocation = locations[i]
	}

	return true
}

func decodeControlData(controlData []byte, length int) (category1, category2, locationFormat, numLocations int, locations []int) {
	bits := make([]byte, maxAlertCNTLen*8)
	off := 0

	for i := 0; i < length; i++ {
		for j := 0; j < 8; j++ {
			bits[i*8+j] = (controlData[i] >> j) & 1
		}
	}

	off += 8  // unknown
	off += 12 // CNT CRC
	off += 8  // unknown
	category1 = int(decodeIntReverse(bits, &off, 5))
	category2 = int(decodeIntReverse(bits, &off, 5))
	off += 9 // unknown
	locationFormat = int(decodeIntReverse(bits, &off, 3))
	numLocations = int(decodeIntReverse(bits, &off, 5))
	off++ // unknown

	locations = make([]int, maxAlertCNTLen)
	if !decodeLocations(bits[off:], length*8-off, locations, locationFormat, numLocations) {
		numLocations = 0
	}
	return
}

func utf8Encode(encoding int, buf []byte) string {
	switch encoding {
	case encodingISO8859_1:
		return iso8859ToUTF8(buf)
	case encodingUCS2:
		return ucs2ToUTF8(buf)
	default:
		log.Printf("nrsc5: invalid encoding: %d", encoding)
		return ""
	}
}

// pids holds SIS/PIDS decoder state (ported from pids_t).
type pids struct {
	input *input

	countryCode   [3]byte
	fccFacilityID int

	shortName [8]byte

	longName          [maxLongNameLen + 1]byte
	longNameHaveFrame [maxLongNameFrames]byte
	longNameSeq       int
	longNameDisplayed bool

	latitude  float32
	longitude float32
	altitude  int

	message          [maxMessageLen + 1]byte
	messageHaveFrame [maxMessageFrames]byte
	messageSeq       int
	messagePriority  int
	messageEncoding  int
	messageLen       int
	messageChecksum  uint
	messageDisplayed bool

	audioServices [maxAudioServices]asd
	dataServices  [maxDataServices]dsd

	parameters [numParameters]int

	universalShortName          [maxUniversalShortNameLen + 1]byte
	universalShortNameFinal     [maxUniversalShortNameLen + 4]byte
	universalShortNameHaveFrame [maxUniversalShortNameFrames]byte
	universalShortNameEncoding  int
	universalShortNameAppend    int
	universalShortNameLen       int
	universalShortNameDisplayed bool

	slogan          [maxSloganLen + 1]byte
	sloganHaveFrame [maxSloganFrames]byte
	sloganSeq       int
	sloganEncoding  int
	sloganLen       int
	sloganDisplayed bool

	alert          [maxAlertLen]byte
	alertHaveFrame [maxAlertFrames]byte
	alertSeq       int
	alertEncoding  int
	alertLen       int
	alertCRC       int
	alertCNTLen    int
	alertDisplayed bool
	alertTimeout   int
}

func newPids(input *input) *pids {
	st := &pids{}
	st.init(input)
	return st
}

func (st *pids) init(input *input) {
	st.input = input
	st.reset()
}

func (st *pids) reset() {
	st.fccFacilityID = -1

	st.longNameSeq = -1
	st.longNameDisplayed = false

	st.latitude = float32(math.NaN())
	st.longitude = float32(math.NaN())
	st.altitude = 0

	st.messageSeq = -1
	st.messageDisplayed = false

	for i := 0; i < maxAudioServices; i++ {
		st.audioServices[i].access = -1
		st.audioServices[i].typ = -1
		st.audioServices[i].soundExp = -1
	}
	for i := 0; i < maxDataServices; i++ {
		st.dataServices[i].access = -1
		st.dataServices[i].typ = -1
		st.dataServices[i].mimeType = -1
	}

	for i := 0; i < numParameters; i++ {
		st.parameters[i] = -1
	}

	st.universalShortNameAppend = -1
	st.universalShortNameLen = -1
	st.universalShortNameDisplayed = false

	st.sloganLen = -1
	st.sloganDisplayed = false

	st.resetAlert()
}

func (st *pids) resetAlert() {
	st.alert = [maxAlertLen]byte{}
	st.alertHaveFrame = [maxAlertFrames]byte{}
	st.alertSeq = -1
	st.alertDisplayed = false
	st.alertTimeout = 0
}

func (st *pids) report() {
	var countryCode string
	if st.countryCode[0] != 0 {
		countryCode = cstr(st.countryCode[:])
	}

	var name string
	if st.universalShortNameDisplayed {
		name = utf8Encode(st.universalShortNameEncoding, trimZeros(st.universalShortNameFinal[:]))
	} else if st.shortName[0] != 0 {
		name = cstr(st.shortName[:])
	}

	var slogan string
	if st.sloganDisplayed {
		slogan = utf8Encode(st.sloganEncoding, st.slogan[:st.sloganLen])
	} else if st.longNameDisplayed {
		slogan = iso8859ToUTF8(trimZeros(st.longName[:]))
	}

	var message string
	if st.messageDisplayed {
		message = utf8Encode(st.messageEncoding, st.message[:st.messageLen])
	}

	var alert string
	category1, category2 := -1, -1
	locationFormat, numLocations := -1, -1
	var locations []int
	if st.alertDisplayed {
		alert = utf8Encode(st.alertEncoding, st.alert[st.alertCNTLen:st.alertLen])
		category1, category2, locationFormat, numLocations, locations =
			decodeControlData(st.alert[:st.alertCNTLen], st.alertCNTLen)
	}

	latitude := float32(math.NaN())
	longitude := float32(math.NaN())
	altitude := 0
	if !math.IsNaN(float64(st.latitude)) && !math.IsNaN(float64(st.longitude)) {
		latitude = st.latitude
		longitude = st.longitude
		altitude = st.altitude
	}

	var audioServices []SISAudioService
	for i := maxAudioServices - 1; i >= 0; i-- {
		if st.audioServices[i].typ != -1 {
			audioServices = append([]SISAudioService{{
				Program:     uint(i),
				Access:      uint(st.audioServices[i].access),
				Type:        uint(st.audioServices[i].typ),
				SoundExpert: uint(st.audioServices[i].soundExp),
			}}, audioServices...)
		}
	}

	var dataServices []SISDataService
	for i := maxDataServices - 1; i >= 0; i-- {
		if st.dataServices[i].typ != -1 {
			dataServices = append([]SISDataService{{
				Access: uint(st.dataServices[i].access),
				Type:   uint(st.dataServices[i].typ),
				MIME:   uint32(st.dataServices[i].mimeType),
			}}, dataServices...)
		}
	}

	st.input.radio.reportSIS(&SISEvent{
		CountryCode:    countryCode,
		FCCFacilityID:  st.fccFacilityID,
		Name:           name,
		Slogan:         slogan,
		Message:        message,
		Alert:          alert,
		AlertCNT:       st.alert[:st.alertCNTLen],
		Category1:      category1,
		Category2:      category2,
		LocationFormat: locationFormat,
		NumLocations:   numLocations,
		Locations:      locations,
		Latitude:       latitude,
		Longitude:      longitude,
		Altitude:       altitude,
		AudioServices:  audioServices,
		DataServices:   dataServices,
	})
}

func cstr(buf []byte) string {
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

func (st *pids) sisDecodeStationID(bits []byte) bool {
	var countryCode [3]byte
	off := 0

	for j := 0; j < 2; j++ {
		countryCode[j] = decodeChar5(bits, &off)
	}
	off += 3 // reserved
	fccFacilityID := int(decodeInt(bits, &off, 19))

	if countryCode != st.countryCode || fccFacilityID != st.fccFacilityID {
		st.countryCode = countryCode
		st.fccFacilityID = fccFacilityID
		st.input.radio.reportStationID(cstr(st.countryCode[:]), st.fccFacilityID)
		return true
	}
	return false
}

func (st *pids) sisDecodeStationNameShort(bits []byte) bool {
	var shortName [8]byte
	off := 0

	for j := 0; j < 4; j++ {
		shortName[j] = decodeChar5(bits, &off)
	}
	if bits[off] == 0 && bits[off+1] == 1 {
		copy(shortName[4:], "-FM")
	}
	off += 2

	if shortName != st.shortName {
		st.shortName = shortName
		st.input.radio.reportStationName(cstr(st.shortName[:]))
		return true
	}
	return false
}

func (st *pids) sisDecodeStationLongName(bits []byte) bool {
	off := 0
	tmp := 55

	lastFrame := decodeInt(bits, &off, 3)
	currentFrame := decodeInt(bits, &off, 3)
	seq := int(decodeInt(bits, &tmp, 3))

	if currentFrame == 0 && seq != st.longNameSeq {
		st.longName = [maxLongNameLen + 1]byte{}
		st.longNameHaveFrame = [maxLongNameFrames]byte{}
		st.longNameSeq = seq
		st.longNameDisplayed = false
	}

	for j := 0; j < 7; j++ {
		st.longName[int(currentFrame)*7+j] = decodeChar7(bits, &off)
	}
	st.longNameHaveFrame[currentFrame] = 1

	if st.longNameSeq >= 0 && !st.longNameDisplayed {
		complete := true
		for j := uint(0); j <= lastFrame; j++ {
			complete = complete && st.longNameHaveFrame[j] != 0
		}

		if complete {
			st.longNameDisplayed = true

			if !st.sloganDisplayed {
				slogan := iso8859ToUTF8(trimZeros(st.longName[:]))
				st.input.radio.reportStationSlogan(slogan)
			}
			return true
		}
	}
	return false
}

func (st *pids) sisDecodeStationLocation(bits []byte) bool {
	off := 0

	if bits[off] != 0 {
		off++
		latitude := float32(decodeSignedInt(bits, &off, 22)) / 8192.0
		altitudeHigh := int(decodeInt(bits, &off, 4)) << 8
		if latitude != st.latitude || altitudeHigh != st.altitude&0xf00 {
			st.latitude = latitude
			st.altitude = (st.altitude & 0x0f0) | altitudeHigh
			if !math.IsNaN(float64(st.longitude)) {
				st.input.radio.reportStationLocation(st.latitude, st.longitude, st.altitude)
				return true
			}
		}
	} else {
		off++
		longitude := float32(decodeSignedInt(bits, &off, 22)) / 8192.0
		altitudeLow := int(decodeInt(bits, &off, 4)) << 4
		if longitude != st.longitude || altitudeLow != st.altitude&0x0f0 {
			st.longitude = longitude
			st.altitude = (st.altitude & 0xf00) | altitudeLow
			if !math.IsNaN(float64(st.latitude)) {
				st.input.radio.reportStationLocation(st.latitude, st.longitude, st.altitude)
				return true
			}
		}
	}
	return false
}

func (st *pids) sisDecodeStationMessage(bits []byte) bool {
	off := 0
	currentFrame := int(decodeInt(bits, &off, 5))
	seq := int(decodeInt(bits, &off, 2))

	if currentFrame == 0 {
		if seq != st.messageSeq {
			st.message = [maxMessageLen + 1]byte{}
			st.messageHaveFrame = [maxMessageFrames]byte{}
			st.messageSeq = seq
			st.messageDisplayed = false
		}
		st.messagePriority = int(bits[off])
		off++
		st.messageEncoding = int(decodeInt(bits, &off, 3))
		st.messageLen = int(decodeInt(bits, &off, 8))
		st.messageChecksum = decodeInt(bits, &off, 7)
		for j := 0; j < 4; j++ {
			st.message[j] = byte(decodeInt(bits, &off, 8))
		}
	} else {
		off += 3 // reserved
		for j := 0; j < 6; j++ {
			st.message[currentFrame*6-2+j] = byte(decodeInt(bits, &off, 8))
		}
	}
	st.messageHaveFrame[currentFrame] = 1

	if st.messageSeq >= 0 && !st.messageDisplayed {
		complete := true
		for j := 0; j < (st.messageLen+7)/6; j++ {
			complete = complete && st.messageHaveFrame[j] != 0
		}

		if complete {
			checksum := uint(0)
			for j := 0; j < st.messageLen; j++ {
				checksum += uint(st.message[j])
			}
			checksum = (((checksum >> 8) & 0x7f) + (checksum & 0xff)) & 0x7f

			if checksum == st.messageChecksum {
				st.messageDisplayed = true
				st.input.radio.reportStationMessage(utf8Encode(st.messageEncoding, st.message[:st.messageLen]))
				return true
			}
			log.Printf("nrsc5: invalid message checksum: %d != %d", st.messageChecksum, checksum)
		}
	}
	return false
}

func (st *pids) sisDecodeServiceInformation(bits []byte) bool {
	off := 0
	category := decodeInt(bits, &off, 2)

	switch category {
	case sisServiceCategoryAudio:
		access := int(decodeInt(bits, &off, 1))
		progNum := int(decodeInt(bits, &off, 6))
		typ := int(decodeInt(bits, &off, 8))
		off += 5 // reserved
		soundExp := int(decodeInt(bits, &off, 5))

		if progNum >= maxAudioServices {
			log.Printf("nrsc5: invalid program number: %d", progNum)
			break
		}

		if st.audioServices[progNum].access != access ||
			st.audioServices[progNum].typ != typ ||
			st.audioServices[progNum].soundExp != soundExp {
			st.audioServices[progNum] = asd{access: access, typ: typ, soundExp: soundExp}
			st.input.radio.reportASD(uint(progNum), uint(access), uint(typ), uint(soundExp))
			return true
		}
	case sisServiceCategoryData:
		access := int(decodeInt(bits, &off, 1))
		typ := int(decodeInt(bits, &off, 9))
		off += 3 // reserved
		mimeType := int(decodeInt(bits, &off, 12))

		for j := 0; j < maxDataServices; j++ {
			if st.dataServices[j].access == access &&
				st.dataServices[j].typ == typ &&
				st.dataServices[j].mimeType == mimeType {
				break
			} else if st.dataServices[j].typ == -1 {
				st.dataServices[j] = dsd{access: access, typ: typ, mimeType: mimeType}
				st.input.radio.reportDSD(uint(access), uint(typ), uint32(mimeType))
				return true
			}
		}
	default:
		log.Printf("nrsc5: unknown service category identifier: %d", category)
	}

	return false
}

func (st *pids) sisDecodeParameter(bits []byte) {
	off := 0

	index := decodeInt(bits, &off, 6)
	parameter := int(decodeInt(bits, &off, 16))

	if index >= numParameters {
		log.Printf("nrsc5: invalid parameter index: %d", index)
		return
	}

	if st.parameters[index] != parameter {
		st.parameters[index] = parameter
		switch index {
		case 0, 1, 2:
			if st.parameters[0] >= 0 && st.parameters[1] >= 0 && st.parameters[2] >= 0 {
				pendingOffset := st.parameters[0] >> 8
				currentOffset := st.parameters[0] & 0xff
				pendingALFN := uint(st.parameters[2])<<16 | uint(st.parameters[1])

				st.input.radio.reportLeapSecondOffset(pendingOffset, currentOffset, pendingALFN)
			}
		case 3:
			dstRegional := st.parameters[3] & 0x1
			dstLocal := (st.parameters[3] >> 1) & 0x1
			dstSchedule := (st.parameters[3] >> 2) & 0x7
			tzo := (st.parameters[3] >> 5) & 0x7ff

			if tzo >= 1024 {
				tzo -= 2048
			}

			st.input.radio.reportLocalTime(tzo, dstRegional, dstLocal, dstSchedule)
		case 4, 5, 6, 7:
			if st.parameters[4] >= 0 && st.parameters[5] >= 0 && st.parameters[6] >= 0 && st.parameters[7] >= 0 {
				manufacturerID := string([]byte{
					byte((st.parameters[4] >> 8) & 0x7f), byte(st.parameters[4] & 0x7f),
				})
				coreVersion := [4]int{
					(st.parameters[5] >> 11) & 0x1f, (st.parameters[5] >> 6) & 0x1f, (st.parameters[5] >> 1) & 0x1f,
					(st.parameters[7] >> 11) & 0x1f,
				}
				manufacturerVersion := [4]int{
					(st.parameters[6] >> 11) & 0x1f, (st.parameters[6] >> 6) & 0x1f, (st.parameters[6] >> 1) & 0x1f,
					(st.parameters[7] >> 6) & 0x1f,
				}

				coreStatus := (st.parameters[7] >> 3) & 0x7
				manufacturerStatus := st.parameters[7] & 0x7
				importerConnected := (st.parameters[4] >> 7) & 0x1

				st.input.radio.reportExciterInfo(manufacturerID, coreVersion, manufacturerVersion,
					coreStatus, manufacturerStatus, importerConnected)
			}
		case 8, 9, 10, 11:
			if st.parameters[8] >= 0 && st.parameters[9] >= 0 && st.parameters[10] >= 0 && st.parameters[11] >= 0 {
				manufacturerID := string([]byte{
					byte((st.parameters[8] >> 8) & 0x7f), byte(st.parameters[8] & 0x7f),
				})
				coreVersion := [4]int{
					(st.parameters[9] >> 11) & 0x1f, (st.parameters[9] >> 6) & 0x1f, (st.parameters[9] >> 1) & 0x1f,
					(st.parameters[11] >> 11) & 0x1f,
				}
				manufacturerVersion := [4]int{
					(st.parameters[10] >> 11) & 0x1f, (st.parameters[10] >> 6) & 0x1f, (st.parameters[10] >> 1) & 0x1f,
					(st.parameters[11] >> 6) & 0x1f,
				}
				coreStatus := (st.parameters[11] >> 3) & 0x7
				manufacturerStatus := st.parameters[11] & 0x7

				st.input.radio.reportImporterInfo(manufacturerID, coreVersion, manufacturerVersion,
					coreStatus, manufacturerStatus)
			}
		case 12:
			log.Printf("nrsc5: importer configuration number: %d", parameter)
		default:
			log.Printf("nrsc5: unknown SIS parameter index: %d", index)
		}
	}
}

func (st *pids) sisDecodeUniversalShortStationName(bits []byte) bool {
	off := 0

	currentFrame := decodeInt(bits, &off, 4)
	if bits[off] == 0 {
		off++
		if currentFrame >= maxUniversalShortNameFrames {
			log.Printf("nrsc5: unexpected frame number in Universal Short Station Name: %d", currentFrame)
			return false
		}

		if currentFrame == 0 {
			st.universalShortNameEncoding = int(decodeInt(bits, &off, 3))
			st.universalShortNameAppend = int(bits[off])
			off++
			st.universalShortNameLen = int(bits[off]) + 1
			off++
			for j := 0; j < 6; j++ {
				st.universalShortName[j] = byte(decodeInt(bits, &off, 8))
			}
		} else {
			off += 5 // reserved
			for j := 0; j < 6; j++ {
				st.universalShortName[int(currentFrame)*6+j] = byte(decodeInt(bits, &off, 8))
			}
		}
		st.universalShortNameHaveFrame[currentFrame] = 1

		if st.universalShortNameLen >= 0 && !st.universalShortNameDisplayed {
			complete := true
			for j := 0; j < st.universalShortNameLen; j++ {
				complete = complete && st.universalShortNameHaveFrame[j] != 0
			}

			if complete {
				copy(st.universalShortNameFinal[:], cstr(st.universalShortName[:]))
				final := cstr(st.universalShortName[:])
				if st.universalShortNameAppend != 0 {
					final += "-FM"
				}
				copy(st.universalShortNameFinal[:], final)
				st.universalShortNameDisplayed = true

				st.input.radio.reportStationName(utf8Encode(st.universalShortNameEncoding, []byte(final)))
				return true
			}
		}
	} else {
		off++
		if currentFrame == 0 {
			st.sloganEncoding = int(decodeInt(bits, &off, 3))
			off += 3 // reserved
			st.sloganLen = int(decodeInt(bits, &off, 7))
			for j := 0; j < 5; j++ {
				st.slogan[j] = byte(decodeInt(bits, &off, 8))
			}
		} else {
			off += 5 // reserved
			for j := 0; j < 6; j++ {
				st.slogan[int(currentFrame)*6-1+j] = byte(decodeInt(bits, &off, 8))
			}
		}
		st.sloganHaveFrame[currentFrame] = 1

		if st.sloganLen >= 0 && !st.sloganDisplayed {
			complete := true
			for j := 0; j < (st.sloganLen+6)/6; j++ {
				complete = complete && st.sloganHaveFrame[j] != 0
			}

			if complete {
				st.sloganDisplayed = true

				if !st.longNameDisplayed {
					st.input.radio.reportStationSlogan(utf8Encode(st.sloganEncoding, st.slogan[:st.sloganLen]))
				}
				return true
			}
		}
	}

	return false
}

func (st *pids) sisDecodeEmergencyAlerts(bits []byte) bool {
	off := 0

	currentFrame := int(decodeInt(bits, &off, 6))
	seq := int(decodeInt(bits, &off, 2))
	off += 2 // reserved

	st.alertTimeout = 0

	if currentFrame == 0 {
		if seq != st.alertSeq {
			st.alert = [maxAlertLen]byte{}
			st.alertHaveFrame = [maxAlertFrames]byte{}
			st.alertSeq = seq
			st.alertDisplayed = false
		}
		st.alertEncoding = int(decodeInt(bits, &off, 3))
		st.alertLen = int(decodeInt(bits, &off, 9))
		st.alertCRC = int(decodeInt(bits, &off, 7))
		st.alertCNTLen = 1 + 2*int(decodeInt(bits, &off, 5))
		for j := 0; j < 3; j++ {
			st.alert[j] = byte(decodeInt(bits, &off, 8))
		}
	} else {
		for j := 0; j < 6; j++ {
			st.alert[currentFrame*6-3+j] = byte(decodeInt(bits, &off, 8))
		}
	}
	st.alertHaveFrame[currentFrame] = 1

	if st.alertLen >= 0 && !st.alertDisplayed {
		complete := true
		for j := 0; j < (st.alertLen+8)/6; j++ {
			complete = complete && st.alertHaveFrame[j] != 0
		}

		if complete {
			expectedAlertCRC := crc7(st.alert[:st.alertLen], st.alertLen)
			if st.alertCRC != expectedAlertCRC {
				log.Printf("nrsc5: invalid alert CRC: %#02x != %#02x", st.alertCRC, expectedAlertCRC)
				return false
			}

			if st.alertCNTLen < 7 || st.alertLen < st.alertCNTLen {
				log.Printf("nrsc5: invalid alert CNT length")
				return false
			}

			actualCNTCRC := (int(st.alert[2]&0x0f) << 8) | int(st.alert[1])
			expectedCNTCRC := controlDataCRC(st.alert[:st.alertCNTLen], st.alertCNTLen)
			if actualCNTCRC == expectedCNTCRC {
				st.alertDisplayed = true

				message := utf8Encode(st.alertEncoding, st.alert[st.alertCNTLen:st.alertLen])
				category1, category2, locationFormat, numLocations, locations :=
					decodeControlData(st.alert[:st.alertCNTLen], st.alertCNTLen)
				st.input.radio.reportEmergencyAlert(&EASEvent{
					Message:        message,
					ControlData:    st.alert[:st.alertCNTLen],
					Category1:      category1,
					Category2:      category2,
					LocationFormat: locationFormat,
					NumLocations:   numLocations,
					Locations:      locations,
				})
				return true
			}
			log.Printf("nrsc5: invalid CNT CRC: %#03x != %#03x", actualCNTCRC, expectedCNTCRC)
		}
	}

	return false
}

func (st *pids) sisDecode(bits []byte) {
	off := 0
	updated := false

	payloads := int(bits[0]) + 1
	off++

	if st.alertDisplayed {
		st.alertTimeout++
	}

	for i := 0; i < payloads; i++ {
		if off > 59 {
			break
		}
		msgID := decodeInt(bits, &off, 4)
		if int(msgID) >= len(payloadSizes) {
			log.Printf("nrsc5: unexpected msg_id: %d", msgID)
			break
		}
		payloadSize := payloadSizes[msgID]

		if payloadSize == -1 {
			log.Printf("nrsc5: unexpected msg_id: %d", msgID)
			break
		}

		if off > 63-payloadSize {
			log.Printf("nrsc5: not enough room for SIS payload, msg_id: %d", msgID)
			break
		}

		switch msgID {
		case sisMsgIDStationID:
			if st.sisDecodeStationID(bits[off:]) {
				updated = true
			}
			off += 32
		case sisMsgIDStationNameShort:
			if st.sisDecodeStationNameShort(bits[off:]) {
				updated = true
			}
			off += 22
		case sisMsgIDStationNameLong:
			if st.sisDecodeStationLongName(bits[off:]) {
				updated = true
			}
			off += 58
		case 3:
			// reserved
			off += 32
		case sisMsgIDStationLocation:
			if st.sisDecodeStationLocation(bits[off:]) {
				updated = true
			}
			off += 27
		case sisMsgIDStationMessage:
			if st.sisDecodeStationMessage(bits[off:]) {
				updated = true
			}
			off += 58
		case sisMsgIDServiceInformation, sisMsgIDAdvServiceInformation:
			if st.sisDecodeServiceInformation(bits[off:]) {
				updated = true
			}
			off += 27
		case sisMsgIDParameterMessage:
			st.sisDecodeParameter(bits[off:])
			off += 22
		case sisMsgIDUniversalShortStationName:
			if st.sisDecodeUniversalShortStationName(bits[off:]) {
				updated = true
			}
			off += 58
		case sisMsgIDEmergencyAlertsMessage:
			if st.sisDecodeEmergencyAlerts(bits[off:]) {
				updated = true
			}
			off += 58
		default:
			log.Printf("nrsc5: unknown SIS MSG ID: %d", msgID)
		}
	}

	if st.alertDisplayed && st.alertTimeout >= alertTimeoutLimit {
		st.resetAlert()
		st.input.radio.reportEmergencyAlert(nil)
		updated = true
	}

	if updated {
		st.report()
	}
}

func (st *pids) framePush(bits []byte) {
	pids := make([]byte, PIDS_FRAME_LEN)

	for i := 0; i < PIDS_FRAME_LEN; i++ {
		pids[i] = bits[((i>>3)<<3)+7-(i&7)]
	}

	if checkCRC12(pids) {
		typ := pids[0]

		if typ == pidsTypeSIS {
			st.sisDecode(pids[1:])
		}
	}
}
