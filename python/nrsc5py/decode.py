"""L1 FEC driver: deinterleavers, depuncture, FEC dispatch (decode.go)."""

from __future__ import annotations

import numpy as np

from .defines import (
    BLKSZ, P1_FRAME_LEN_ENCODED_FM, P1_FRAME_LEN_FM, PARTITION_WIDTH_AM,
    P3_FRAME_LEN_MP3_MP11, PM_BLOCK_SIZE, PIDS_FRAME_LEN,
    PIDS_FRAME_LEN_ENCODED_FM, SERVICE_MODE_MA3,
)
from .conv import conv_decode_p1, conv_decode_pids, conv_decode_p3_p4

PM_V_SIZE = 20

BL_DELAY = [2, 1, 5]
ML_DELAY = [11, 6, 7]
BU_DELAY = [10, 8, 9]
MU_DELAY = [4, 3, 0]
EL_DELAY = [0, 1]
EU_DELAY = [2, 3, 5, 4]

# 1012s.pdf partition schedule for the PM interleavers.
PM_V = [10, 2, 18, 6, 14, 8, 16, 0, 12, 4,
        11, 3, 19, 7, 15, 9, 17, 1, 13, 5]

# 1012s.pdf figure 10-5: AM PIDS bit mapping.
PIDS_IL_DELAY = [0, 1, 12, 13, 6, 5, 18, 17, 11, 7, 23, 19]
PIDS_IU_DELAY = [2, 4, 14, 16, 3, 8, 15, 20, 9, 10, 21, 22]


def bit_map(matrix, b: int, k: int, p: int) -> int:
    """AM bit mapping (decode.c bit_map)."""
    col = (9 * k) % 25
    row = (11 * col + 16 * (k // 25) + 11 * (k // 50)) % 32
    return (matrix[PARTITION_WIDTH_AM * (b * BLKSZ + row) + col] >> p) & 1


def bit_errors(coded, decoded, k: int, frame_len: int, gens,
               puncture, puncture_len: int) -> int:
    """Re-encode and count symbol disagreements (BER estimate)."""
    r = 0
    errors = 0
    for i in range(k - 1):
        r = (r >> 1) | (int(decoded[frame_len - (k - 1) + i]) << (k - 1))
    for i in range(frame_len):
        r = (r >> 1) | (int(decoded[i]) << (k - 1))
        for bit_idx in range(3):
            if puncture[(3 * i + bit_idx) % puncture_len]:
                coded_sym = coded[3 * i + bit_idx] > 0
                parity = bin(r & gens[bit_idx]).count("1") & 1
                if coded_sym != (parity == 1):
                    errors += 1
    return errors


def descramble(buf: bytearray, length: int):
    """L2 scrambler: XOR with a 11-bit LFSR sequence."""
    val = 0x3FF
    for i in range(0, length, 8):
        for j in range(8):
            bit = ((val >> 9) ^ val) & 1
            val |= bit << 11
            val >>= 1
            buf[i + j] ^= bit


def interleaver_i(in_pm, viterbi, J=20, B=16, C=36, M=1, N=P1_FRAME_LEN_ENCODED_FM):
    """Type I interleaver (P1). in_pm is buffer_pm (PM_BLOCK_SIZE*16)."""
    out = 0
    length_v = PM_V_SIZE
    for i in range(N):
        partition = PM_V[((i + 2 * (M // 4)) // M) % length_v]
        if M == 1:
            block = ((i // J) + partition * 7) % B
        else:
            block = (i + (i // (J * B))) % B
        k = i // (J * B)
        row = (k * 11) % 32
        column = (k * 11 + k // (32 * 9)) % C
        viterbi[out] = in_pm[(block * 32 + row) * (J * C) + partition * C + column]
        out += 1
        if out % 6 == 5:  # depuncture [1,1,1,1,1,0]
            viterbi[out] = 0
            out += 1


def interleaver_ii(in_pm, viterbi, bc: int, J=20, B=16, C=36,
                   b=PIDS_FRAME_LEN_ENCODED_FM, I0=P1_FRAME_LEN_ENCODED_FM):
    """Type II interleaver (PIDS)."""
    out = 0
    for i in range(bc * b, (bc + 1) * b):
        partition = PM_V[i % PM_V_SIZE]
        block = i // b
        k = ((i // J) % (b // J)) + (I0 // (J * B))
        row = (k * 11) % 32
        column = (k * 11 + k // (32 * 9)) % C
        viterbi[out] = in_pm[(block * 32 + row) * (J * C) + partition * C + column]
        out += 1
        if out % 6 == 5:
            viterbi[out] = 0
            out += 1


class InterleaverIV:
    """Streaming type IV interleaver for PX1/PX2 (P3/P4)."""

    def __init__(self):
        self.buffer = [0] * (P3_FRAME_LEN_MP3_MP11 * 2)
        self.internal = [0] * (P3_FRAME_LEN_MP3_MP11 * 32)
        self.i = 0
        self.pt = [0, 0, 0, 0]
        self.ready = False
        self.started = False

    def reset(self):
        self.i = 0
        self.pt = [0, 0, 0, 0]
        self.ready = False
        self.started = False

    def run(self, viterbi, frame_len: int):
        J = 4 if frame_len == P3_FRAME_LEN_MP3_MP11 else 2
        B = 32
        C = 36
        M = 2 if frame_len == P3_FRAME_LEN_MP3_MP11 else 4
        N = 147456 if frame_len == P3_FRAME_LEN_MP3_MP11 else 73728
        bk_bits = 32 * C
        bk_adj = 32 * C - 1

        if self.i == N:
            self.i = 0
            self.pt = [0, 0, 0, 0]
            self.ready = True

        out = 0
        for i in range(frame_len * 2):
            partition = ((self.i + 2 * (M // 4)) // M) % J
            pti = self.pt[partition]
            self.pt[partition] += 1
            block = (pti + partition * 7 - bk_adj * (pti // bk_bits)) % B
            row = ((11 * pti) % bk_bits) // C
            column = (pti * 11) % C
            viterbi[out] = self.internal[(block * 32 + row) * (J * C)
                                         + partition * C + column]
            out += 1
            if out % 6 in (1, 4):  # depuncture [1,0,1,1,0,1]
                viterbi[out] = 0
                out += 1
            self.internal[self.i] = self.buffer[i]
            self.i += 1


class Decode:
    def __init__(self, inp):
        self.input = inp
        size = PM_BLOCK_SIZE * 16
        self.buffer_pm = [0] * size
        self.started_pm = False
        self.buffer_pl = np.zeros(PARTITION_WIDTH_AM * BLKSZ * 8, dtype=np.uint8)
        self.buffer_pu = np.zeros(PARTITION_WIDTH_AM * BLKSZ * 8, dtype=np.uint8)
        self.buffer_s = np.zeros(PARTITION_WIDTH_AM * BLKSZ * 8, dtype=np.uint8)
        self.buffer_t = np.zeros(PARTITION_WIDTH_AM * BLKSZ * 8, dtype=np.uint8)
        self.am_errors = 0
        self.am_diversity_wait = 4
        self.interleaver_px1 = InterleaverIV()
        self.interleaver_px2 = InterleaverIV()
        self.viterbi_p1 = [0] * (P1_FRAME_LEN_FM * 3)
        self.scrambler_p1 = bytearray(P1_FRAME_LEN_FM)
        self.viterbi_pids = [0] * (PIDS_FRAME_LEN * 3)
        self.scrambler_pids = bytearray(PIDS_FRAME_LEN)
        self.viterbi_p3 = [0] * (P3_FRAME_LEN_MP3_MP11 * 3)
        self.scrambler_p3 = bytearray(P3_FRAME_LEN_MP3_MP11)
        self.viterbi_p4 = [0] * (P3_FRAME_LEN_MP3_MP11 * 3)
        self.scrambler_p4 = bytearray(P3_FRAME_LEN_MP3_MP11)
        from .pids import Pids
        self.pids = Pids(self.input)
        self.reset()

    def reset(self):
        self.started_pm = False
        self.am_errors = 0
        self.am_diversity_wait = 4
        self.interleaver_px1.reset()
        self.interleaver_px2.reset()
        self.pids.init(self.input)

    def push_pm(self, sbit, bc: int):
        base = PM_BLOCK_SIZE * bc
        self.buffer_pm[base:base + PM_BLOCK_SIZE] = sbit
        self.process_pids(bc)
        if bc == 0:
            self.started_pm = True
        if self.started_pm and bc == 15:
            self.process_p1()

    def process_p1(self):
        viterbi = [0] * (P1_FRAME_LEN_FM * 3)
        interleaver_i(self.buffer_pm, viterbi)
        bits = conv_decode_p1(np.array(viterbi, dtype=np.int64))
        self.input.radio.report_ber(
            bit_errors_2_5_fm(viterbi, bits, P1_FRAME_LEN_FM)
            / P1_FRAME_LEN_ENCODED_FM)
        # pack decoded bits LSB-first into bytes
        bits_bytes = np.packbits(np.asarray(bits, dtype=np.uint8),
                                 bitorder="little")
        self.scrambler_p1[:len(bits_bytes)] = bits_bytes
        descramble(self.scrambler_p1, P1_FRAME_LEN_FM)
        self.input.frame.push(self.scrambler_p1, P1_FRAME_LEN_FM, 0)

    def process_pids(self, bc: int):
        viterbi = [0] * (PIDS_FRAME_LEN * 3)
        interleaver_ii(self.buffer_pm, viterbi, bc)
        bits = conv_decode_pids(np.array(viterbi, dtype=np.int64))
        bits_bytes = bytes(np.packbits(np.asarray(bits, dtype=np.uint8),
                                       bitorder="little"))
        self.scrambler_pids[:len(bits_bytes)] = bits_bytes
        descramble(self.scrambler_pids, PIDS_FRAME_LEN)
        self.pids.frame_push(bytes(self.scrambler_pids))

    def push_px1(self, sbit, bc: int):
        length = P3_FRAME_LEN_MP3_MP11
        if bc % 2 == 0:
            self.interleaver_px1.started = True
        if self.interleaver_px1.started:
            self.interleaver_px1.buffer[length * (bc % 2):length * (bc % 2 + 1)] = sbit
            if bc % 2 == 1:
                viterbi = [0] * (length * 3)
                self.interleaver_px1.run(viterbi, length)
                if self.interleaver_px1.ready:
                    bits = conv_decode_p3_p4(np.array(viterbi, dtype=np.int64), length)
                    bb = np.packbits(np.asarray(bits, dtype=np.uint8),
                                     bitorder="little")
                    self.scrambler_p3[:len(bb)] = bb
                    descramble(self.scrambler_p3, length)
                    self.input.frame.push(self.scrambler_p3, length, 1)

    def push_px2(self, sbit, bc: int):
        length = P3_FRAME_LEN_MP3_MP11
        if bc % 2 == 0:
            self.interleaver_px2.started = True
        if self.interleaver_px2.started:
            self.interleaver_px2.buffer[length * (bc % 2):length * (bc % 2 + 1)] = sbit
            if bc % 2 == 1:
                viterbi = [0] * (length * 3)
                self.interleaver_px2.run(viterbi, length)
                if self.interleaver_px2.ready:
                    bits = conv_decode_p3_p4(np.array(viterbi, dtype=np.int64), length)
                    bb = np.packbits(np.asarray(bits, dtype=np.uint8),
                                     bitorder="little")
                    self.scrambler_p4[:len(bb)] = bb
                    descramble(self.scrambler_p4, length)
                    self.input.frame.push(self.scrambler_p4, length, 2)


PUNCTURE_2_5 = [1, 1, 1, 1, 1, 0]


def bit_errors_2_5_fm(coded, decoded, length: int) -> int:
    return bit_errors(coded, decoded, 7, length, (0o133, 0o171, 0o165),
                      PUNCTURE_2_5, 6)