"""Reed-Solomon decoder (mirrors rs.go — a port of Phil Karn's
decode_rs_char, as used by nrsc5 for the L2 frame headers).

nrsc5 uses RS(255,247) shortened: 8 parity symbols over GF(256)
(poly 0x11d, fcr=1, prim=1). The 96-byte header is placed at the END
of a virtual 255-symbol block whose first 159 symbols are zero.

Syndromes, Berlekamp-Massey, Chien search and Forney's algorithm are
JIT-compiled; the Galois field tables are built at init.
"""

from __future__ import annotations

import numpy as np

try:
    from numba import njit
except ImportError:  # pragma: no cover
    def njit(*args, **kwargs):
        if len(args) == 1 and callable(args[0]):
            return args[0]
        def wrap(fn):
            return fn
        return wrap


class RsState:
    """Galois field tables + generator polynomial (struct rs)."""

    def __init__(self, symsize: int = 8, gfpoly: int = 0x11D,
                 fcr: int = 1, prim: int = 1, nroots: int = 8):
        self.mm = symsize
        self.nn = (1 << symsize) - 1
        self.nroots = nroots
        self.fcr = fcr
        self.prim = prim
        self.a0 = self.nn  # A0 sentinel: log(0) = -inf

        self.alpha_to = np.zeros(self.nn + 1, dtype=np.uint8)
        self.index_of = np.zeros(self.nn + 1, dtype=np.uint8)
        self.index_of[0] = self.a0
        self.alpha_to[self.a0] = 0
        sr = 1
        for i in range(self.nn):
            self.index_of[sr] = i
            self.alpha_to[i] = sr
            sr <<= 1
            if sr & (1 << symsize):
                sr ^= gfpoly
            sr &= self.nn
        if sr != 1:
            raise ValueError("field generator polynomial is not primitive")

        # prim-th root of 1 (iprim), used in the Chien search.
        iprim = 1
        while iprim % self.prim != 0:
            iprim += self.nn
        self.iprim = iprim // self.prim

        # Generator polynomial from its roots, in index form.
        self.genpoly = np.zeros(nroots + 1, dtype=np.uint8)
        self.genpoly[0] = 1
        root = fcr * prim
        for i in range(nroots):
            self.genpoly[i + 1] = 1
            for j in range(i, 0, -1):
                if self.genpoly[j] != 0:
                    self.genpoly[j] = self.genpoly[j-1] ^ self.alpha_to[
                        _modnn(self, int(self.index_of[self.genpoly[j]]) + root)]
                else:
                    self.genpoly[j] = self.genpoly[j-1]
            self.genpoly[0] = self.alpha_to[
                _modnn(self, int(self.index_of[self.genpoly[0]]) + root)]
            root += prim
        self.genpoly = np.array(
            [self.index_of[v] for v in self.genpoly], dtype=np.uint8)


def _modnn(rs: RsState, x: int) -> int:
    while x >= rs.nn:
        x -= rs.nn
        x = (x >> rs.mm) + (x & rs.nn)
    return x


def init_rs(symsize=8, gfpoly=0x11D, fcr=1, prim=1, nroots=8) -> RsState:
    return RsState(symsize, gfpoly, fcr, prim, nroots)


@njit(cache=True)
def _decode_rs_core(data, alpha_to, index_of, genpoly_unused,
                    nn, nroots, fcr, prim, iprim, a0, eras_pos, no_eras):
    """Karn's decode_rs_char, JIT-compiled. Returns corrections (-1 = fail).

    All polynomial arrays work in int32 (the C version's uint8 arithmetic
    relies on wrap-around in a few places; Karn's code keeps values < 256
    so int32 is safe and numba-friendly).
    """
    deg_lambda = 0
    el = 0
    deg_omega = 0
    i = 0
    j = 0
    r = 0
    k = 0
    lambda_ = np.zeros(nroots + 1, dtype=np.int32)
    s = np.zeros(nroots, dtype=np.int32)
    b = np.zeros(nroots + 1, dtype=np.int32)
    t = np.zeros(nroots + 1, dtype=np.int32)
    omega = np.zeros(nroots + 1, dtype=np.int32)
    root = np.zeros(nroots, dtype=np.int64)
    reg = np.zeros(nroots + 1, dtype=np.int32)
    loc = np.zeros(nroots, dtype=np.int64)

    # Form the syndromes: evaluate data(x) at the roots of g(x).
    for i in range(nroots):
        s[i] = data[0]
    for j in range(1, nn):
        for i in range(nroots):
            if s[i] == 0:
                s[i] = data[j]
            else:
                x = index_of[s[i]] + (fcr + i) * prim
                while x >= nn:
                    x -= nn
                    x = (x >> 8) + (x & nn)
                s[i] = data[j] ^ alpha_to[x]

    # Convert syndromes to index form, checking for nonzero condition.
    syn_error = 0
    for i in range(nroots):
        syn_error |= s[i]
        s[i] = index_of[s[i]]

    if syn_error == 0:
        return 0

    lambda_[0] = 1
    for i in range(1, nroots + 1):
        lambda_[i] = 0

    if no_eras > 0:
        # Init lambda to the erasure locator polynomial.
        x = prim * (nn - 1 - eras_pos[0])
        while x >= nn:
            x -= nn
            x = (x >> 8) + (x & nn)
        lambda_[1] = alpha_to[x]
        for i in range(1, no_eras):
            x = prim * (nn - 1 - eras_pos[i])
            while x >= nn:
                x -= nn
                x = (x >> 8) + (x & nn)
            u = x
            for j in range(i + 1, 0, -1):
                tmp = index_of[lambda_[j - 1]]
                if tmp != a0:
                    x2 = u + tmp
                    while x2 >= nn:
                        x2 -= nn
                        x2 = (x2 >> 8) + (x2 & nn)
                    lambda_[j] ^= alpha_to[x2]

    for i in range(nroots + 1):
        b[i] = index_of[lambda_[i]]

    # Berlekamp-Massey.
    r = no_eras
    el = no_eras
    while True:
        r += 1
        if r > nroots:
            break
        discr_r = 0
        for i in range(r):
            if lambda_[i] != 0 and s[r - i - 1] != a0:
                x = index_of[lambda_[i]] + s[r - i - 1]
                while x >= nn:
                    x -= nn
                    x = (x >> 8) + (x & nn)
                discr_r ^= alpha_to[x]
        discr_r = index_of[discr_r]
        if discr_r == a0:
            for j in range(nroots, 0, -1):
                b[j] = b[j - 1]
            b[0] = a0
        else:
            t[0] = lambda_[0]
            for i in range(nroots):
                if b[i] != a0:
                    x = discr_r + b[i]
                    while x >= nn:
                        x -= nn
                        x = (x >> 8) + (x & nn)
                    t[i + 1] = lambda_[i + 1] ^ alpha_to[x]
                else:
                    t[i + 1] = lambda_[i + 1]
            if 2 * el <= r + no_eras - 1:
                el = r + no_eras - el
                for i in range(nroots + 1):
                    if lambda_[i] == 0:
                        b[i] = a0
                    else:
                        x = index_of[lambda_[i]] - discr_r + nn
                        while x >= nn:
                            x -= nn
                            x = (x >> 8) + (x & nn)
                        b[i] = x
            else:
                for j in range(nroots, 0, -1):
                    b[j] = b[j - 1]
                b[0] = a0
            for i in range(nroots + 1):
                lambda_[i] = t[i]

    # Convert lambda to index form and compute deg(lambda).
    deg_lambda = 0
    for i in range(nroots + 1):
        lambda_[i] = index_of[lambda_[i]]
        if lambda_[i] != a0:
            deg_lambda = i

    # Chien search for roots of the locator polynomial.
    for i in range(1, nroots + 1):
        reg[i] = lambda_[i]
    count = 0
    k = iprim - 1
    i = 1
    while i <= nn:
        q = 1
        for j in range(deg_lambda, 0, -1):
            if reg[j] != a0:
                x = reg[j] + j
                while x >= nn:
                    x -= nn
                    x = (x >> 8) + (x & nn)
                reg[j] = x
                q ^= alpha_to[reg[j]]
        if q == 0:
            root[count] = i
            loc[count] = k
            count += 1
            if count == deg_lambda:
                break
        x = k + iprim
        while x >= nn:
            x -= nn
            x = (x >> 8) + (x & nn)
        k = x
        i += 1
    if deg_lambda != count:
        return -1

    # Omega = s(x)*lambda(x) mod x**nroots.
    deg_omega = 0
    for i in range(nroots):
        tmp = 0
        j = deg_lambda if deg_lambda < i else i
        while j >= 0:
            if s[i - j] != a0 and lambda_[j] != a0:
                x = s[i - j] + lambda_[j]
                while x >= nn:
                    x -= nn
                    x = (x >> 8) + (x & nn)
                tmp ^= alpha_to[x]
            j -= 1
        if tmp != 0:
            deg_omega = i
        omega[i] = index_of[tmp]
    omega[nroots] = a0

    # Forney's algorithm for the error values.
    for j in range(count - 1, -1, -1):
        num1 = 0
        for i in range(deg_omega, -1, -1):
            if omega[i] != a0:
                x = omega[i] + i * root[j]
                while x >= nn:
                    x -= nn
                    x = (x >> 8) + (x & nn)
                num1 ^= alpha_to[x]
        x = root[j] * (fcr - 1) + nn
        while x >= nn:
            x -= nn
            x = (x >> 8) + (x & nn)
        num2 = alpha_to[x]
        den = 0
        lim = deg_lambda if deg_lambda < (nroots - 1) else (nroots - 1)
        i = lim & ~1
        while i >= 0:
            if lambda_[i + 1] != a0:
                x = lambda_[i + 1] + i * root[j]
                while x >= nn:
                    x -= nn
                    x = (x >> 8) + (x & nn)
                den ^= alpha_to[x]
            i -= 2
        if den == 0:
            return -1
        if num1 != 0:
            x = index_of[num1] + index_of[num2] + nn - index_of[den]
            while x >= nn:
                x -= nn
                x = (x >> 8) + (x & nn)
            data[loc[j]] ^= alpha_to[x]

    return count


def decode_rs(rs: RsState, data: np.ndarray, eras_pos=None, no_eras: int = 0) -> int:
    """Decode a codeword in place; returns corrections or -1."""
    return _decode_rs_core(
        data, rs.alpha_to, rs.index_of, rs.genpoly,
        rs.nn, rs.nroots, rs.fcr, rs.prim, rs.iprim, rs.a0,
        eras_pos if eras_pos is not None else np.zeros(0, dtype=np.int64),
        no_eras)
