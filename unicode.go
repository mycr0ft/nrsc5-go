package nrsc5

// ISO-8859-1 and UCS-2 to UTF-8 conversion, ported from unicode.c.

func iso8859ToUTF8(buf []byte) string {
	out := make([]byte, 0, len(buf)*2)
	for _, ch := range buf {
		if ch < 0x80 {
			out = append(out, ch)
		} else {
			out = append(out, 0xc0|(ch>>6), 0x80|(ch&0x3f))
		}
	}
	return string(out)
}

func ucs2ToUTF8(buf []byte) string {
	out := make([]byte, 0, len(buf))
	i := 0
	bigEndian := false

	if len(buf) >= 2 {
		if buf[0] == 0xfe && buf[1] == 0xff {
			bigEndian = true
			i += 2
		} else if buf[0] == 0xff && buf[1] == 0xfe {
			bigEndian = false
			i += 2
		}
	}

	for ; i < len(buf)-1; i += 2 {
		var ch uint16
		if bigEndian {
			ch = uint16(buf[i])<<8 | uint16(buf[i+1])
		} else {
			ch = uint16(buf[i]) | uint16(buf[i+1])<<8
		}

		switch {
		case ch < 0x80:
			out = append(out, byte(ch))
		case ch < 0x800:
			out = append(out, 0xc0|byte(ch>>6), 0x80|byte(ch&0x3f))
		default:
			out = append(out, 0xe0|byte(ch>>12), 0x80|byte((ch>>6)&0x3f), 0x80|byte(ch&0x3f))
		}
	}
	return string(out)
}

// trimZeros strips trailing NUL bytes from a fixed-size buffer.
func trimZeros(buf []byte) []byte {
	for i, b := range buf {
		if b == 0 {
			return buf[:i]
		}
	}
	return buf
}
