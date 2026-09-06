package nrsc5

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// Pure-Go rtl_tcp client, ported from rtltcp.c.

// Tuner types (from librtlsdr rtl-sdr.h).
const (
	tunerE4000  = 1
	tunerFC0012 = 2
	tunerFC0013 = 3
	tunerFC2580 = 4
	tunerR820T  = 5
	tunerR828D  = 6
)

// rtl_tcp command opcodes.
const (
	rtltcpSetCenterFreq     = 0x01
	rtltcpSetSampleRate     = 0x02
	rtltcpSetTunerGainMode  = 0x03
	rtltcpSetTunerGain      = 0x04
	rtltcpSetFreqCorrection = 0x05
	rtltcpSetDirectSampling = 0x09
	rtltcpSetOffsetTuning   = 0x0a
	rtltcpSetBiasTee        = 0x0e
	rtltcpDongleID          = 0x0f
	rtltcpSetAGCMode        = 0x08
)

type rtltcp struct {
	conn      net.Conn
	tunerType uint32
	gainCount uint32
}

// dialRTLTCP connects to an rtl_tcp server and reads the dongle info.
func dialRTLTCP(addr string, timeout time.Duration) (*rtltcp, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	st := &rtltcp{conn: conn}

	// dongle_info_t: magic[4] tuner_type tuner_gain_count (network order)
	var info [12]byte
	if err := st.readFull(info[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("rtl_tcp: reading dongle info: %w", err)
	}
	if string(info[:4]) != "RTL0" {
		conn.Close()
		return nil, fmt.Errorf("rtl_tcp: bad magic %q", info[:4])
	}
	st.tunerType = binary.BigEndian.Uint32(info[4:8])
	st.gainCount = binary.BigEndian.Uint32(info[8:12])
	return st, nil
}

func (st *rtltcp) readFull(buf []byte) error {
	off := 0
	for off < len(buf) {
		n, err := st.conn.Read(buf[off:])
		if err != nil {
			return err
		}
		off += n
	}
	return nil
}

// sendCmd writes a 5-byte command: 1-byte opcode + 4-byte big-endian param.
func (st *rtltcp) sendCmd(opc byte, param uint32) error {
	var cmd [5]byte
	cmd[0] = opc
	binary.BigEndian.PutUint32(cmd[1:], param)
	_, err := st.conn.Write(cmd[:])
	return err
}

func (st *rtltcp) setCenterFreq(freq uint32) error { return st.sendCmd(rtltcpSetCenterFreq, freq) }
func (st *rtltcp) setSampleRate(rate uint32) error { return st.sendCmd(rtltcpSetSampleRate, rate) }
func (st *rtltcp) setTunerGainMode(mode uint32) error {
	return st.sendCmd(rtltcpSetTunerGainMode, mode)
}
func (st *rtltcp) setTunerGain(gain uint32) error { return st.sendCmd(rtltcpSetTunerGain, gain) }
func (st *rtltcp) setFreqCorrection(ppm uint32) error {
	return st.sendCmd(rtltcpSetFreqCorrection, ppm)
}
func (st *rtltcp) setDirectSampling(on uint32) error {
	return st.sendCmd(rtltcpSetDirectSampling, on)
}
func (st *rtltcp) setOffsetTuning(on uint32) error { return st.sendCmd(rtltcpSetOffsetTuning, on) }
func (st *rtltcp) setBiasTee(on uint32) error      { return st.sendCmd(rtltcpSetBiasTee, on) }

// read samples into buf; returns bytes read (0 on EOF).
func (st *rtltcp) read(buf []byte) (int, error) {
	return st.conn.Read(buf)
}

// getTunerGains returns the supported gain values in tenths of a dB,
// from https://github.com/steve-m/librtlsdr/blob/master/src/librtlsdr.c
func (st *rtltcp) getTunerGains() []int {
	e4kGains := []int{-10, 15, 40, 65, 90, 115, 140, 165, 190, 215,
		240, 290, 340, 420}
	fc0012Gains := []int{-99, -40, 71, 179, 192}
	fc0013Gains := []int{-99, -73, -65, -63, -60, -58, -54, 58, 61,
		63, 65, 67, 68, 70, 71, 179, 181, 182,
		184, 186, 188, 191, 197}
	r82xxGains := []int{0, 9, 14, 27, 37, 77, 87, 125, 144, 157,
		166, 197, 207, 229, 254, 280, 297, 328,
		338, 364, 372, 386, 402, 421, 434, 439,
		445, 480, 496}

	switch st.tunerType {
	case tunerE4000:
		return e4kGains
	case tunerFC0012:
		return fc0012Gains
	case tunerFC0013:
		return fc0013Gains
	case tunerFC2580:
		return nil
	case tunerR820T, tunerR828D:
		return r82xxGains
	default:
		return nil
	}
}

// resetBuffer drains pending data, then reads cnt bytes to realign
// the stream after a gain change (from rtltcp.c).
func (st *rtltcp) resetBuffer(cnt int) error {
	st.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1024)
	recvd := 0
	for {
		n, err := st.conn.Read(buf)
		if err != nil {
			break
		}
		recvd += n
	}
	st.conn.SetReadDeadline(time.Time{})
	// if we read an odd number of bytes, read one more
	if recvd&1 != 0 {
		st.conn.SetReadDeadline(time.Now().Add(time.Second))
		st.conn.Read(buf[:1])
		st.conn.SetReadDeadline(time.Time{})
	}
	// then read cnt bytes
	remaining := cnt
	for remaining > 0 {
		toRead := len(buf)
		if remaining < toRead {
			toRead = remaining
		}
		st.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := st.conn.Read(buf[:toRead])
		if err != nil || n <= 0 {
			return fmt.Errorf("rtl_tcp: reset read failed: %w", err)
		}
		remaining -= n
	}
	st.conn.SetReadDeadline(time.Time{})
	return nil
}

func (st *rtltcp) close() {
	st.conn.Close()
}
