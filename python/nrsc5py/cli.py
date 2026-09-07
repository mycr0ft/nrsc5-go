"""Command-line interface (mirrors cmd/nrsc5/main.go).

    nrsc5py play -H localhost:1234 -f 89.3 -g 30 0
    nrsc5py -r sample.iq -o out.wav -dump-hdc out.aac 0
"""

from __future__ import annotations

import argparse
import sys

import numpy as np

from .defines import SAMPLE_RATE_AUDIO
from .radio import Radio


def wav_header(data_len: int) -> bytes:
    import struct
    rate = SAMPLE_RATE_AUDIO
    hdr = b"RIFF" + struct.pack("<I", 36 + data_len) + b"WAVE"
    hdr += b"fmt " + struct.pack("<IHHIIHH", 16, 1, 2, rate, rate * 4, 4, 16)
    hdr += b"data" + struct.pack("<I", data_len)
    return hdr


class WavWriter:
    """Streams a WAV header, patches the sizes at close."""

    def __init__(self, f):
        self.f = f
        self.bytes_written = 0
        self.f.write(wav_header(0))

    def write(self, samples):
        b = np.asarray(samples, dtype="<i2").tobytes()
        self.f.write(b)
        self.bytes_written += len(b)

    def close(self):
        import struct
        self.f.seek(4)
        self.f.write(struct.pack("<I", 36 + self.bytes_written))
        self.f.seek(40)
        self.f.write(struct.pack("<I", self.bytes_written))
        self.f.close()


def main(argv=None):
    parser = _build_parser()
    args = parser.parse_args(argv)

    radio = Radio()
    if args.rtltcp:
        host, _, port = args.rtltcp.partition(":")
        radio = Radio.open_rtltcp(host, int(port) if port else 1234)
    else:
        radio = Radio.open_file(sys.stdin.buffer)

    if args.am:
        radio.set_mode(1)

    writer = None
    hdc_file = None
    if args.output:
        if args.output == "-":
            writer = sys.stdout.buffer
        else:
            writer = WavWriter(open(args.output, "wb"))
    if args.dump_hdc:
        hdc_file = open(args.dump_hdc, "wb")

    def on_event(e):
        t = e["type"]
        if t == "sync":
            print("Synchronized")
            print("Frequency offset: {:.0f} Hz".format(e["freq_offset"]))
            print("Primary service mode: %d" % e["psmi"])
        elif t == "lost_sync":
            print("Lost synchronization")
        elif t == "lost_device":
            print("Lost device")
        elif t == "mer":
            print("MER: %.1f dB (lower), %.1f dB (upper)"
                  % (e["lower"], e["upper"]))
        elif t == "ber":
            pass
        elif t == "station_id":
            print("Country: %s, FCC facility ID: %d"
                  % (e["country_code"], e["fcc_facility_id"]))
        elif t == "station_name":
            print("Station name: {}".format(e["name"]))
        elif t == "station_slogan":
            print("Slogan: {}".format(e["slogan"]))
        elif t == "station_message":
            print("Message: {}".format(e["message"]))
        elif t == "station_location":
            print("Station location: %.4f, %.4f, %dm"
                  % (e["latitude"], e["longitude"], e["altitude"]))
        elif t == "id3":
            if e["title"]:
                print("Title: {}".format(e["title"]))
            if e["artist"]:
                print("Artist: {}".format(e["artist"]))
            if e["album"]:
                print("Album: {}".format(e["album"]))
            if e["genre"]:
                print("Genre: {}".format(e["genre"]))
        elif t == "audio":
            if e["program"] != args.program:
                return
            if writer is not None:
                writer.write(e["samples"])
        elif t == "hdc":
            if e["program"] != args.program:
                return
            if hdc_file and not (e["flags"] & 1):
                # write ADTS frame (AAC-LC, 22050 Hz → 44.1 output)
                hdc_file.write(_adts_header(len(e["data"])))
                hdc_file.write(e["data"])

    radio.set_callback(on_event)
    radio.start()

    if args.rtltcp:
        import time
        try:
            while True:
                time.sleep(0.2)
        except KeyboardInterrupt:
            pass
    else:
        radio.run_file()

    radio.stop()
    if hdc_file:
        hdc_file.close()
    if writer is not None and hasattr(writer, "close"):
        writer.close()


def _adts_header(length: int) -> bytes:
    """7-byte ADTS header for AAC-LC stereo (mirrors write_adts_header)."""
    bw = _BitWriter(7)
    bw.add(0xFFF, 12)
    bw.add(0, 1)
    bw.add(0, 2)
    bw.add(1, 1)
    bw.add(1, 2)
    bw.add(7, 4)
    bw.add(0, 1)
    bw.add(2, 3)
    bw.add(0, 1)
    bw.add(0, 1)
    bw.add(0, 1)
    bw.add(0, 1)
    bw.add(length + 7, 13)
    bw.add(0x7FF, 11)
    bw.add(0, 2)
    return bw.bytes


class _BitWriter:
    def __init__(self, n: int):
        self.data = bytearray(n)
        self.bits = 0

    def add(self, value: int, num: int):
        for i in range(num - 1, -1, -1):
            bit = (value >> i) & 1
            self.data[self.bits // 8] |= bit << (7 - self.bits % 8)
            self.bits += 1

    @property
    def bytes(self) -> bytes:
        return bytes(self.data)


def _build_parser():
    p = argparse.ArgumentParser(
        prog="nrsc5py",
        description="NRSC-5 (HD Radio) receiver — instructional Python port")
    p.add_argument("play", nargs="?", default=None,
                   help="'play' to stream into a local player")
    p.add_argument("-H", "--rtltcp", help="rtl_tcp host:port")
    p.add_argument("-r", "--input", help="read cu8 IQ from file")
    p.add_argument("-f", "--freq", type=float, help="center frequency (Hz)")
    p.add_argument("-g", "--gain", type=float, help="tuner gain (dB)")
    p.add_argument("-p", "--ppm", type=int, help="frequency correction")
    p.add_argument("-T", "--bias-tee", action="store_true")
    p.add_argument("-am", "--am", action="store_true", dest="am")
    p.add_argument("-o", "--output", help="audio output (wav file, '-' = stdout)")
    p.add_argument("-t", "--audio-type", default="wav",
                   choices=["wav", "raw"], help="audio output format")
    p.add_argument("--dump-hdc", help="dump HDC packets as ADTS")
    p.add_argument("--dump-aas-files", help="save received data files")
    p.add_argument("-P", "--player", default="mplayer",
                  help="player for 'play' mode")
    p.add_argument("program", nargs="?", type=int, help="audio program 0-7")
    return p


if __name__ == "__main__":
    main()
