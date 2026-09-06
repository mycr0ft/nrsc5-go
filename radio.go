package nrsc5

import (
	"io"
	"log"
	gosync "sync"
)

// Radio is the top-level receiver (ported from nrsc5.c / nrsc5_t).

type Radio struct {
	mode int

	callback func(*Event)

	iq  *input
	out *output

	// file input state
	iqReader io.Reader

	// rtl_tcp input state
	rtltcp  *rtltcp
	tcpAddr string
	useTCP  bool

	samplesBuf [128 * 256]byte

	freq      float32
	gain      float32
	autoGain  bool
	stopped   bool
	pause     bool
	closed    bool
	stateMu   gosync.Mutex
	stateCond *gosync.Cond
	workerWG  gosync.WaitGroup

	leftoverU8   [4]byte
	leftoverU8N  int
	leftoverS16  [2]int16
	leftoverS16N int
}

// NewRadio creates a receiver that reads IQ samples via PipeSamples calls
// (equivalent of nrsc5_open_pipe).
func NewRadio() *Radio {
	st := &Radio{
		mode:     ModeFM,
		autoGain: true,
		gain:     -1,
		stopped:  true,
	}
	st.stateCond = gosync.NewCond(&st.stateMu)
	st.out = newOutput(st)
	st.iq = newInput(st, st.out)
	return st
}

// NewRadioFile creates a receiver that reads cu8 IQ samples from a reader.
func NewRadioFile(r io.Reader) *Radio {
	st := NewRadio()
	st.iqReader = r
	return st
}

// NewRadioRTLTCP creates a receiver connected to an rtl_tcp server,
// e.g. "localhost:1234".
func NewRadioRTLTCP(addr string) (*Radio, error) {
	tcp, err := dialRTLTCP(addr, 5e9)
	if err != nil {
		return nil, err
	}
	if err := tcp.setSampleRate(SAMPLE_RATE_CU8); err != nil {
		tcp.close()
		return nil, err
	}
	if err := tcp.setTunerGainMode(1); err != nil {
		tcp.close()
		return nil, err
	}
	if err := tcp.setOffsetTuning(1); err != nil {
		tcp.close()
		return nil, err
	}
	st := NewRadio()
	st.useTCP = true
	st.tcpAddr = addr
	st.rtltcp = tcp
	st.startWorker()
	return st, nil
}

// startWorker spawns the receive loop for device-backed inputs
// (worker_thread in nrsc5.c).
func (st *Radio) startWorker() {
	st.workerWG.Add(1)
	go func() {
		defer st.workerWG.Done()
		for {
			// wait until running or closed
			st.stateMu.Lock()
			for !st.closed && (st.stopped || st.pause) {
				st.stateCond.Wait()
			}
			if st.closed {
				st.stateMu.Unlock()
				return
			}
			st.stateMu.Unlock()

			if st.useTCP && st.autoGain && st.gain < 0 {
				if !st.doAutoGain() {
					st.stateMu.Lock()
					st.stopped = true
					st.stateMu.Unlock()
					st.reportLostDevice()
					continue
				}
			}

			// read samples (outside the state lock)
			if st.useTCP {
				n, rerr := st.rtltcp.read(st.samplesBuf[:])
				st.stateMu.Lock()
				closed := st.closed
				stopped := st.stopped
				st.stateMu.Unlock()
				if closed || stopped {
					continue
				}
				if rerr != nil || n < 4 {
					st.stateMu.Lock()
					st.stopped = true
					st.stateMu.Unlock()
					st.reportLostDevice()
					continue
				}
				// a short read is possible at EOF and may be unaligned
				st.iq.pushCU8(st.samplesBuf[:n&^3])
			}
		}
	}()
}

// SetCallback registers the event callback.
func (st *Radio) SetCallback(cb func(*Event)) {
	st.callback = cb
}

// SetMode switches between FM and AM (NRSC5_MODE_*).
func (st *Radio) SetMode(mode int) bool {
	if mode == ModeFM || mode == ModeAM {
		st.mode = mode
		st.iq.setMode()
		return true
	}
	return false
}

// Reset reinitializes the decoder state.
func (st *Radio) Reset() {
	st.iq.reset()
	st.out.reset()
}

// Start begins processing.
func (st *Radio) Start() {
	st.stateMu.Lock()
	st.stopped = false
	st.pause = false
	st.stateCond.Broadcast()
	st.stateMu.Unlock()
}

// Stop pauses processing.
func (st *Radio) Stop() {
	st.stateMu.Lock()
	st.stopped = true
	st.stateCond.Broadcast()
	st.stateMu.Unlock()
}

// Close shuts down the receiver and releases the device.
func (st *Radio) Close() {
	if st.useTCP {
		st.stateMu.Lock()
		st.closed = true
		st.stateCond.Broadcast()
		st.stateMu.Unlock()
		st.workerWG.Wait()
		st.rtltcp.close()
	}
}

// Run processes the entire IQ file (blocking). Reports go to the callback.
func (st *Radio) Run() {
	if st.iqReader == nil {
		return
	}
	buf := st.samplesBuf[:]
	for {
		n, err := st.iqReader.Read(buf)
		if n > 0 {
			st.iq.pushCU8(buf[:n&^3])
		}
		if err != nil {
			st.reportLostDevice()
			return
		}
	}
}

// SetFrequency tunes the device to the given center frequency in Hz.
// Only for device-backed inputs; must be called while stopped.
func (st *Radio) SetFrequency(freq float32) bool {
	st.stateMu.Lock()
	if st.freq == freq || !st.stopped {
		st.stateMu.Unlock()
		return st.freq == freq
	}
	st.pause = true
	st.stateMu.Unlock()
	defer func() {
		st.stateMu.Lock()
		st.pause = false
		st.stateMu.Unlock()
	}()
	if st.useTCP {
		if err := st.rtltcp.setCenterFreq(uint32(freq)); err != nil {
			return false
		}
	}
	st.Reset()
	st.freq = freq
	return true
}

// SetGain sets the tuner gain in dB (device inputs only).
func (st *Radio) SetGain(gain float32) bool {
	st.stateMu.Lock()
	if st.gain == gain || !st.stopped {
		st.stateMu.Unlock()
		return st.gain == gain
	}
	st.pause = true
	st.stateMu.Unlock()
	defer func() {
		st.stateMu.Lock()
		st.pause = false
		st.stateMu.Unlock()
	}()
	if st.useTCP {
		if err := st.rtltcp.setTunerGain(uint32(gain * 10)); err != nil {
			return false
		}
	}
	st.gain = gain
	return true
}

// SetAutoGain enables/disables automatic gain selection.
func (st *Radio) SetAutoGain(enabled bool) {
	st.autoGain = enabled
	st.gain = -1
}

// doAutoGain performs binary-search gain selection (do_auto_gain in nrsc5.c).
func (st *Radio) doAutoGain() bool {
	gains := st.rtltcp.getTunerGains()
	if len(gains) == 0 {
		return false
	}
	bestGain := 0
	var bestAmpDB float32

	low, high := 0, len(gains)-1
	for low <= high {
		mid := (low + high) / 2
		gain := gains[mid]

		if st.rtltcp.setTunerGain(uint32(gain)) != nil {
			continue
		}
		// dump 250ms of samples and hope for the best
		st.rtltcp.resetBuffer((SAMPLE_RATE_CU8 / 4) * 2)

		buf := st.samplesBuf[:]
		n, rerr := st.rtltcp.read(buf)
		if rerr != nil {
			return false
		}
		var maxSample, minSample byte
		maxSample = 0
		minSample = 255
		for j := n / 4; j < n; j++ {
			if buf[j] > maxSample {
				maxSample = buf[j]
			}
			if buf[j] < minSample {
				minSample = buf[j]
			}
		}
		ampDB := 20 * log10f(float32(maxSample-minSample+1)/256.0)

		if ampDB < -6.0 {
			bestGain = gain
			bestAmpDB = ampDB
			low = mid + 1
		} else {
			high = mid - 1
		}

		if high == -1 {
			bestGain = gain
			bestAmpDB = ampDB
		}
	}
	st.gain = float32(bestGain) / 10
	st.rtltcp.setTunerGain(uint32(bestGain))
	_ = bestAmpDB
	return true
}

// PipeSamplesCU8 feeds cu8 IQ samples (arbitrary length).
func (st *Radio) PipeSamplesCU8(samples []byte) {
	for st.leftoverU8N > 0 && len(samples) > 0 {
		st.leftoverU8[st.leftoverU8N] = samples[0]
		st.leftoverU8N++
		samples = samples[1:]

		if st.leftoverU8N == 4 {
			st.iq.pushCU8(st.leftoverU8[:4])
			st.leftoverU8N = 0
			break
		}
	}

	sampleGroups := len(samples) / 4
	st.iq.pushCU8(samples[:sampleGroups*4])
	samples = samples[sampleGroups*4:]

	for len(samples) > 0 {
		st.leftoverU8[st.leftoverU8N] = samples[0]
		st.leftoverU8N++
		samples = samples[1:]
	}
}

// PipeSamplesCS16 feeds cs16 IQ samples (arbitrary length).
func (st *Radio) PipeSamplesCS16(samples []int16) {
	if st.leftoverS16N == 1 && len(samples) > 0 {
		st.leftoverS16[1] = samples[0]
		samples = samples[1:]

		st.iq.pushCS16(st.leftoverS16[:2])
		st.leftoverS16N = 0
	}

	sampleGroups := len(samples) / 2
	st.iq.pushCS16(samples[:sampleGroups*2])
	samples = samples[sampleGroups*2:]

	if len(samples) == 1 {
		st.leftoverS16[0] = samples[0]
		st.leftoverS16N = 1
	}
}

// PipeSamplesCF32 feeds cf32 IQ samples (arbitrary length).
func (st *Radio) PipeSamplesCF32(samples []float32) {
	st.iq.pushCF32(samples)
}

func (st *Radio) reportLostDevice() {
	if st.callback != nil {
		st.callback(&Event{Type: EventTypeLostDevice})
	}
}

// SetFreqCorrection sets the tuner frequency correction in ppm.
func (st *Radio) SetFreqCorrection(ppm int) {
	if st.useTCP {
		if err := st.rtltcp.setFreqCorrection(uint32(ppm)); err != nil {
			log.Printf("nrsc5: set freq correction failed")
		}
	}
}

// SetBiasTee toggles the bias-T power supply (rtl_tcp only).
func (st *Radio) SetBiasTee(on bool) {
	if st.useTCP {
		v := uint32(0)
		if on {
			v = 1
		}
		if err := st.rtltcp.setBiasTee(v); err != nil {
			log.Printf("nrsc5: set bias tee failed")
		}
	}
}

// SetDirectSampling enables direct sampling mode (rtl_tcp only).
func (st *Radio) SetDirectSampling(mode int) {
	if st.useTCP {
		if err := st.rtltcp.setDirectSampling(uint32(mode)); err != nil {
			log.Printf("nrsc5: set direct sampling failed")
		}
	}
}
