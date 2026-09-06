package nrsc5

import "time"

func sigComponentToPublic(c *sigComponent) *SigComponent {
	pc := &SigComponent{Type: uint(c.typ), ID: uint(c.id)}
	if c.typ == SigComponentAudio {
		pc.AudioComponent = &SigAudioComponent{Port: c.audioPort, Type: c.audioType, MIME: c.audioMime}
	} else if c.typ == SigComponentData {
		pc.DataComponent = &SigDataComponent{Port: c.port, ServiceDataType: c.serviceDataType, Type: c.dataType, MIME: c.mime}
	}
	return pc
}

func (r *Radio) reportIQ(data []byte) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeIQ, IQ: data})
	}
}

func (r *Radio) reportLostSync() {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeLostSync})
	}
}

func (r *Radio) reportSync(freqOffset float32, psmi, pli, hppi, aabi, rdbi int) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeSync, FreqOffset: freqOffset, PSMI: psmi, PLI: pli, HPPI: hppi, AABI: aabi, RDBI: rdbi})
	}
}

func (r *Radio) reportMER(lower, upper float32) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeMER, Lower: lower, Upper: upper})
	}
}

func (r *Radio) reportBER(cber float32) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeBER, CBER: cber})
	}
}

func (r *Radio) reportAudio(program uint, samples []int16) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeAudio, Program: program, Audio: samples})
	}
}

func (r *Radio) reportHDC(program uint, data []byte, flags uint) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeHDC, Program: program, HDC: append([]byte(nil), data...)})
	}
}

func (r *Radio) reportSIS(e *SISEvent) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeSIS, SIS: e})
	}
}

func (r *Radio) reportEmergencyAlert(e *EASEvent) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeEmergencyAlert, EA: e})
	}
}

func (r *Radio) reportStationID(countryCode string, fccFacilityID int) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeStationID, CountryCode: countryCode, FCCFacilityID: fccFacilityID})
	}
}

func (r *Radio) reportStationName(name string) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeStationName, Message: name})
	}
}

func (r *Radio) reportStationSlogan(slogan string) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeStationSlogan, Slogan: slogan})
	}
}

func (r *Radio) reportStationMessage(message string) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeStationMessage, Message: message})
	}
}

func (r *Radio) reportStationLocation(latitude, longitude float32, altitude int) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeStationLocation, Latitude: latitude, Longitude: longitude, Altitude: altitude})
	}
}

func (r *Radio) reportASD(program, access, typ, soundExpert uint) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeASD, ASD: SISAudioService{Program: program, Access: access, Type: typ, SoundExpert: soundExpert}})
	}
}

func (r *Radio) reportDSD(access, typ uint, mime uint32) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeDSD, DSD: SISDataService{Access: access, Type: typ, MIME: mime}})
	}
}

func (r *Radio) reportLeapSecondOffset(pendingOffset, currentOffset int, pendingALFN uint) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeLeapSecondOffset})
	}
}

func (r *Radio) reportLocalTime(tzo, dstRegional, dstLocal, dstSchedule int) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeLocalTime})
	}
}

func (r *Radio) reportExciterInfo(manufacturerID string, coreVersion, manufacturerVersion [4]int, coreStatus, manufacturerStatus, importerConnected int) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeExciterInfo})
	}
}

func (r *Radio) reportImporterInfo(manufacturerID string, coreVersion, manufacturerVersion [4]int, coreStatus, manufacturerStatus int) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeImporterInfo})
	}
}

func (r *Radio) reportAudioService(program, access, typ, codecMode, blendControl uint, digitalAudioGain int, commonDelay, latency uint) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeAudioService, AudioService: AudioServiceEvent{
			Program: program, Access: access, Type: typ, CodecMode: codecMode,
			BlendControl: blendControl, DigitalAudioGain: digitalAudioGain,
			CommonDelay: commonDelay, Latency: latency}})
	}
}

func (r *Radio) reportID3(e *ID3Event) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeID3, ID3: e})
	}
}

func (r *Radio) reportSIG(services []SigService) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeSIG, Services: services})
	}
}

func (r *Radio) reportStream(seq uint16, size uint, data []byte, component *sigComponent) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeStream, Seq: seq, Size: size, Data: append([]byte(nil), data...), Component: sigComponentToPublic(component)})
	}
}

func (r *Radio) reportPacket(seq uint16, size uint, data []byte, component *sigComponent) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypePacket, Seq: seq, Size: size, Data: append([]byte(nil), data...), Component: sigComponentToPublic(component)})
	}
}

func (r *Radio) reportLOTLot(kind int, file *aasFile) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeLOT, LotEvent: kind, Lot: uint(file.lot), MIME: file.mime,
			Name: file.name, Size: uint(file.size), Data: file.data,
			ExpiryUTC: file.expiryUTC.Format(time.RFC3339)})
	}
}

func (r *Radio) reportLOTFragment(file *aasFile, seq uint, repeat byte, isDuplicate bool, size uint, bytesSoFar uint32, data []byte) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeLOTFragment, Lot: uint(file.lot), LotSeq: seq, Repeat: uint(repeat),
			IsDuplicate: isDuplicate, Size: size, BytesSoFar: uint(bytesSoFar), Data: append([]byte(nil), data...), MIME: file.mime, Name: file.name})
	}
}

func (r *Radio) reportHereImage(imageType, seq, n1, n2 int, timestamp uint32, lat1, lon1, lat2, lon2 float32, name string, size uint, data []byte) {
	if r.callback != nil {
		r.callback(&Event{Type: EventTypeHereImage, ImageType: imageType, ImageSeq: seq, N1: n1, N2: n2,
			Timestamp: timestamp, Lat1: lat1, Lon1: lon1, Lat2: lat2, Lon2: lon2,
			Name: name, Size: size, Data: append([]byte(nil), data...)})
	}
}
