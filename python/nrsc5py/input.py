"""IQ input and decimation (mirrors input.go).

cu8 samples arrive from rtl_tcp at 1488375 sps. FM halves that rate
with one halfband filter (744188 sps = the OFDM sample rate); AM
decimates 32x via five halfband stages.
"""

from __future__ import annotations

import numpy as np

from .defines import (
    AM_DECIM_STAGES,
    FFTCP_AM,
    FFTCP_FM,
    ModeFM,
    u8_f,
)

# AM halfband taps from the C code (input.c decim_taps): symmetric with
# interleaved zeros; center tap weight 1.0 is implicit in the dot product.
DECIM_TAPS = np.array(
    [0.6062333583831787, 0, -0.13481467962265015, 0,
     0.032919470220804214, 0, -0.00410953676328063], dtype=np.float32)

# FM matched filter (RRC-ish, from acquire.c).
FILTER_TAPS_FM = np.array([
    -0.000685643230099231, 0.005636964458972216, 0.009015781804919243,
    -0.015486305579543114, -0.035108357667922974, 0.017446253448724747,
    0.08155813068151474, 0.007995186373591423, -0.13311293721199036,
    -0.0727422907948494, 0.15914097428321838, 0.16498781740665436,
    -0.1324498951435089, -0.2484012246131897, 0.051773931831121445,
    0.2821577787399292, 0.051773931831121445, -0.2484012246131897,
    -0.1324498951435089, 0.16498781740665436, 0.15914097428321838,
    -0.0727422907948494, -0.13311293721199036, 0.007995186373591423,
    0.08155813068151474, 0.017446253448724747, -0.035108357667922974,
    -0.015486305579543114, 0.009015781804919243, 0.005636964458972216,
    -0.000685643230099231, 0], dtype=np.float32)

FILTER_TAPS_AM = np.array([
    -0.00038464731187559664, -0.00021618751634377986,
    0.0026779419276863337, -0.00029802651260979474,
    -0.0012626448879018426, -0.0013182522961869836,
    -0.012252614833414555, 0.015980124473571777, 0.037112727761268616,
    -0.05451361835002899, -0.05804193392395973, 0.11320608854293823,
    0.055298302322626114, -0.16878043115139008, -0.022917453199625015,
    0.19178225100040436, -0.022917453199625015, -0.16878043115139008,
    0.055298302322626114, 0.11320608854293823, -0.05804193392395973,
    -0.05451361835002899, 0.037112727761268616, 0.015980124473571777,
    -0.012252614833414555, -0.0013182522961869836,
    -0.0012626448879018426, -0.00029802651260979474,
    0.0026779419276863337, -0.00021618751634377986,
    -0.00038464731187559664, 0], dtype=np.float32)

FIR_WINDOW = 2048


def _reversed_taps(taps: np.ndarray) -> np.ndarray:
    """firdecim_cf32_create: ntaps is 32 for the FM filter, else 15 with
    only the supplied taps reversed into the front."""
    n = 32 if len(taps) == 32 else 15
    out = np.zeros(n, dtype=np.float32)
    out[:len(taps)] = taps[::-1]
    return out


class FirFilter:
    """Sliding-window FIR matching firdecim_cf32.c.

    The C code pushes samples into a 2048-slot window and evaluates a
    symmetric dot product ending at the newest sample. We keep the same
    structure; execute_fir handles one sample per call (FM), and
    execute_halfband two (AM decimation).
    """

    def __init__(self, taps: np.ndarray):
        self.taps = _reversed_taps(taps)
        self.window = np.zeros(FIR_WINDOW, dtype=np.complex64)
        self.idx = len(self.taps) - 1

    def reset(self):
        self.idx = len(self.taps) - 1

    def _push(self, x: complex):
        if self.idx == FIR_WINDOW:
            keep = len(self.taps) - 1
            self.window[:keep] = self.window[self.idx - keep:]
            self.idx = keep
        self.window[self.idx] = x
        self.idx += 1

    def execute_fir(self, x: complex) -> complex:
        self._push(x)
        a = self.window[self.idx - len(self.taps):self.idx]
        b = self.taps
        return complex(np.sum((a[1:16] + a[17:32]) * b[1:16]) + a[16] * b[16])

    def execute_halfband(self, x0: complex, x1: complex) -> complex:
        self._push(x0)
        a = self.window[self.idx - len(self.taps):self.idx]
        b = self.taps
        sum_ = complex(np.sum((a[0:7:2] + a[14:7:-2]) * b[0:7:2]) + a[7])
        self._push(x1)
        return sum_


class Input:
    """The input chain: decimation + handoff to acquire."""

    def __init__(self, radio, output):
        self.radio = radio
        self.output = output
        self.decim = [FirFilter(DECIM_TAPS) for _ in range(AM_DECIM_STAGES)]
        self.stages = np.zeros((AM_DECIM_STAGES, 2), dtype=np.complex64)
        self.offset = 0
        self.sync_state = 0
        from .acquire import Acquire
        from .decode import Decode
        from .frame import Frame
        from .sync import Sync
        self.acq = Acquire(self)
        self.decode = Decode(self)
        self.frame = Frame(self)
        self.sync = Sync(self)
        self.reset()

    def reset(self):
        self.offset = 0
        self.resample_input_size = FFTCP_FM * 2 if self.radio.mode == ModeFM \
            else FFTCP_AM * 32
        self.set_sync_state(0)
        for d in self.decim:
            d.reset()
        self.acq.reset()
        self.decode.reset()
        self.frame.reset()
        self.sync.reset()

    def set_sync_state(self, new_state: int):
        if self.sync_state == new_state:
            return
        if self.sync_state == 2:  # SYNC_STATE_FINE
            self.radio.report_lost_sync()
        if new_state == 2:
            sample_rate = SAMPLE_RATE_FM if self.radio.mode == ModeFM \
                else SAMPLE_RATE_AM
            freq_offset = ((self.acq.prev_angle - 2 * np.pi * self.acq.cfo)
                           * sample_rate / (2 * np.pi * self.acq.fft))
            self.radio.report_sync(freq_offset, self.sync.psmi,
                                   self.sync.pli, self.sync.hppi,
                                   self.sync.aabi, self.sync.rdbi)
        self.sync_state = new_state

    def push(self, buf: np.ndarray):
        consumed = 0
        while consumed < len(buf):
            consumed += self.acq.push(buf[consumed:])
            self.acq.process()

    def decimate_samples(self, in_u8: np.ndarray) -> np.ndarray:
        """cu8 → complex, decimated. Returns an array of out samples."""
        out = []
        for i in range(0, len(in_u8), 4):
            x0 = u8_f(in_u8[i]) + 1j * u8_f(in_u8[i + 1])
            x1 = u8_f(in_u8[i + 2]) + 1j * u8_f(in_u8[i + 3])
            if self.radio.mode == ModeFM:
                out.append(self.decim[0].execute_halfband(x0, x1))
            else:
                self.stages[0][self.offset & 1] = \
                    self.decim[0].execute_halfband(x0, x1)
                if self.offset & 0x1 == 0x1:
                    self.stages[1][(self.offset >> 1) & 1] = \
                        self.decim[1].execute_halfband(*self.stages[0])
                if self.offset & 0x3 == 0x3:
                    self.stages[2][(self.offset >> 2) & 1] = \
                        self.decim[2].execute_halfband(*self.stages[1])
                if self.offset & 0x7 == 0x7:
                    self.stages[3][(self.offset >> 3) & 1] = \
                        self.decim[3].execute_halfband(*self.stages[2])
                if self.offset & 0xF == 0xF:
                    out.append(self.decim[4].execute_halfband(*self.stages[3]))
                self.offset += 1
        return np.array(out, dtype=np.complex64)

    def push_cu8(self, buf: np.ndarray):
        self.radio.report_iq(buf)
        out = self.decimate_samples(buf)
        self.push(out)


SAMPLE_RATE_FM = 744188
SAMPLE_RATE_AM = 46512
