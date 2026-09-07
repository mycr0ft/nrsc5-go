"""rtl_tcp client (mirrors rtltcp.go).

Protocol: the server sends a 12-byte greeting (magic "RTL0", tuner type,
tuner gain count, big-endian). Commands are 5 bytes: one opcode byte
plus a big-endian uint32 parameter. Samples then stream as cu8.
"""

from __future__ import annotations

import socket
import struct

OP_SET_CENTER_FREQ = 0x01
OP_SET_SAMPLE_RATE = 0x02
OP_SET_TUNER_GAIN_MODE = 0x03
OP_SET_TUNER_GAIN = 0x04
OP_SET_FREQ_CORRECTION = 0x05
OP_SET_AGC_MODE = 0x08
OP_SET_DIRECT_SAMPLING = 0x09
OP_SET_OFFSET_TUNING = 0x0A
OP_SET_BIAS_TEE = 0x0E

TUNER_NAMES = {1: "E4000", 2: "FC0012", 3: "FC0013", 4: "FC2580",
               5: "R820T", 6: "R828D"}

# Per-tuner gain tables in tenths of a dB (librtlsdr).
GAIN_TABLES = {
    1: [-10, 15, 40, 65, 90, 115, 140, 165, 190, 215, 240, 290, 340, 420],
    2: [-99, -40, 71, 179, 192],
    3: [-99, -73, -65, -63, -60, -58, -54, 58, 61, 63, 65, 67, 68, 70, 71,
        179, 181, 182, 184, 186, 188, 191, 197],
    4: [],
    5: [0, 9, 14, 27, 37, 77, 87, 125, 144, 157, 166, 197, 207, 229, 254,
        280, 297, 328, 338, 364, 372, 386, 402, 421, 434, 439, 445, 480,
        496],
    6: [0, 9, 14, 27, 37, 77, 87, 125, 144, 157, 166, 197, 207, 229, 254,
        280, 297, 328, 338, 364, 372, 386, 402, 421, 434, 439, 445, 480,
        496],
}


class RtlTcp:
    def __init__(self, host: str, port: int, timeout: float = 5.0):
        self.sock = socket.create_connection((host, port), timeout=timeout)
        greeting = self._read_full(12)
        if greeting[:4] != b"RTL0":
            raise ValueError("bad rtl_tcp magic")
        self.tuner_type, self.gain_count = struct.unpack(">II", greeting[4:12])
        self.sock.settimeout(None)

    def _read_full(self, n: int) -> bytes:
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("rtl_tcp closed")
            buf += chunk
        return buf

    def _cmd(self, op: int, param: int):
        self.sock.sendall(struct.pack(">BI", op, param))

    def set_center_freq(self, freq: int):
        self._cmd(OP_SET_CENTER_FREQ, freq)

    def set_sample_rate(self, rate: int):
        self._cmd(OP_SET_SAMPLE_RATE, rate)

    def set_tuner_gain_mode(self, manual: bool):
        self._cmd(OP_SET_TUNER_GAIN_MODE, 1 if manual else 0)

    def set_tuner_gain(self, gain_tenths: int):
        self._cmd(OP_SET_TUNER_GAIN, gain_tenths)

    def set_freq_correction(self, ppm: int):
        self._cmd(OP_SET_FREQ_CORRECTION, ppm)

    def set_agc_mode(self, on: bool):
        self._cmd(OP_SET_AGC_MODE, 1 if on else 0)

    def set_direct_sampling(self, mode: int):
        self._cmd(OP_SET_DIRECT_SAMPLING, mode)

    def set_offset_tuning(self, on: bool):
        self._cmd(OP_SET_OFFSET_TUNING, 1 if on else 0)

    def set_bias_tee(self, on: bool):
        self._cmd(OP_SET_BIAS_TEE, 1 if on else 0)

    def read(self, n: int) -> bytes:
        return self.sock.recv(n)

    def reset_buffer(self, cnt: int):
        """Drain pending samples, then read cnt bytes to realign."""
        self.sock.settimeout(0.05)
        recvd = 0
        try:
            while True:
                chunk = self.sock.recv(1024)
                if not chunk:
                    break
                recvd += len(chunk)
        except TimeoutError:
            pass
        self.sock.settimeout(2.0)
        if recvd & 1:
            self.sock.recv(1)
        remaining = cnt
        while remaining > 0:
            chunk = self.sock.recv(min(1024, remaining))
            if not chunk:
                raise ConnectionError("rtl_tcp closed during reset")
            remaining -= len(chunk)
        self.sock.settimeout(None)

    def get_tuner_gains(self):
        return GAIN_TABLES.get(self.tuner_type, [])

    def close(self):
        self.sock.close()
