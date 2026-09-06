package nrsc5

import "time"

// Event types delivered to the caller-supplied callback.
type EventType int

const (
	EventTypeIQ EventType = iota
	EventTypeSync
	EventTypeLostSync
	EventTypeLostDevice
	EventTypeMER
	EventTypeBER
	EventTypeAudio
	EventTypePacket
	EventTypeStream
	EventTypeLOT
	EventTypeLOTFragment
	EventTypeSIG
	EventTypeSIS
	EventTypeEmergencyAlert
	EventTypeStationID
	EventTypeStationName
	EventTypeStationSlogan
	EventTypeStationMessage
	EventTypeStationLocation
	EventTypeASD
	EventTypeDSD
	EventTypeLeapSecondOffset
	EventTypeLocalTime
	EventTypeExciterInfo
	EventTypeImporterInfo
	EventTypeAudioService
	EventTypeID3
	EventTypeHDC
	EventTypeHereImage
)

// Event mirrors nrsc5_event_t.
type Event struct {
	Type EventType

	// IQ
	IQ []byte

	// Sync
	FreqOffset float32
	PSMI       int
	PLI        int
	HPPI       int
	AABI       int
	RDBI       int

	// MER
	Lower, Upper float32

	// BER
	CBER float32

	// Audio
	Program uint
	Audio   []int16

	// HDC packet
	HDC      []byte
	HDCFlags uint

	// Data
	Seq       uint16
	Size      uint
	Data      []byte
	Component *SigComponent

	// LOT
	Lot         uint
	MIME        uint32
	Name        string
	ExpiryUTC   string
	BytesSoFar  uint
	Repeat      uint
	LotEvent    int
	LotSeq      uint
	IsDuplicate bool

	// SIG table
	Services []SigService

	// SIS
	SIS *SISEvent
	EA  *EASEvent

	CountryCode   string
	FCCFacilityID int
	Slogan        string
	Message       string
	Latitude      float32
	Longitude     float32
	Altitude      int
	ASD           SISAudioService
	DSD           SISDataService

	AudioService AudioServiceEvent
	ID3          *ID3Event

	ImageType              int
	ImageSeq               int
	N1, N2                 int
	Timestamp              uint32
	Lat1, Lon1, Lat2, Lon2 float32
}

// AudioServiceEvent describes an audio service announcement.
type AudioServiceEvent struct {
	Program          uint
	Access           uint
	Type             uint
	CodecMode        uint
	BlendControl     uint
	DigitalAudioGain int
	CommonDelay      uint
	Latency          uint
}

// SigService mirrors nrsc5_sig_service_t (SIG record for a channel).
type SigService struct {
	Type       uint
	Number     uint
	Name       string
	Components []SigComponent
}

// SigComponent mirrors nrsc5_sig_component_t.
type SigComponent struct {
	Type uint
	ID   uint

	AudioComponent *SigAudioComponent
	DataComponent  *SigDataComponent
}

// SigAudioComponent mirrors the audio union member.
type SigAudioComponent struct {
	Port uint8
	Type uint8
	MIME uint32
}

// SigDataComponent mirrors the data union member.
type SigDataComponent struct {
	Port            uint16
	ServiceDataType uint16
	Type            uint8
	MIME            uint32
}

// SISAudioService / SISDataService (nrsc5_sis_asd_t / nrsc5_sis_dsd_t).
type SISAudioService struct {
	Program     uint
	Access      uint
	Type        uint
	SoundExpert uint
}

type SISDataService struct {
	Access uint
	Type   uint
	MIME   uint32
}

// SISEvent mirrors the nrsc5_sis fields of nrsc5_event_t.
type SISEvent struct {
	CountryCode    string
	FCCFacilityID  int
	Name           string
	Slogan         string
	Message        string
	Alert          string
	AlertCNT       []byte
	Category1      int
	Category2      int
	LocationFormat int
	NumLocations   int
	Locations      []int
	Latitude       float32
	Longitude      float32
	Altitude       int
	AudioServices  []SISAudioService
	DataServices   []SISDataService
}

// EASEvent carries emergency alert data.
type EASEvent struct {
	Message        string
	ControlData    []byte
	ControlDataLen int
	Category1      int
	Category2      int
	LocationFormat int
	NumLocations   int
	Locations      []int
}

// ID3Comment is a COMM frame parsed from an ID3 tag.
type ID3Comment struct {
	Lang             string
	ShortContentDesc string
	FullText         string
}

// ID3Event carries decoded ID3 metadata (NRSC5_EVENT_ID3).
type ID3Event struct {
	Program uint
	Title   string
	Artist  string
	Album   string
	Genre   string

	UFIDOwner string
	UFIDID    string

	XHRDMime  uint32
	XHRDParam int
	XHRDLot   int

	Price       string
	ContactURL  string
	Seller      string
	Description string
	ReceivedAs  byte
	ValidUntil  time.Time

	Comments []ID3Comment
}
