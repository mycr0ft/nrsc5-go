"""OFDM sync and demodulation (mirrors sync.go).

sync collects 32 symbols (one L1 block) into buffer[subcarrier][symbol],
then process_fm runs:

1. Costas loop on each reference subcarrier (known sync pattern),
2. reference decode for block counter / service mode,
3. partition calibration (adjust_data),
4. MER calculation,
5. soft demodulation of the data subcarriers into ±1 soft bits.
"""

from __future__ import annotations

import math

import numpy as np

from .defines import (
    BLKSZ,
    CP_FM,
    FFT_FM,
    LB_START,
    PARTITION_DATA_CARRIERS,
    PARTITION_WIDTH_FM,
    PM_PARTITIONS,
    UB_END,
)

MAX_PARTITIONS = 14
MIDDLE_REF_SC = 30  # midpoint of table 11-3 in 1011s.pdf

# Table 6-4 in 1011s.pdf: compatibility mode from psmi.
COMPATIBILITY_MODE = [
    0, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
    6, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
    6, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
    6, 1, 2, 3, 1, 5, 6, 5, 6, 1, 2, 11, 1, 5, 6, 5,
]

# Differentially-encoded sync & parity bits (adjust_ref).
SYNC_PATTERN = np.array(
    [-1, 1, -1, -1, -1, 1, 1, 0, 1, -1, 0, 0, 0, -1, -1, 0,
     0, 0, 0, 0, -1, 1, -1, 0, 0, 0, 0, 0, 0, 0, 0, -1], dtype=np.float32)


def gray4(f: float) -> int:
    if f < -1:
        return 0
    if f < 0:
        return 2
    if f < 1:
        return 3
    return 1


def gray8(f: float) -> int:
    if f < -3:
        return 0
    if f < -2:
        return 4
    if f < -1:
        return 6
    if f < 0:
        return 2
    if f < 1:
        return 3
    if f < 2:
        return 7
    if f < 3:
        return 5
    return 1


def demod(x: float, mult: float) -> int:
    clamped = max(-1.0, min(1.0, x))
    return round(clamped * mult)


def qpsk(cf: complex) -> int:
    return (1 if cf.real >= 0 else 0) | (2 if cf.imag >= 0 else 0)


def qam16(cf: complex) -> int:
    return gray4(cf.real) | (gray4(cf.imag) << 2)


def qam64(cf: complex) -> int:
    return gray8(cf.real) | (gray8(cf.imag) << 3)


def phase_diff(a: float, b: float) -> float:
    diff = a - b
    while diff > math.pi / 2:
        diff -= math.pi
    while diff < -math.pi / 2:
        diff += math.pi
    return diff


class Sync:
    def __init__(self, inp):
        self.input = inp
        self.buffer = np.zeros((FFT_FM, BLKSZ), dtype=np.complex64)
        self.phases = np.zeros((FFT_FM, BLKSZ), dtype=np.float32)
        self.idx = 0
        self.psmi = 1
        self.pli = -1
        self.hppi = -1
        self.aabi = -1
        self.rdbi = -1
        self.cfo_wait = 0
        self.bc = 0
        self.offset_history = 0
        self.samperr = 0
        self.angle = 0.0

        loop_bw = 0.05
        damping = 0.70710678
        denom = 1 + 2 * damping * loop_bw + loop_bw * loop_bw
        self.alpha = 4 * damping * loop_bw / denom
        self.beta = 4 * loop_bw * loop_bw / denom
        self.costas_freq = np.zeros(FFT_FM, dtype=np.float32)
        self.costas_phase = np.zeros(FFT_FM, dtype=np.float32)

        self.mer_cnt = 0
        self.error_lb = 0.0
        self.error_ub = 0.0
        self.reset()

    def reset(self):
        self.costas_freq[:] = 0
        self.costas_phase[:] = 0
        self.idx = 0
        self.psmi = 1
        self.pli = self.hppi = self.aabi = self.rdbi = -1
        self.cfo_wait = 0
        self.offset_history = 0
        self.mer_cnt = 0
        self.error_lb = self.error_ub = 0.0

    # --- reference subcarrier handling -------------------------------------

    def adjust_ref(self, ref: int, cfo: int):
        cfo_freq = 2 * math.pi * cfo * CP_FM / FFT_FM
        for n in range(BLKSZ):
            error = np.angle(self.buffer[ref][n] * self.buffer[ref][n]
                             * np.exp(-2j * self.costas_phase[ref])) * 0.5
            self.phases[ref][n] = self.costas_phase[ref]
            self.buffer[ref][n] *= np.exp(-1j * self.costas_phase[ref])
            self.costas_freq[ref] += self.beta * error
            self.costas_freq[ref] = max(-0.5, min(0.5, self.costas_freq[ref]))
            self.costas_phase[ref] += (self.costas_freq[ref] + cfo_freq
                                       + self.alpha * error)
            if self.costas_phase[ref] > math.pi:
                self.costas_phase[ref] -= 2 * math.pi
            if self.costas_phase[ref] < -math.pi:
                self.costas_phase[ref] += 2 * math.pi
        x = sum(self.buffer[ref][n].real * SYNC_PATTERN[n] for n in range(BLKSZ))
        if x < 0:
            for n in range(BLKSZ):
                self.phases[ref][n] += math.pi
                self.buffer[ref][n] *= -1
            self.costas_phase[ref] += math.pi

    def reset_ref(self, ref: int):
        for n in range(BLKSZ):
            self.buffer[ref][n] *= np.exp(1j * self.phases[ref][n])

    def decode_dbpsk(self, buf, data, size: int):
        prev = 0
        for n in range(size):
            bit = 0 if buf[n].real <= 0 else 1
            data[n] = bit ^ prev
            prev = bit

    def fuzzy_match(self, needle, data, size: int) -> int:
        for n in range(size):
            for i, nd in enumerate(needle):
                if nd < 0:
                    continue
                if nd != data[(n + i) % size]:
                    break
            else:
                return n
        return -1

    _FM_NEEDLE_BASE = [0, 1, 0, 0, 0, 1, 1, -1, 1, 0]

    def decode_ref_fm(self, ref: int, rsid: int):
        needle = self._fm_needle(rsid)
        for n in range(BLKSZ):
            if needle[n] >= 0:
                bit = 0 if self.buffer[ref][n].real <= 0 else 1
                if needle[n] != bit:
                    return None
        data = [0] * BLKSZ
        self.decode_dbpsk(self.buffer[ref], data, BLKSZ)
        bc = (data[16] << 3) | (data[17] << 2) | (data[18] << 1) | data[19]
        psmi = ((data[25] << 5) | (data[26] << 4) | (data[27] << 3)
                | (data[28] << 2) | (data[29] << 1) | data[30])
        return bc, psmi

    def _fm_needle(self, rsid: int):
        base = list(self._FM_NEEDLE_BASE)
        base += [rsid >> 1, (rsid >> 1) ^ (rsid & 1), -1, 0, 0, -1,
                 -1, -1, -1, -1, 0, 1, 0, -1, -1, -1, -1, -1, -1, -1, -1, 0]
        return base

    def find_ref_fm(self, ref: int, rsid: int) -> int:
        needle = self._fm_needle(rsid)
        data = [0 if self.buffer[ref][n].real <= 0 else 1 for n in range(BLKSZ)]
        match = self.fuzzy_match(needle, data, BLKSZ)
        if match >= 0:
            return match
        data = [b ^ 1 for b in data]
        return self.fuzzy_match(needle, data, BLKSZ)

    # --- data partition calibration ----------------------------------------

    def calc_smag(self, ref: int) -> float:
        return sum(abs(self.buffer[ref][n].real) for n in range(BLKSZ)) / BLKSZ

    def adjust_data(self, lower: int, upper: int):
        smag0 = self.calc_smag(lower)
        smag19 = self.calc_smag(upper)
        for n in range(BLKSZ):
            upper_phase = np.exp(1j * self.phases[upper][n])
            lower_phase = np.exp(1j * self.phases[lower][n])
            for k in range(1, PARTITION_WIDTH_FM):
                c = complex(PARTITION_WIDTH_FM, PARTITION_WIDTH_FM) / (
                    k * smag19 * upper_phase
                    + (PARTITION_WIDTH_FM - k) * smag0 * lower_phase)
                self.buffer[lower + k][n] *= c

    # --- CFO detection ------------------------------------------------------

    def detect_cfo(self):
        for cfo in range(-2 * PARTITION_WIDTH_FM, 2 * PARTITION_WIDTH_FM):
            best_offset = -1
            best_count = 0
            offset_count = [0] * BLKSZ
            for i in range(PM_PARTITIONS + 1):
                self.adjust_ref(cfo + LB_START + i * PARTITION_WIDTH_FM, cfo)
                offset = self.find_ref_fm(cfo + LB_START + i * PARTITION_WIDTH_FM,
                                          (MIDDLE_REF_SC - i) & 0x3)
                self.reset_ref(cfo + LB_START + i * PARTITION_WIDTH_FM)
                if offset >= 0:
                    offset_count[offset] += 1
                self.adjust_ref(cfo + UB_END - i * PARTITION_WIDTH_FM, cfo)
                offset = self.find_ref_fm(cfo + UB_END - i * PARTITION_WIDTH_FM,
                                          (MIDDLE_REF_SC - i) & 0x3)
                self.reset_ref(cfo + UB_END - i * PARTITION_WIDTH_FM)
                if offset >= 0:
                    offset_count[offset] += 1
            best_offset = int(np.argmax(offset_count))
            best_count = offset_count[best_offset]
            if best_offset >= 0 and best_count >= 3:
                self.input.acq.set_keep_extra(((BLKSZ - best_offset) % BLKSZ)
                                              * 2160)
                self.input.acq.cfo_adjust(cfo)
                self.cfo_wait = 8
                break

    # --- main FM block processing -------------------------------------------

    def process_fm(self):
        psmi = self.psmi
        cm = COMPATIBILITY_MODE[psmi]
        ppb = {2: 11, 3: 12, 5: 14, 6: 14, 11: 14}.get(cm, 10)

        for i in range(0, ppb * PARTITION_WIDTH_FM + 1, PARTITION_WIDTH_FM):
            self.adjust_ref(LB_START + i, 0)
            self.adjust_ref(UB_END - i, 0)

        if self.input.sync_state == 1:  # COARSE
            good_refs = 0
            seen_bc = [0] * 16
            seen_psmi = [0] * 64
            for i in range(ppb + 1):
                for sc in (LB_START + i * PARTITION_WIDTH_FM,
                           UB_END - i * PARTITION_WIDTH_FM):
                    r = self.decode_ref_fm(sc, (MIDDLE_REF_SC - i) & 0x3)
                    if r is not None:
                        good_refs += 1
                        seen_bc[r[0]] += 1
                        seen_psmi[r[1]] += 1
            if good_refs >= 4:
                majority_bc = next((bc for bc in range(16)
                                    if seen_bc[bc] > good_refs // 2), -1)
                majority_psmi = next((p for p in range(16)
                                      if seen_psmi[p] > good_refs // 2), -1)
                if majority_bc >= 0 and majority_psmi >= 0:
                    self.bc = majority_bc
                    self.psmi = majority_psmi
                    self.input.set_sync_state(2)  # FINE
                    self.input.decode.reset()
                    self.input.frame.reset()
            elif self.cfo_wait == 0:
                self.detect_cfo()
            else:
                self.cfo_wait -= 1

        if self.input.sync_state == 2:  # FINE
            self._process_fm_fine(ppb)

    def _process_fm_fine(self, ppb: int):
        samperr = angle = sum_xy = sum_x2 = 0.0
        for i in range(0, ppb * PARTITION_WIDTH_FM, PARTITION_WIDTH_FM):
            self.adjust_data(LB_START + i, LB_START + i + PARTITION_WIDTH_FM)
            self.adjust_data(UB_END - i - PARTITION_WIDTH_FM, UB_END - i)
            samperr += phase_diff(self.phases[LB_START + i][0],
                                  self.phases[LB_START + i + PARTITION_WIDTH_FM][0])
            samperr += phase_diff(self.phases[UB_END - i - PARTITION_WIDTH_FM][0],
                                  self.phases[UB_END - i][0])
        samperr = samperr / (ppb * 2) * FFT_FM / PARTITION_WIDTH_FM / (2 * math.pi)

        for i in range(0, ppb * PARTITION_WIDTH_FM + 1, PARTITION_WIDTH_FM):
            x = LB_START + i - FFT_FM // 2
            y = self.costas_freq[LB_START + i]
            angle += y
            sum_xy += x * y
            sum_x2 += x * x
            x = UB_END - i - FFT_FM // 2
            y = self.costas_freq[UB_END - i]
            angle += y
            sum_xy += x * y
            sum_x2 += x * x
        samperr -= (sum_xy / sum_x2) * FFT_FM / (2 * math.pi) * 32
        self.samperr = round(samperr)

        angle /= (ppb + 1) * 2
        self.angle = angle
        for i in range(0, ppb * PARTITION_WIDTH_FM + 1, PARTITION_WIDTH_FM):
            self.costas_freq[LB_START + i] -= angle
            self.costas_freq[UB_END - i] -= angle

        # MER
        error_lb = error_ub = 0.0
        for n in range(BLKSZ):
            for i in range(0, ppb * PARTITION_WIDTH_FM, PARTITION_WIDTH_FM):
                for j in range(1, PARTITION_WIDTH_FM):
                    c = self.buffer[LB_START + i + j][n]
                    ideal = complex(1 if c.real >= 0 else -1,
                                    1 if c.imag >= 0 else -1)
                    error_lb += abs(ideal - c) ** 2
                    c = self.buffer[UB_END - i - PARTITION_WIDTH_FM + j][n]
                    ideal = complex(1 if c.real >= 0 else -1,
                                    1 if c.imag >= 0 else -1)
                    error_ub += abs(ideal - c) ** 2
        self.error_lb += error_lb
        self.error_ub += error_ub
        self.mer_cnt += 1
        if self.mer_cnt == 16:
            signal = 2 * BLKSZ * (ppb * PARTITION_DATA_CARRIERS) * self.mer_cnt
            self.input.radio.report_mer(
                10 * math.log10(signal / self.error_lb),
                10 * math.log10(signal / self.error_ub))
            self.mer_cnt = 0
            self.error_lb = self.error_ub = 0.0

        # Soft demod
        mer_lb = 2.0 * BLKSZ * (ppb * PARTITION_DATA_CARRIERS) / error_lb
        mer_ub = 2.0 * BLKSZ * (ppb * PARTITION_DATA_CARRIERS) / error_ub
        mult_lb = max(1.0, min(mer_lb * 10, 127))
        mult_ub = max(1.0, min(mer_ub * 10, 127))

        buffer_pm = []
        buffer_px1 = []
        buffer_px2 = []
        for n in range(BLKSZ):
            for i in range(LB_START, LB_START + PM_PARTITIONS * PARTITION_WIDTH_FM,
                           PARTITION_WIDTH_FM):
                for j in range(1, PARTITION_WIDTH_FM):
                    c = self.buffer[i + j][n]
                    buffer_pm.append(demod(c.real, mult_lb))
                    buffer_pm.append(demod(c.imag, mult_lb))
            for i in range(UB_END - PM_PARTITIONS * PARTITION_WIDTH_FM, UB_END,
                           PARTITION_WIDTH_FM):
                for j in range(1, PARTITION_WIDTH_FM):
                    c = self.buffer[i + j][n]
                    buffer_pm.append(demod(c.real, mult_ub))
                    buffer_pm.append(demod(c.imag, mult_ub))
            cm = COMPATIBILITY_MODE[self.psmi]
            if cm == 2:
                for j in range(1, PARTITION_WIDTH_FM):
                    c = self.buffer[
                        LB_START + PM_PARTITIONS * PARTITION_WIDTH_FM + j][n]
                    buffer_px1.append(demod(c.real, mult_lb))
                    buffer_px1.append(demod(c.imag, mult_lb))
                    c = self.buffer[
                        UB_END - (PM_PARTITIONS + 1) * PARTITION_WIDTH_FM + j][n]
                    buffer_px1.append(demod(c.real, mult_ub))
                    buffer_px1.append(demod(c.imag, mult_ub))
            if cm in (3, 11):
                for i in range(LB_START + PM_PARTITIONS * PARTITION_WIDTH_FM,
                               LB_START + (PM_PARTITIONS + 2) * PARTITION_WIDTH_FM,
                               PARTITION_WIDTH_FM):
                    for j in range(1, PARTITION_WIDTH_FM):
                        c = self.buffer[i + j][n]
                        buffer_px1.append(demod(c.real, mult_lb))
                        buffer_px1.append(demod(c.imag, mult_lb))
                for i in range(UB_END - (PM_PARTITIONS + 2) * PARTITION_WIDTH_FM,
                               UB_END - PM_PARTITIONS * PARTITION_WIDTH_FM,
                               PARTITION_WIDTH_FM):
                    for j in range(1, PARTITION_WIDTH_FM):
                        c = self.buffer[i + j][n]
                        buffer_px1.append(demod(c.real, mult_ub))
                        buffer_px1.append(demod(c.imag, mult_ub))
            if cm == 11:
                for i in range(LB_START + (PM_PARTITIONS + 2) * PARTITION_WIDTH_FM,
                               LB_START + (PM_PARTITIONS + 4) * PARTITION_WIDTH_FM,
                               PARTITION_WIDTH_FM):
                    for j in range(1, PARTITION_WIDTH_FM):
                        c = self.buffer[i + j][n]
                        buffer_px2.append(demod(c.real, mult_lb))
                        buffer_px2.append(demod(c.imag, mult_lb))
                for i in range(UB_END - (PM_PARTITIONS + 4) * PARTITION_WIDTH_FM,
                               UB_END - (PM_PARTITIONS + 2) * PARTITION_WIDTH_FM,
                               PARTITION_WIDTH_FM):
                    for j in range(1, PARTITION_WIDTH_FM):
                        c = self.buffer[i + j][n]
                        buffer_px2.append(demod(c.real, mult_ub))
                        buffer_px2.append(demod(c.imag, mult_ub))

        self.input.decode.push_pm(buffer_pm, self.bc)
        if buffer_px1:
            self.input.decode.push_px1(buffer_px1, self.bc)
        if buffer_px2:
            self.input.decode.push_px2(buffer_px2, self.bc)
        self.bc = (self.bc + 1) % 16

    # --- push ---------------------------------------------------------------

    def push(self, fftout):
        if self.input.radio.mode == 0:  # FM
            for i in range(MAX_PARTITIONS * PARTITION_WIDTH_FM + 1):
                self.buffer[LB_START + i][self.idx] = fftout[LB_START + i]
                self.buffer[UB_END - i][self.idx] = fftout[UB_END - i]
        self.idx += 1
        if self.idx == BLKSZ:
            self.idx = 0
            if self.input.radio.mode == 0:
                self.process_fm()

    def adjust(self, sample_adj: int):
        for i in range(MAX_PARTITIONS * PARTITION_WIDTH_FM + 1):
            self.costas_phase[LB_START + i] -= (sample_adj
                * (LB_START + i - FFT_FM // 2) * 2 * math.pi / FFT_FM)
            self.costas_phase[UB_END - i] -= (sample_adj
                * (UB_END - i - FFT_FM // 2) * 2 * math.pi / FFT_FM)
