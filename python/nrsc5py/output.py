"""Audio output, ID3, SIG, LOT files (mirrors output.go).

The elastic buffer absorbs network-style jitter between the decoder and
the audio consumer; each program/stream has its own 64-slot ring.
"""

from __future__ import annotations

import numpy as np

MAX_SIG_SERVICES = 16
MAX_SIG_COMPONENTS = 8
MAX_LOT_FILES = 12
LOT_FRAGMENT_SIZE = 256
MAX_FILE_BYTES = 65536
MAX_LOT_FRAGMENTS = MAX_FILE_BYTES // LOT_FRAGMENT_SIZE

PACKET_FLAG_NONE = 0

AUDIO_FRAME_SAMPLES = 2048


class AasFile:
    def __init__(self):
        self.timestamp = 0
        self.name = ""
        self.mime = 0
        self.expiry = None
        self.lot = 0
        self.size = 0
        self.bytes_so_far = 0
        self.fragments = [None] * MAX_LOT_FRAGMENTS
        self.data = None


class SigComponent:
    def __init__(self):
        self.typ = 0  # 0 none, 1 data, 2 audio
        self.id = 0
        self.port = 0
        self.service_data_type = 0
        self.data_type = 0
        self.mime = 0
        self.lot_files = [None] * MAX_LOT_FILES
        self.audio_port = 0
        self.audio_type = 0


class Output:
    def __init__(self, radio):
        self.radio = radio
        self.elastic = {}
        self.aac = {}
        self.sig_bytes = None
        self.services = []
        self.lot_lru_counter = 1
        self.here_expected_seq = -1
        self.here_sync = 0
        self.here_payload_len = -1
        self.here_buffer = bytearray(2048)
        self.here_idx = 0
        self.here_last_timestamp = [0] * 10
        self.reset()

    def reset(self):
        for k in self.elastic:
            for pkt in self.elastic[k]["packets"]:
                pkt["size"] = 0
                pkt["flags"] = 0
                pkt["shape"] = 0
            self.elastic[k]["audio_offset"] = -1
        self.sig_bytes = None
        self.services = []

    def align(self, program: int, stream_id: int, offset: int):
        key = (program, stream_id)
        self._elastic(key)["audio_offset"] = offset

    def push(self, ref: dict):
        if ref["stream_id"] != 0:
            return
        key = (ref["program"], ref["stream_id"])
        elastic = self._elastic(key)
        pkt = elastic["packets"][ref["seq"]]
        if pkt["shape"] == 1:  # PACKET_FULL
            pass  # overwrite warning in C
        if ref["shape"] == PACKET_HALF_BACK and pkt["shape"] == PACKET_HALF_FRONT:
            pkt["flags"] |= ref["flags"]
            pkt["shape"] = PACKET_FULL
            if pkt["flags"] & 1 == 0:
                pkt["data"] += ref["data"]
                pkt["size"] += ref["size"]
            else:
                pkt["size"] = 0
        else:
            if ref["shape"] == PACKET_HALF_BACK:
                return
            pkt["flags"] = ref["flags"]
            pkt["shape"] = ref["shape"]
            if pkt["flags"] & 1 == 0:
                pkt["data"] = bytearray(ref["data"])
                pkt["size"] = ref["size"]
            else:
                pkt["size"] = 0

    def _elastic(self, key):
        if key not in self.elastic:
            self.elastic[key] = dict(
                packets=[dict(size=0, flags=0, shape=0, data=bytearray())
                         for _ in range(64)],
                audio_offset=-1)
        return self.elastic[key]

    def advance(self):
        audio_frames = 2 if self.radio.mode == 0 else 4
        for program in range(8):
            elastic = self.elastic.get((program, 0))
            if not elastic or elastic["audio_offset"] == -1:
                continue
            for _ in range(audio_frames):
                pkt = elastic["packets"][elastic["audio_offset"]]
                produced = False
                if pkt["shape"] == 1:
                    self.radio.report_hdc(program, bytes(pkt["data"][:pkt["size"]]),
                                          pkt["flags"])
                    dec = self.aac.get(program)
                    if dec is not None:
                        samples = dec.decode(pkt["data"][:pkt["size"]])
                        if samples is not None:
                            self.radio.report_audio(program, samples)
                            produced = True
                pkt["size"] = 0
                pkt["flags"] = 0
                pkt["shape"] = 0
                if not produced:
                    self.radio.report_audio(program,
                                            np.zeros(AUDIO_FRAME_SAMPLES * 2,
                                                     dtype=np.int16))
                elastic["audio_offset"] = (elastic["audio_offset"] + 1) % 64

    # --- AAS ----------------------------------------------------------------

    def aas_push(self, psd: bytes):
        buf = psd
        port = buf[0] | (buf[1] << 8)
        seq = buf[2] | (buf[3] << 8)
        if port == 0x5100 or 0x5201 <= port <= 0x5207:
            self.id3(port & 0x7, buf[4:])
        elif port == 0x20:
            self.parse_sig(buf[4:])
        elif 0x401 <= port <= 0x50FF:
            self.process_port(port, seq, buf[4:])
        else:
            pass  # unknown port

    # --- ID3 ------------------------------------------------------------------

    def id3(self, program: int, buf: bytes):
        if len(buf) < 10 or buf[:5] != b"ID3\x03\x00" or buf[5] != 0:
            return
        id3_len = ((buf[6] & 0x7F) << 21 | (buf[7] & 0x7F) << 14
                   | (buf[8] & 0x7F) << 7 | (buf[9] & 0x7F)) + 10
        if id3_len > len(buf):
            return
        off = 10
        evt = dict(program=program, title="", artist="", album="",
                   genre="", comments=[], ufid_owner="", ufid_id="",
                   xhdr_mime=0, xhdr_param=-1, xhdr_lot=-1)
        while off + 10 <= id3_len:
            tag = buf[off:off + 10]
            data = buf[off + 10:]
            frame_len = (tag[4] << 24) | (tag[5] << 16) | (tag[6] << 8) | tag[7]
            if off + 10 + frame_len > id3_len:
                break
            name = bytes(tag[:4])
            if name == b"TIT2":
                evt["title"] = self.id3_text(data, frame_len)
            elif name == b"TPE1":
                evt["artist"] = self.id3_text(data, frame_len)
            elif name == b"TALB":
                evt["album"] = self.id3_text(data, frame_len)
            elif name == b"TCON":
                evt["genre"] = self.id3_text(data, frame_len)
            elif name == b"XHDR":
                if frame_len >= 6:
                    evt["xhdr_mime"] = (data[0] | data[1] << 8
                                        | data[2] << 16 | data[3] << 24)
                    evt["xhdr_param"] = data[4]
                    extlen = data[5]
                    if 6 + extlen != frame_len:
                        pass
                    elif evt["xhdr_param"] == 0 and extlen == 2:
                        evt["xhdr_lot"] = data[6] | data[7] << 8
                    elif evt["xhdr_param"] == 1 and extlen == 0:
                        evt["xhdr_lot"] = -1
            off += 10 + frame_len
        self.radio.report_id3(evt)

    @staticmethod
    def id3_text(buf, frame_len: int) -> str:
        if frame_len > 0:
            enc = buf[0]
            text = buf[1:frame_len]
            return text.decode("latin-1" if enc == 0 else "utf-16-le",
                               errors="replace")
        return ""

    # --- SIG -------------------------------------------------------------------

    def parse_sig(self, buf: bytes):
        if self.sig_bytes is not None:
            if buf == self.sig_bytes:
                return
            self.services = []
            self.sig_bytes = None
        self.sig_bytes = bytes(buf)
        p = 0
        service = None
        while p < len(buf):
            typ = buf[p]
            p += 1
            if typ & 0xF0 == 0x40:
                number = buf[p] | buf[p + 1] << 8
                service = dict(typ=(2 if typ == 0x40 else 1), number=number,
                               name="", components=[])
                self.services.append(service)
                p += 3
            elif typ & 0xF0 == 0x60:
                l = buf[p]
                p += 1
                if service is None:
                    break
                if typ == 0x69:
                    service["name"] = buf[p + 1:p + l - 1].decode("latin-1")
                elif typ == 0x67:
                    comp = SigComponent()
                    comp.typ = 1
                    comp.id = buf[p]
                    comp.port = buf[p + 1] | buf[p + 2] << 8
                    comp.service_data_type = buf[p + 3] | buf[p + 4] << 8
                    comp.data_type = buf[p + 5]
                    comp.mime = (buf[p + 8] | buf[p + 9] << 8
                                 | buf[p + 10] << 16 | buf[p + 11] << 24)
                    service["components"].append(comp)
                elif typ == 0x66:
                    comp = SigComponent()
                    comp.typ = 2
                    comp.id = buf[p]
                    comp.audio_port = buf[p + 1]
                    comp.audio_type = buf[p + 2]
                    comp.mime = (buf[p + 7] | buf[p + 8] << 8
                                 | buf[p + 9] << 16 | buf[p + 10] << 24)
                    service["components"].append(comp)
                p += l - 1
            else:
                break
        self.radio.report_sig(self.services)

    # --- data ports / LOT ---------------------------------------------------------

    def find_port(self, port_id: int):
        for svc in self.services:
            for comp in svc["components"]:
                if comp.typ == 1 and comp.port == port_id:
                    return comp
        return None

    def process_port(self, port_id: int, seq: int, buf: bytes):
        if not self.services:
            return
        component = self.find_port(port_id)
        if component is None:
            return
        if component.data_type == 0:  # STREAM
            self.radio.report_stream(seq, len(buf), buf, component)
            if component.mime == 0xB7F03DFC:  # HERE image
                self.here_images_push(seq, buf)
        elif component.data_type == 1:  # PACKET
            self.radio.report_packet(seq, len(buf), buf, component)
        elif component.data_type == 3:  # LOT
            self._process_lot(port_id, seq, buf, component)

    def _process_lot(self, port_id: int, seq: int, buf: bytes, component):
        if len(buf) < 8:
            return
        hdrlen = buf[0]
        repeat = buf[1]
        lot = buf[2] | buf[3] << 8
        frag_seq = int.from_bytes(buf[4:8], "little")
        if hdrlen < 8 or hdrlen > len(buf):
            return
        buf = buf[8:]
        if frag_seq >= MAX_LOT_FRAGMENTS:
            return
        file = next((f for f in component.lot_files
                     if f and f.timestamp and f.lot == lot), None)
        if file is None:
            file = AasFile()
            file.lot = lot
            min_ts = min((f.timestamp for f in component.lot_files
                          if f), default=0)
            for i, f in enumerate(component.lot_files):
                if f is None or f.timestamp == 0:
                    component.lot_files[i] = file
                    break
            else:
                component.lot_files[0] = file
        file.timestamp = self.lot_lru_counter
        self.lot_lru_counter += 1
        new_data = False
        if hdrlen > 8:
            if hdrlen < 16:
                return
            hdr = buf[:hdrlen - 8]
            version = int.from_bytes(hdr[0:4], "little")
            year = ((hdr[7] << 4 | hdr[6] >> 4)) - 1900
            mon = (hdr[6] & 0xF) - 1
            mday = hdr[5] >> 3
            hour = (hdr[5] & 7) << 2 | hdr[4] >> 6
            minute = hdr[4] & 0x3F
            size = int.from_bytes(hdr[8:12], "little")
            mime = int.from_bytes(hdr[12:16], "little")
            name = hdr[16:].decode("latin-1")
            buf = buf[hdrlen:]
            if name != file.name or size != file.size or mime != file.mime:
                file.fragments = [None] * MAX_LOT_FRAGMENTS
                file.bytes_so_far = 0
                new_data = True
            file.name = name
            file.size = size
            file.mime = mime
            if new_data:
                self.radio.report_lot(lot, file)
        is_dup = file.fragments[frag_seq] is not None
        if not is_dup:
            frag = bytearray(LOT_FRAGMENT_SIZE)
            frag[:len(buf)] = buf
            file.fragments[frag_seq] = bytes(frag)
            file.bytes_so_far += len(buf)
        if file.size:
            num_frags = (file.size + LOT_FRAGMENT_SIZE - 1) // LOT_FRAGMENT_SIZE
            if all(f is not None for f in file.fragments[:num_frags]):
                self.radio.report_lot(lot, file)

    # --- HERE images ----------------------------------------------------------

    def here_images_push(self, seq: int, buf: bytes):
        if seq != self.here_expected_seq:
            self.here_buffer = bytearray(2048)
            self.here_payload_len = -1
            self.here_sync = 0
        for offset in range(len(buf)):
            self.here_sync = ((self.here_sync << 8) | buf[offset]) & 0xFFFFFFFF
            if self.here_payload_len == -1:
                if (self.here_sync >> 16) == 0xFFF7FFF7:
                    self.here_payload_len = self.here_sync & 0xFFFF
                    self.here_idx = 0
            else:
                self.here_buffer[self.here_idx] = buf[offset]
                self.here_idx += 1
                if self.here_idx == self.here_payload_len + 2:
                    self._here_process()
                    self.here_payload_len = -1
        self.here_expected_seq = (seq + 1) & 0xFFFF

    def _here_process(self):
        pass  # HERE image payload parsing (traffic/weather maps)

