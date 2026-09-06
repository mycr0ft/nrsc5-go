//go:build aac

package nrsc5

// HDC audio decoding via patched FAAD2 (libfaad_hdc), enabled with
// -tags aac. Build the library from FAAD2 2.11.2 with the nrsc5
// HDC-support patch (HDC_SUPPORT defined):
//
//	git clone --depth 1 --branch 2.11.2 https://github.com/knik0/faad2.git
//	cd faad2 && git apply faad2-hdc-support.patch
//	gcc -O2 -fPIC -shared -DHDC_SUPPORT -DHAVE_INTTYPES_H \
//	    -DPACKAGE_VERSION='"2.11.2"' -Iinclude -Ilibfaad \
//	    libfaad/*.c -o libfaad_hdc.so -lm
//
// Then build this package with: go build -tags aac

// #cgo LDFLAGS: -lfaad_hdc
// #include <neaacdec.h>
import "C"

import "unsafe"

// hdcDecoder wraps a NeAACDec handle initialized for HDC.
type hdcDecoder struct {
	handle C.NeAACDecHandle
}

func newHDCDecoder() *hdcDecoder {
	d := &hdcDecoder{}
	d.handle = C.NeAACDecOpen()
	C.NeAACDecInitHDC(&d.handle)
	cfg := C.NeAACDecGetCurrentConfiguration(d.handle)
	cfg.outputFormat = C.FAAD_FMT_16BIT
	C.NeAACDecSetConfiguration(d.handle, cfg)
	return d
}

func (d *hdcDecoder) close() {
	if d.handle != nil {
		C.NeAACDecClose(d.handle)
		d.handle = nil
	}
}

// decode decodes one HDC frame; returns PCM samples (interleaved L/R)
// or nil on error.
func (d *hdcDecoder) decode(frame []byte) []int16 {
	if len(frame) == 0 {
		return nil
	}
	var info C.NeAACDecFrameInfo
	out := C.NeAACDecDecode(d.handle, &info, (*C.uchar)(unsafe.Pointer(&frame[0])), C.ulong(len(frame)))
	if info.error != 0 || info.samples == 0 {
		return nil
	}
	n := int(info.samples)
	samples := make([]int16, n)
	copy(samples, (*[1 << 20]int16)(unsafe.Pointer(out))[:n:n])
	return samples
}

// decodeHDCFrame decodes one HDC packet (used by output).
func decodeHDCFrame(d interface{}, frame []byte) []int16 {
	if dec, ok := d.(*hdcDecoder); ok {
		return dec.decode(frame)
	}
	return nil
}
