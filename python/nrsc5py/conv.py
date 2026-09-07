"""Viterbi decoder for the NRSC-5 convolutional codes (mirrors conv.go).

The NRSC-5 L1 channels use rate-1/3 convolutional codes with
tail-biting termination:

    P1 / PIDS / P3 (FM)   K=7, generators 0133, 0171, 0165 (octal)
    AM E1                 K=9, generators 0561, 0657, 0711
    AM E2/E3              K=9, generators 0561, 0753, 0711

Tail-biting means the encoder's shift register is initialized with the
last K-1 data bits, so the trellis starts and ends in the same state.
The decoder runs the trellis an extra 32 steps on each end (a "wrap"
period) and picks the best final state, then traces back.

The heavy loop (Add-Compare-Select over all states for every input bit)
is JIT-compiled with numba. Everything else stays readable Python.
"""

from __future__ import annotations

import numpy as np

try:
    from numba import njit
except ImportError:  # pragma: no cover - fall back to slow path
    def njit(*args, **kwargs):
        if len(args) == 1 and callable(args[0]):
            return args[0]
        def wrap(fn):
            return fn
        return wrap

from .defines import P1_FRAME_LEN_FM, PIDS_FRAME_LEN

# Termination types.
CONV_TERM_FLUSH = 0
CONV_TERM_TAIL_BITING = 1

# Extra trellis steps at each end for tail-biting codes.
TAIL_BITING_EXTRA = 32

# Generator polynomials in octal, matching the C code's LTE-style
# descriptors. gen[i] taps the shift register to produce output i.
CONV_CODE_K7 = (7, (0o133, 0o171, 0o165))
CONV_CODE_E1 = (9, (0o561, 0o657, 0o711))
CONV_CODE_E2_E3 = (9, (0o561, 0o753, 0o711))


def _num_states(k: int) -> int:
    return 256 if k == 9 else (64 if k == 7 else 16)


def vstate_lshift(reg: int, k: int, val: int) -> int:
    """Left-shift a K-1 bit register and insert val as the LSB.

    The C decoder stores the newest input at bit 0. The mask keeps only
    the K-1 significant bits.
    """
    mask = {5: 0x0E, 7: 0x3E, 9: 0xFE}.get(k, 0)
    return ((reg << 1) & mask) | val


def _parity(x: int) -> int:
    """Population count mod 2."""
    x ^= x >> 32
    x ^= x >> 16
    x ^= x >> 8
    x ^= x >> 4
    x ^= x >> 2
    x ^= x >> 1
    return x & 1


def gen_state_info(code, reg: int):
    """Populate non-recursive trellis state info.

    For a state (the K-1 bit register), returns:
      val  — the input bit that drove the transition to this state
      out  — the 3 NRZ output symbols (+1/-1) of that transition

    In the C code this is gen_state_info(); the "previous '0' state"
    is computed by shifting the register with a 0 input.
    """
    k, gens = code
    prev = vstate_lshift(reg, k, 0)
    val = (reg >> (k - 2)) & 0x01
    prev |= val << (k - 1)
    out = [(_parity(prev & gens[i]) * 2 - 1) for i in range(3)]
    return val, out


def _build_trellis(code):
    """Compute the output symbols and input value for each state.

    outs[s] holds the 3 NRZ outputs (+1/-1) of the transition into
    state s; vals[s] is the input bit that led to it.
    """
    ns = _num_states(code[0])
    outs = np.zeros((ns, 3), dtype=np.int64)
    vals = np.zeros(ns, dtype=np.uint8)
    for reg in range(ns):
        val, o = gen_state_info(code, reg)
        vals[reg] = val
        outs[reg, :] = o
    return vals, outs


def conv_decode(in_soft: np.ndarray, length: int, k: int, gens):
    """Viterbi decode a tail-biting convolutional code.

    in_soft: 3 * length NRZ soft bits (+1 = bit 1, -1 = bit 0).
    Returns (bits, confidence) where bits is a uint8 array of `length`
    decoded bits and confidence is max−second_max path metric.
    """
    ns = _num_states(k)
    nsteps = length + 2 * TAIL_BITING_EXTRA

    vals, outs = _build_trellis((k, gens))
    # The butterfly pairs destination states (s, s+half); by trellis
    # symmetry both transitions of a pair share the metric of state s
    # (see gen_state_info). So the metric vector is the first half of
    # the output table.
    outs_pair = outs[: ns // 2, :]

    sums = np.zeros(ns, dtype=np.int64)
    paths = np.zeros((nsteps, ns), dtype=np.int8)

    # Normalization interval, matching the C decoder: the path metrics
    # are int16 there, normalized every INT16_MAX/(n*INT8_MAX) - k steps.
    intrvl = 32767 // (3 * 127) - k

    # Tail-biting: the trellis starts TAIL_BITING_EXTRA input bits
    # before the frame start, wrapping to the beginning of the frame.
    seq_idx = (np.arange(length - TAIL_BITING_EXTRA, length)[:, None]
               + np.arange(nsteps)[None, :])
    seq_idx = seq_idx[0] % length

    # numba loop (indexed via precomputed seq_idx)
    _acs_forward_wrapped(sums, outs_pair, in_soft, seq_idx, paths, intrvl)

    # Traceback: find the best final state...
    best = int(np.argmax(sums))
    # Discard the extra tail-biting steps.
    for i in range(nsteps - 1, length + TAIL_BITING_EXTRA - 1, -1):
        path = int(paths[i, best]) + 1
        best = vstate_lshift(best, k, path)
    # Trace back through the data region.
    bits = np.zeros(length, dtype=np.uint8)
    for i in range(length - 1, -1, -1):
        path = int(paths[i + TAIL_BITING_EXTRA, best]) + 1
        bits[i] = vals[best]
        best = vstate_lshift(best, k, path)
    return bits


@njit(cache=True)
def _acs_forward_wrapped(sums, outs, soft, seq_idx, paths, norm_interval):
    ns = sums.shape[0]
    half = ns // 2
    new = np.empty(ns, dtype=np.int64)
    for i in range(seq_idx.shape[0]):
        base = seq_idx[i] * 3
        b0 = soft[base]
        b1 = soft[base + 1]
        b2 = soft[base + 2]
        # The butterfly must read the PREVIOUS step's metrics — write
        # destinations to a scratch array and swap after the sweep, or
        # later butterflies would consume their own outputs (destinations
        # s+half are sources of the pair s+16 in the next step... in
        # fact destinations overlap sources for ANY in-place update).
        for s in range(half):
            m = outs[s, 0] * b0 + outs[s, 1] * b1 + outs[s, 2] * b2
            state0 = sums[2 * s]
            state1 = sums[2 * s + 1]
            sum0 = state0 + m
            sum1 = state1 - m
            sum2 = state0 - m
            sum3 = state1 + m
            if sum0 > sum1:
                paths[i, s] = -1
                new[s] = sum0
            else:
                paths[i, s] = 0
                new[s] = sum1
            if sum2 > sum3:
                paths[i, s + half] = -1
                new[s + half] = sum2
            else:
                paths[i, s + half] = 0
                new[s + half] = sum3
        for j in range(ns):
            sums[j] = new[j]
        if i % norm_interval == 0:
            mn = sums[0]
            for j in range(1, ns):
                if sums[j] < mn:
                    mn = sums[j]
            for j in range(ns):
                sums[j] -= mn


# Public API mirroring the Go wrappers.
def conv_decode_p1(in_soft):
    return conv_decode(in_soft, P1_FRAME_LEN_FM, *CONV_CODE_K7)


def conv_decode_pids(in_soft):
    return conv_decode(in_soft, PIDS_FRAME_LEN, *CONV_CODE_K7)


def conv_decode_p3_p4(in_soft, length):
    return conv_decode(in_soft, length, *CONV_CODE_K7)


def conv_decode_e1(in_soft, length):
    return conv_decode(in_soft, length, *CONV_CODE_E1)


def conv_decode_e2_e3(in_soft, length):
    return conv_decode(in_soft, length, *CONV_CODE_E2_E3)