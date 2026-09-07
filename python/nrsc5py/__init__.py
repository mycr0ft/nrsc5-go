"""nrsc5py — an instructional Python port of the NRSC-5 (HD Radio) receiver.

This package mirrors the Go implementation (the parent repository)
function-for-function, but written for readability. Where Go optimizes
for speed, this package optimizes for teaching: every routine is
documented with its spec reference (NRSC-5-DOC-1011s / 1012s), and the
heavy inner loops are JIT-compiled with numba so the whole receiver runs
about 5x faster than realtime.

Layout (mirrors the Go files):

    defines.py     constants and helpers
    input.py       IQ conversion and halfband decimation
    acquire.py     matched filter, CFO estimation, OFDM FFT
    sync.py        symbol sync, Costas loops, soft demodulation
    decode.py      deinterleavers and FEC driver
    conv.py        Viterbi decoder
    rs.py          Reed-Solomon decoder
    frame.py       L2 framing, PCI, HDLC
    pids.py        SIS/PIDS station information
    output.py      audio buffers, ID3, data services
    rtltcp.py      rtl_tcp client
    radio.py       the receiver object and event pump
    cli.py         command line interface

The DSP math is identical to the Go version — see docs/architecture.md
in the repository root for the full signal-chain walkthrough. Where a
Python implementation detail differs (usually vectorization strategy),
the code comments explain why.
"""

__version__ = "0.1.0"
