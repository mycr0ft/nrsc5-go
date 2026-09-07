"""NRSC-5 constants and helpers (mirrors defines.go).

Most of these come straight from the NRSC-5 spec documents:
- 1011s.pdf defines the OFDM parameters (FFT size, cyclic prefix,
  subcarrier allocation) and the service modes.
- 1012s.pdf defines the frame lengths, interleavers, and transports.
"""

from __future__ import annotations

import numpy as np

# --- OFDM physical layer (1011s) -------------------------------------------

# FFT length in samples. FM uses 2048 (matching a ~370 kHz OFDM band),
# AM uses 256.
FFT_FM = 2048
FFT_AM = 256

# Cyclic prefix length in samples (the guard interval repeated before
# each symbol body).
CP_FM = 112
CP_AM = 14

FFTCP_FM = FFT_FM + CP_FM   # 2160 samples per OFDM symbol
FFTCP_AM = FFT_AM + CP_AM   # 270 samples per AM symbol

# OFDM symbols per L1 block. The receiver syncs and demodulates in
# block-sized units.
BLKSZ = 32

# Symbols processed per acquire invocation (one FFT push per symbol,
# so acquire fills fftcp*(BLKSZ+1) samples before running).
ACQUIRE_SYMBOLS = BLKSZ

# Index of the first lower-sideband subcarrier in the FFT output.
# FM allocates subcarriers ±130..280 kHz around the analog carrier;
# after the FFT shift, subcarrier index i sits at frequency
# (i - FFT/2) * 744188/FFT Hz.
LB_START = (FFT_FM // 2) - 546
UB_END = (FFT_FM // 2) + 546

# AM subcarrier indices (relative to the DC carrier).
CENTER_AM = FFT_AM // 2
REF_INDEX_AM = 1
PIDS_INNER_INDEX_AM = 27
PIDS_OUTER_INDEX_AM = 53
INNER_PARTITION_START_AM = 2
MIDDLE_PARTITION_START_AM = 28
OUTER_PARTITION_START_AM = 57
MAX_INDEX_AM = 81

# AM service modes (psmi values).
SERVICE_MODE_MA1 = 1
SERVICE_MODE_MA3 = 2

# Bits per P1 frame (main audio program, FM and AM).
P1_FRAME_LEN_FM = 146176
P1_FRAME_LEN_AM = 3750

# Bits per encoded P1 frame: rate 1/3 conv coding punctured to 2/5.
# Encoded length = length * (1/3) / (2/5) = length * 5/2.
P1_FRAME_LEN_ENCODED_FM = P1_FRAME_LEN_FM * 5 // 2
P1_FRAME_LEN_ENCODED_AM = P1_FRAME_LEN_AM * 12 // 5

# Bits per PIDS frame (Station Information Service).
PIDS_FRAME_LEN = 80
PIDS_FRAME_LEN_ENCODED_FM = PIDS_FRAME_LEN * 5 // 2
PIDS_FRAME_LEN_ENCODED_AM = PIDS_FRAME_LEN * 3

# Bits per P3 frame. MP3/MP11 is the FM supplemental frame; MA1/MA3 are
# the AM variants.
P3_FRAME_LEN_MP2 = 2304
P3_FRAME_LEN_MP3_MP11 = 4608
P3_FRAME_LEN_MA1 = 24000
P3_FRAME_LEN_MA3 = 30000

P3_FRAME_LEN_ENCODED_MA1 = P3_FRAME_LEN_MA1 * 3 // 2
P3_FRAME_LEN_ENCODED_MA3 = P3_FRAME_LEN_MA3 * 12 // 5

# Bits per L2 PCI (frame header CRC block) — used to locate the header
# inside a P1 frame.
PCI_LEN = 24

# Maximum bytes per L2 PDU.
MAX_PDU_LEN = (P1_FRAME_LEN_FM - PCI_LEN) // 8

# Bytes per L2 PDU in an AM P1 frame.
P1_PDU_LEN_AM = 466

# MAX_PROGRAMS audio programs (0..7), MAX_STREAMS streams per program.
MAX_PROGRAMS = 8
MAX_STREAMS = 2

# Audio packets in the elastic jitter buffer.
ELASTIC_BUFFER_LEN = 64

# Subcarriers per partition (FM 19 = 1 reference + 18 data; AM 25).
PARTITION_WIDTH_FM = 19
PARTITION_DATA_CARRIERS = 18
PARTITION_WIDTH_AM = 25

# Partitions in each Primary Main (PM) sideband.
PM_PARTITIONS = 10

# One PM block: 2 sidebands × 10 partitions × 18 carriers × 32 symbols.
PM_BLOCK_SIZE = 2 * 2 * PM_PARTITIONS * PARTITION_DATA_CARRIERS * BLKSZ

# Sample rates (Hz).
SAMPLE_RATE_CU8 = 1488375     # rtl_tcp rate: 2x the OFDM rate
SAMPLE_RATE_CS16_FM = 744188  # OFDM rate after 2:1 decimation
SAMPLE_RATE_CS16_AM = 46512
SAMPLE_RATE_AUDIO = 44100     # HDC output audio

# Logical channel identifiers.
P1_LOGICAL_CHANNEL = 0
P3_LOGICAL_CHANNEL = 1
P4_LOGICAL_CHANNEL = 2

# AM decimation stages (5 × 2:1 halfband = 32:1).
AM_DECIM_STAGES = 5


def u8_f(x: int) -> float:
    """Convert an unsigned 8-bit I/Q sample to a float in [-1, 1)."""
    return (float(x) - 127.0) / 128.0


def u8_q15(x: int) -> int:
    """Convert an unsigned 8-bit sample to Q15 fixed point."""
    return (int(x) - 127) * 64


class Cint16:
    """Q15 fixed-point complex sample (mirrors cint16_t)."""

    __slots__ = ("re", "im")

    def __init__(self, re: int, im: int):
        self.re = re
        self.im = im


def cq15_to_cf(v: Cint16) -> complex:
    return complex(v.re / 32767.0, v.im / 32767.0)


def cq15_to_cf_conj(v: Cint16) -> complex:
    return complex(v.re / 32767.0, -v.im / 32767.0)