"""OFDM acquisition (mirrors acquire.go).

Acquire fills a window of FFTCP_FM*(ACQUIRE_SYMBOLS+1) samples, then:

1. Coarse mode: slide a symbol-length correlation over the window to
   find the sample- and phase-offset (the cyclic prefix makes the
   signal repeat itself 2048 samples later).
2. Fine mode: use sync's tracked sample error and Costas phase.

Then, for each of 32 symbols, assemble the FFT input (folding the
cyclic prefix into the tail, applying the pulse-shaping window and
phase rotation), run the FFT, and hand the spectrum to sync.
"""

from __future__ import annotations

import math

import numpy as np

from .defines import (
    ACQUIRE_SYMBOLS, CENTER_AM, CP_AM, CP_FM, FFT_AM, FFT_FM, FFTCP_AM,
    FFTCP_FM, ModeAM, ModeFM, PIDS_OUTER_INDEX_AM, UB_END, LB_START,
)
from .input import FILTER_TAPS_AM, FILTER_TAPS_FM

FILTER_DELAY = 15


class Acquire:
    def __init__(self, inp):
        self.input = inp
        self.mode = ModeFM
        self.fft = FFT_FM
        self.fftcp = FFTCP_FM
        self.cp = CP_FM
        self.filter_fm = inp.decim[0].__class__(FILTER_TAPS_FM)
        self.filter_am = inp.decim[0].__class__(FILTER_TAPS_AM)

        size = FFTCP_FM * (ACQUIRE_SYMBOLS + 1)
        self.in_buffer = np.zeros(size, dtype=np.complex64)
        self.buffer = np.zeros(size, dtype=np.complex64)
        self.sums = np.zeros(FFTCP_FM, dtype=np.complex64)
        self.fftin = np.zeros(FFT_FM, dtype=np.complex64)
        self.fftout = np.zeros(FFT_FM, dtype=np.complex64)

        # Pulse shaping window: raised cosine over the cyclic prefix.
        self.shape_fm = self._make_shape(FFT_FM, CP_FM)
        self.shape_am = self._make_shape(FFT_AM, CP_AM)
        self.shape = self.shape_fm

        self.idx = 0
        self.prev_angle = 0.0
        self.phase = complex(1, 0)
        self.keep_extra = 0
        self.cfo = 0
        self.reset()

    @staticmethod
    def _make_shape(fft: int, cp: int) -> np.ndarray:
        shape = np.ones(fft + cp, dtype=np.float32)
        for i in range(cp):
            shape[i] = math.sin(math.pi / 2 * i / cp)
            shape[fft + i] = math.cos(math.pi / 2 * i / cp)
        return shape

    def set_mode(self, mode: int):
        self.mode = mode
        if mode == ModeFM:
            self.fft, self.fftcp, self.cp = FFT_FM, FFTCP_FM, CP_FM
            self.shape = self.shape_fm
        else:
            self.fft, self.fftcp, self.cp = FFT_AM, FFTCP_AM, CP_AM
            self.shape = self.shape_am

    def reset(self):
        self.filter_fm.reset()
        self.filter_am.reset()
        self.idx = 0
        self.prev_angle = 0.0
        self.phase = complex(1, 0)
        self.keep_extra = 0
        self.cfo = 0

    def set_keep_extra(self, extra: int):
        self.keep_extra = extra

    def cfo_adjust(self, cfo: int):
        self.cfo += cfo

    def push(self, buf: np.ndarray) -> int:
        size = self.fftcp * (ACQUIRE_SYMBOLS + 1)
        needed = size - self.idx
        pushed = min(len(buf), needed)
        self.in_buffer[self.idx:self.idx + pushed] = buf[:pushed]
        self.idx += pushed
        return pushed

    def process(self):
        size = self.fftcp * (ACQUIRE_SYMBOLS + 1)
        if self.idx != size:
            return

        self.input.output.advance()

        if self.input.sync_state == 2:  # SYNC_STATE_FINE
            samperr = self.fftcp // 2 + self.input.sync.samperr
            self.input.sync.samperr = 0
            angle_diff = -self.input.sync.angle
            self.input.sync.angle = 0.0
            angle = self.prev_angle + angle_diff
            self.prev_angle = angle
        else:
            # Coarse acquisition: matched filter, then correlate the
            # signal against itself offset by one FFT length.
            y = 0j
            for i in range(size):
                if self.mode == ModeFM:
                    y = self.filter_fm.execute_fir(self.in_buffer[i])
                    self.buffer[i] = np.conj(y)
                else:
                    self.buffer[i] = self.filter_am.execute_fir(self.in_buffer[i])

            self.sums[:self.fftcp] = 0
            for i in range(self.fftcp):
                for j in range(ACQUIRE_SYMBOLS):
                    self.sums[i] += (self.buffer[i + j * self.fftcp]
                                     * np.conj(self.buffer[i + j * self.fftcp + self.fft]))

            max_mag = -1.0
            max_v = 0j
            samperr = 0
            for i in range(self.fftcp):
                v = 0j
                for j in range(self.cp):
                    v += (self.sums[(i + j) % self.fftcp]
                          * self.shape[j] * self.shape[j + self.fft])
                mag = abs(v) ** 2
                if mag > max_mag:
                    max_mag = mag
                    max_v = v
                    samperr = (i + self.fftcp - FILTER_DELAY) % self.fftcp

            angle_diff = np.angle(max_v * np.exp(-1j * self.prev_angle))
            angle_factor = 0.25 if self.prev_angle != 0 else 1.0
            angle = self.prev_angle + angle_diff * angle_factor
            self.prev_angle = angle
            self.input.set_sync_state(1)  # SYNC_STATE_COARSE

        conj = np.conj(self.in_buffer[:size]) if self.mode == ModeFM \
            else self.in_buffer[:size]
        self.buffer[:size] = conj

        self.input.sync.adjust(self.fftcp // 2 - samperr)
        angle -= 2 * math.pi * self.cfo

        self.phase *= np.exp(complex(0, -(self.fftcp // 2 - samperr)
                                     * angle / self.fft))
        phase_increment = np.exp(complex(0, angle / self.fft))

        if self.mode == ModeAM:
            self._am_cfo(phase_increment)

        for i in range(ACQUIRE_SYMBOLS):
            offset = 0 if self.mode == ModeFM else (FFT_AM - CP_AM) // 2
            for j in range(self.fftcp):
                sample = self.phase * self.buffer[i * self.fftcp + j + samperr]
                k = (j + offset) % self.fft
                if j < self.cp:
                    self.fftin[k] = self.shape[j] * sample
                elif j < self.fft:
                    self.fftin[k] = sample
                else:
                    self.fftin[k] += self.shape[j] * sample
                self.phase *= phase_increment
            self.phase /= abs(self.phase)

            self.fftout[:self.fft] = np.fft.fft(self.fftin[:self.fft])
            np.fft.fftshift(self.fftout[:self.fft])
            self.input.sync.push(self.fftout[:self.fft])

        keep = self.fftcp + (self.fftcp // 2 - samperr) + self.keep_extra
        self.keep_extra = 0
        self.in_buffer[:keep] = self.in_buffer[self.idx - keep:self.idx]
        self.idx = keep

    def _am_cfo(self, phase_increment):
        """AM center-carrier phase/frequency fit (acquire.c AM block)."""
        sum_y = sum_xy = sum_x2 = 0.0
        last_carrier = 0j
        temp_phase = self.phase
        mag_sums = np.zeros(FFT_AM, dtype=np.float32)

        for i in range(ACQUIRE_SYMBOLS):
            offset = (FFT_AM - CP_AM) // 2
            for j in range(self.fftcp):
                sample = temp_phase * self.buffer[i * self.fftcp + j + samperr]
                k = (j + offset) % self.fft
                if j < self.cp:
                    self.fftin[k] = self.shape[j] * sample
                elif j < self.fft:
                    self.fftin[k] = sample
                else:
                    self.fftin[k] += self.shape[j] * sample
                temp_phase *= phase_increment
            temp_phase /= abs(temp_phase)

            self.fftout[:self.fft] = np.fft.fft(self.fftin[:self.fft])
            np.fft.fftshift(self.fftout[:self.fft])

            x = self.fftcp * (i - (ACQUIRE_SYMBOLS - 1) / 2)
            if i == 0:
                y = np.angle(self.fftout[CENTER_AM])
            else:
                y += np.angle(self.fftout[CENTER_AM] / last_carrier)
            last_carrier = self.fftout[CENTER_AM]

            sum_y += y
            sum_xy += x * y
            sum_x2 += x * x

            if self.input.sync_state != 2:
                for j in range(CENTER_AM - PIDS_OUTER_INDEX_AM,
                               CENTER_AM + PIDS_OUTER_INDEX_AM + 1):
                    mag_sums[j] += abs(self.fftout[j])

        if self.input.sync_state != 2:
            max_index = int(np.argmax(mag_sums[CENTER_AM - PIDS_OUTER_INDEX_AM:
                                               CENTER_AM + PIDS_OUTER_INDEX_AM + 1]))
            self.cfo_adjust(max_index - CENTER_AM)

        phase_increment *= np.exp(complex(0, -sum_xy / sum_x2))
        # TODO: investigate why 0.06 is needed below (from the C source).
        self.phase *= np.exp(complex(
            0, -sum_y / ACQUIRE_SYMBOLS
            + (sum_xy / sum_x2) * ACQUIRE_SYMBOLS * self.fftcp / 2 - 0.06))