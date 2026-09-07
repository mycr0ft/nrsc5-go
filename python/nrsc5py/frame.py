"""L2/L3 framing (mirrors frame.go): PCI detection, RS-protected
headers, HDLC deframing, audio PDUs, fixed data channels."""

from __future__ import annotations

MAX_AAS_LEN = 8212
RS_BLOCK_LEN = 255
RS_CODEWORD_LEN = 96
MAX_AUDIO_PACKETS = 64

PCI_AUDIO = 0x38D8D3
PCI_AUDIO_OPP = 0xCE3634
PCI_AUDIO_FIXED = 0xE3634C
PCI_AUDIO_FIXED_OPP = 0x8D8D33
PCI_FIXED = 0x3634CE
PCI_RESERVED_CW5 = 0x8D338D
PCI_RESERVED_CW6 = 0xD8D338
PCI_RESERVED_CW7 = 0x634CE3
PCI_MAX_ERRORS = 4
PCI_COUNT = 8
PCI_POSSIBILITIES = [PCI_AUDIO, PCI_AUDIO_OPP, PCI_AUDIO_FIXED,
                     PCI_AUDIO_FIXED_OPP, PCI_FIXED, PCI_RESERVED_CW5,
                     PCI_RESERVED_CW6, PCI_RESERVED_CW7]

PACKET_FLAG_NONE = 0
PACKET_FLAG_CRC_ERROR = 1 << 0
PACKET_NONE = 0
PACKET_FULL = 1
PACKET_HALF_FRONT = 2
PACKET_HALF_BACK = 3

VALID_FCS16 = 0xF0B8

# CRC8 table (HDLC CRC-8 for audio packets) and FCS16 (HDLC CRC).
CRC8_TAB = bytes.fromhex(
    "00316253c4f5a697b988dbea7d4c1f2e4372211087b6e5d4facb98a93ef5c6d"
    "86b7e4d5427320113f0e5d6cfbca99a8c5f4a79601306352 7c4d1e2fb889da"
    "eb3d0c5f6ef9c89baa84b5e6d7407122137e4f1c2dba8bd8e9c7f6a59403261"
    "50bb8ad9e87f4e1d2c023360 51c6f7a495f8c99a3b3c0d5e6f417023128 5b4"
    "e7d67a4b182 9be8fdcedc3f2a1900736655439085b6afd cc9fae80b1e2d344"
    "752617fccd9eaf38095a6b45742716 81b0e3d2bf8eddec7b4a192806376455c2"
    "f3a0914776251483b2e1d0fecf9c3ad3ab586904356657c0f1a293bd8cdfee79"
    "481b2ac1f0a3920534675678491a2bbc8ddee f82b3e0d15968ff ce9dacffce9dac"
)
# (the hex blob above is unwieldy; the C table is authoritative — build
#  the tables programmatically below instead.)

CRC8_TAB = None
FCS_TAB = None


def _build_crc_tables():
    global CRC8_TAB, FCS_TAB
    crc8 = [0] * 256
    for i in range(256):
        crc = i
        for _ in range(8):
            if crc & 0x80:
                crc = ((crc << 1) ^ 0x07) & 0xFF
            else:
                crc = (crc << 1) & 0xFF
        crc8[i] = crc ^ 0xFF
    # verify against a couple of known values from the C table
    assert crc8[1] == 0x31, hex(crc8[1])
    CRC8_TAB = bytes(crc8)

    fcs = [0] * 256
    for i in range(256):
        crc = i
        for _ in range(8):
            crc = (crc >> 1) ^ (0x8408 if crc & 1 else 0)
        fcs[i] = crc
    FCS_TAB = fcs


_build_crc_tables()


def crc8(pkt, cnt: int) -> int:
    crc = 0xFF
    for i in range(cnt):
        crc = CRC8_TAB[crc ^ pkt[i]]
    return crc


def fcs16(cp, length: int) -> int:
    crc = 0xFFFF
    for i in range(length):
        crc = (crc >> 8) ^ FCS_TAB[(crc ^ cp[i]) & 0xFF]
    return crc


def has_audio(pci) -> bool:
    return pci in (PCI_AUDIO, PCI_AUDIO_OPP, PCI_AUDIO_FIXED,
                   PCI_AUDIO_FIXED_OPP)


def has_fixed(pci) -> bool:
    return pci in (PCI_AUDIO_FIXED, PCI_AUDIO_FIXED_OPP, PCI_FIXED)


def pop_count(n: int) -> int:
    c = 0
    while n:
        n &= n - 1
        c += 1
    return c


def fuzzy_pci(pci: int, pci_len: int):
    for cand in PCI_POSSIBILITIES:
        score = pop_count((pci ^ cand) >> (24 - pci_len))
        if score <= PCI_MAX_ERRORS:
            return cand, score
    return 0, -1


def unescape_hdlc(data: bytearray) -> int:
    p = 0
    i = 0
    while i < len(data):
        if data[i] == 0x7D:
            i += 1
            if i < len(data):
                data[p] = data[i] | 0x20
                p += 1
        else:
            data[p] = data[i]
            p += 1
        i += 1
    return p


class FixedSubchannel:
    def __init__(self):
        self.mode = 0
        self.length = 0
        self.block_idx = 0
        self.blocks = bytearray(255 + 4)
        self.idx = -1
        self.data = bytearray(MAX_AAS_LEN)


class CccData:
    def __init__(self):
        self.sync_width = 0
        self.sync_count = 0
        self.ccc_buf = bytearray(32)
        self.ccc_idx = -1
        self.subchannel = [FixedSubchannel() for _ in range(4)]
        self.fixed_ready = False


class Frame:
    def __init__(self, inp):
        self.input = inp
        from .rs import init_rs
        self.rs_dec = init_rs(8, 0x11D, 1, 1, 8)
        self.buffer = bytearray(MAX_PDU_LEN)
        self.services = [dict(access=-1, typ=-1, codec=-1, blend=-1,
                              gain=-1, delay=-1, latency=-1)
                         for _ in range(8)]
        self.program = 0
        self.psd_buf = [bytearray(MAX_AAS_LEN) for _ in range(8)]
        self.psd_idx = [-1] * 8
        self.ccc_data = [CccData() for _ in range(3)]
        self.reset()

    def reset(self):
        for svc in self.services:
            svc.update(access=-1, typ=-1, codec=-1, blend=-1, gain=-1,
                       delay=-1, latency=-1)
        self.psd_idx = [-1] * 8
        for c in self.ccc_data:
            c.fixed_ready = False
            c.sync_width = 0
            c.sync_count = 0
            c.ccc_idx = -1

    def fix_header(self, buf) -> bool:
        hdr = bytearray(RS_BLOCK_LEN)
        for i in range(RS_CODEWORD_LEN):
            hdr[RS_BLOCK_LEN - i - 1] = buf[i]
        corrections = self.input.rs_decode(self.rs_dec, hdr)
        if corrections == -1:
            return False
        for i in range(RS_BLOCK_LEN - RS_CODEWORD_LEN):
            if hdr[i] != 0:
                return False
        for i in range(RS_CODEWORD_LEN):
            buf[i] = hdr[RS_BLOCK_LEN - i - 1]
        return True

    def push(self, bits, length: int, lc: int):
        """frame_push: extract PCI header, then frame_process."""
        layout = {
            146176: (116176, 1248, 24),
            4608: (120, 184, 24),
            2304: (120, 88, 24),
            3750: (120, 160, 22),
            24000: (120, 992, 24),
            30000: (120, 1240, 24),
        }
        if length not in layout:
            return
        start, offset, pci_len = layout[length]
        h = 0
        header = 0
        val = 0
        j = 0
        ptr = 0
        for i in range(length):
            byte_start = (i >> 3) << 3
            byte_len = min(8, length - byte_start)
            bit = bits[byte_start + byte_len - 1 - (i & 7)]
            if i >= start and (i - start) % offset == 0 and h < pci_len:
                header |= bit << (23 - h)
                h += 1
            else:
                val |= bit << (7 - j)
                j += 1
                if j == 8:
                    self.buffer[ptr] = val
                    ptr += 1
                    val = 0
                    j = 0
        pci, score = fuzzy_pci(header, pci_len)
        if score < 0:
            if lc == 0:
                self.input.set_sync_state(0)
            return
        self.process(ptr, lc, pci)

    def process(self, length: int, lc: int, pci):
        offset = 0
        audio_end = length
        if has_fixed(pci):
            audio_end = self.process_fixed_data(length, lc)
        if not has_audio(pci):
            return

        while offset < audio_end - RS_CODEWORD_LEN:
            start = offset
            if not self.fix_header(self.buffer[offset:offset + RS_CODEWORD_LEN]):
                return
            hdr = self.parse_header(self.buffer[offset:])
            offset += 14
            lc_bits = self.calc_lc_bits(hdr)
            loc_bytes = (lc_bits * hdr["nop"] + 4) // 8
            if (start + hdr["la_location"] + 1 < offset + loc_bytes
                    or start + hdr["la_location"] >= audio_end):
                return
            locations = []
            for jx in range(hdr["nop"]):
                loc = self.parse_location(offset, lc_bits, jx)
                if jx == 0 and loc <= hdr["la_location"]:
                    return
                if jx > 0 and loc <= locations[-1]:
                    return
                if start + loc >= audio_end:
                    return
                locations.append(loc)
            offset += loc_bytes

            if hdr["stream_id"] >= 2:
                offset = start + locations[hdr["nop"] - 1] + 1
                continue

            hef = dict(prog_num=0, access=0, prog_type=0)
            if hdr["hef"]:
                offset += self.parse_hef(offset, audio_end - offset, hef)
            prog = hef["prog_num"]
            service = self.services[prog]
            if hdr["stream_id"] == 0 and any(
                    service[k] != v for k, v in (
                        ("access", hef["access"]),
                        ("typ", hef["prog_type"]),
                        ("codec", hdr["codec_mode"]),
                        ("blend", hdr["blend_control"]),
                        ("gain", hdr["per_stream_delay"]),
                        ("delay", hdr["common_delay"]),
                        ("latency", hdr["latency"]))):
                service.update(access=hef["access"], typ=hef["prog_type"],
                               codec=hdr["codec_mode"],
                               blend=hdr["blend_control"],
                               gain=hdr["per_stream_delay"],
                               delay=hdr["common_delay"],
                               latency=hdr["latency"])
                gain = service["gain"]
                if gain >= 16:
                    gain -= 32
                self.input.radio.report_audio_service(
                    prog, service["access"], service["typ"],
                    service["codec"], service["blend"], gain,
                    service["delay"] * 4, service["latency"] * 2)

            avg = self.calc_avg_packets(hdr)
            seq = (64 + hdr["seq"] - hdr["pfirst"]) % 64
            output_offset = ((64 + hdr["pdu_seq"] * avg - hdr["latency"] * 2)
                             % 64)
            if (64 + seq - output_offset) % 64 >= 32:
                output_offset = (output_offset + 32) % 64
            self.input.output.align(prog, hdr["stream_id"], output_offset)

            self.parse_hdlc(self.psd_buf[prog], self.psd_idx, prog,
                            MAX_AAS_LEN,
                            bytes(self.buffer[offset:start + hdr["la_location"] + 1]),
                            0)
            offset = start + hdr["la_location"] + 1

            for jx in range(hdr["nop"]):
                cnt = start + locations[jx] - offset
                c = crc8(self.buffer[offset:offset + cnt + 1], cnt + 1)
                ref = dict(program=prog, stream_id=hdr["stream_id"],
                           data=bytes(self.buffer[offset:offset + cnt]),
                           size=cnt, seq=seq, flags=0, shape=PACKET_FULL)
                if c != 0:
                    ref["flags"] |= PACKET_FLAG_CRC_ERROR
                if jx == 0 and hdr["pfirst"]:
                    ref["shape"] = PACKET_HALF_BACK
                elif jx == hdr["nop"] - 1 and hdr["plast"]:
                    ref["shape"] = PACKET_HALF_FRONT
                self.input.output.push(ref)
                offset += cnt + 1
                seq = (seq + 1) % 64

    @staticmethod
    def parse_header(buf) -> dict:
        return dict(
            codec_mode=buf[8] & 0xF,
            stream_id=(buf[8] >> 4) & 0x3,
            pdu_seq=(buf[8] >> 6) | ((buf[9] & 1) << 2),
            blend_control=(buf[9] >> 1) & 0x3,
            per_stream_delay=buf[9] >> 3,
            common_delay=buf[10] & 0x3F,
            latency=(buf[10] >> 6) | ((buf[11] & 1) << 2),
            pfirst=(buf[11] >> 1) & 1,
            plast=(buf[11] >> 2) & 1,
            seq=(buf[11] >> 3) | ((buf[12] & 1) << 5),
            nop=(buf[12] >> 1) & 0x3F,
            hef=buf[12] >> 7,
            la_location=buf[13])

    @staticmethod
    def parse_hef(buf, length: int, hef: dict) -> int:
        byte_pos = 0
        while True:
            if byte_pos >= length:
                return length
            b = buf[byte_pos]
            tag = (b >> 4) & 0x7
            if tag == 0:
                hef.setdefault("class_ind", 0)
                hef["class_ind"] = b & 0xF
            elif tag == 1:
                hef["prog_num"] = (b >> 1) & 0x7
                if b & 1:
                    if byte_pos + 2 >= length:
                        return length
                    byte_pos += 1
                    hef["pdu_len"] = (b & 0x7F) << 7
                    byte_pos += 1
                    hef["pdu_len"] |= buf[byte_pos] & 0x7F
            elif tag == 2:
                if byte_pos + 1 >= length:
                    return length
                hef["access"] = (b >> 3) & 1
                hef["prog_type"] = (b & 1) << 7
                byte_pos += 1
                hef["prog_type"] |= buf[byte_pos] & 0x7F
            elif tag == 3:
                skip = 4 if b & 0x8 else 3
                if byte_pos + skip >= length:
                    return length
                byte_pos += skip
            elif tag == 4:
                if b & 0x8:
                    if byte_pos + 3 >= length:
                        return length
                    hef["applied_services"] = b & 0x7
                    byte_pos += 1
                    hef["pdu_marker"] = (buf[byte_pos] & 0x7F) << 14
                    byte_pos += 1
                    hef["pdu_marker"] |= (buf[byte_pos] & 0x7F) << 7
                    byte_pos += 1
                    hef["pdu_marker"] |= buf[byte_pos] & 0x7F
                else:
                    if byte_pos + 1 >= length:
                        return length
                    byte_pos += 1
            byte_pos += 1
            if not buf[byte_pos - 1] & 0x80:
                break
        return byte_pos

    @staticmethod
    def calc_lc_bits(hdr) -> int:
        cm = hdr["codec_mode"]
        if cm == 0:
            return 16
        if cm in (1, 2, 3):
            return 12 if hdr["stream_id"] == 0 else 16
        if cm in (10, 13):
            return 12
        return 16

    @staticmethod
    def calc_avg_packets(hdr) -> int:
        cm = hdr["codec_mode"]
        if cm == 0:
            return 32
        if cm in (1, 2, 3):
            return 4 if hdr["stream_id"] == 0 else 32
        if cm == 10:
            return 32 if hdr["stream_id"] == 0 else 4
        if cm == 13:
            return 4
        return 32

    @staticmethod
    def parse_location(buf, lc_bits: int, i: int) -> int:
        if lc_bits == 16:
            return (buf[2 * i + 1] << 8) | buf[2 * i]
        if i % 2 == 0:
            return ((buf[i // 2 * 3 + 1] & 0xF) << 8) | buf[i // 2 * 3]
        return (buf[i // 2 * 3 + 2] << 4) | (buf[i // 2 * 3 + 1] >> 4)

    def aas_push(self, psd, length: int, lc: int):
        length = unescape_hdlc(psd[:length]) and unescape_hdlc(psd[:length])
        length = unescape_hdlc(psd)
        if length == 0:
            pass
        elif fcs16(psd, length) != VALID_FCS16:
            pass  # abandoned HDLC frames are normal
        elif psd[0] != 0x21:
            pass
        else:
            self.input.output.aas_push(bytes(psd[1:length - 3]))

    def parse_hdlc(self, buffer, bufidx_attr, bufsz: int, input_bytes, lc: int):
        bufidx = getattr(self, bufidx_attr) if isinstance(bufidx_attr, str) \
            else bufidx_attr
        for b in input_bytes:
            if b == 0x7E:
                if bufidx >= 0:
                    self.aas_push(buffer, bufidx, lc)
                bufidx = 0
            elif bufidx >= 0:
                if bufidx == bufsz:
                    bufidx = -1
                    continue
                buffer[bufidx] = b
                bufidx += 1
        if isinstance(bufidx_attr, str):
            setattr(self, bufidx_attr, bufidx)
        else:
            bufidx_attr[0] = bufidx

    def process_fixed_ccc(self, buf, buflen: int, lc: int):
        ccc = self.ccc_data[lc]
        buflen = unescape_hdlc(buf[:buflen])
        if buflen == 0 or ccc.fixed_ready:
            return
        if fcs16(buf, buflen) != VALID_FCS16:
            return
        for i in range(4):
            subch = ccc.subchannel[i]
            subch.mode = 0
            subch.length = 0
            if 5 + i * 4 <= buflen:
                mode = buf[1 + i * 4] | (buf[2 + i * 4] << 8)
                length = buf[3 + i * 4] | (buf[4 + i * 4] << 8)
                if mode == 0:
                    subch.mode = mode
                    subch.length = length
                    subch.block_idx = 0
                    subch.idx = -1
        ccc.fixed_ready = True

    def process_fixed_data(self, length: int, lc: int) -> int:
        ccc = self.ccc_data[lc]
        p = length - 1
        if ccc.sync_count < 2:
            b = self.buffer[p]
            width = 1 if b == 0 else ((b & 0xF) * 2 if b >> 4 == (b & 0xF) else 0)
            if width > 0 and ccc.sync_width == width:
                ccc.sync_count += 1
            else:
                ccc.sync_count = 0
            ccc.sync_width = width
            if ccc.sync_count < 2:
                return p
        p -= ccc.sync_width
        # CCC frame handling via parse_hdlc into ccc_buf
        self._parse_hdlc_generic(ccc.ccc_buf, ccc, 32,
                                 self.buffer[p:p + ccc.sync_width], lc,
                                 self.process_fixed_ccc)
        if not ccc.fixed_ready:
            return p
        for i in range(3, -1, -1):
            subch = ccc.subchannel[i]
            sub_len = subch.length
            if sub_len == 0:
                continue
            p -= sub_len
            for j in range(sub_len):
                subch.blocks[subch.block_idx] = self.buffer[p + j]
                subch.block_idx += 1
                if subch.block_idx == 4 and bytes(subch.blocks[:4]) != b"\x7d\x3a\xe2\x42":
                    subch.blocks[:3] = subch.blocks[1:4]
                    subch.block_idx -= 1
                if subch.block_idx == 255 + 4:
                    self._parse_hdlc_generic(subch.data, subch, MAX_AAS_LEN,
                                             bytes(subch.blocks[4:255 + 4]), lc,
                                             self.aas_push)
                    subch.block_idx = 0
        return p

    def _parse_hdlc_generic(self, buffer, holder, bufsz: int, input_bytes,
                            lc: int, process_fn):
        idx = holder.idx
        for b in input_bytes:
            if b == 0x7E:
                if idx >= 0:
                    process_fn(buffer[:idx], idx, lc)
                idx = 0
            elif idx >= 0:
                if idx == bufsz:
                    idx = -1
                    continue
                buffer[idx] = b
                idx += 1
        holder.idx = idx
