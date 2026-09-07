"""Round-trip tests for the Viterbi decoder (tier 1 — always runs)."""
import numpy as np
import pytest

from nrsc5py.conv import conv_decode_pids, conv_decode_p1


def conv_encode(bits, k, gens):
    """Software tail-biting encoder matching the decoder's conventions
    (register holds u_i at bit k-1; outputs parity(reg & gen) in NRZ)."""
    n = len(bits)
    s = 0
    for j in range(1, k):
        s |= int(bits[n - j]) << (k - 1 - j)
    out = []
    for i in range(n):
        reg = s | (int(bits[i]) << (k - 1))
        for g in gens:
            parity = bin(reg & g).count("1") & 1
            out.append(2 * parity - 1)
        s = (s >> 1) | (int(bits[i]) << (k - 2))
    return np.array(out, dtype=np.int64)


@pytest.mark.parametrize("length,k,gens", [
    (80, 7, (0o133, 0o171, 0o165)),       # PIDS
    (2304, 7, (0o133, 0o171, 0o165)),     # P3
])
def test_round_trip(length, k, gens):
    from nrsc5py.conv import conv_decode_p3_p4
    rng = np.random.default_rng(1)
    bits = rng.integers(0, 2, length).astype(np.uint8)
    enc = conv_encode(bits, k, gens)
    if length == 80:
        dec = conv_decode_pids(enc)
    else:
        dec = conv_decode_p3_p4(enc, length)
    assert (dec == bits).all()


def test_round_trip_pids_multiple_seeds():
    for seed in range(3):
        rng = np.random.default_rng(seed)
        bits = rng.integers(0, 2, 80).astype(np.uint8)
        enc = conv_encode(bits, 7, (0o133, 0o171, 0o165))
        dec = conv_decode_pids(enc)
        assert (dec == bits).all(), f"seed {seed} failed"
