//go:build !aac

package nrsc5

// Fallback when built without -tags aac: HDC packets are not decoded
// in-process (silence is reported). Use -dump-hdc to capture the AAC
// stream for external decoding, or rebuild with -tags aac.

type hdcDecoder struct{}

func newHDCDecoder() *hdcDecoder { return &hdcDecoder{} }

func (d *hdcDecoder) close() {}

func (d *hdcDecoder) decode(frame []byte) []int16 {
	return nil
}
