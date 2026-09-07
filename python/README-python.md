# nrsc5py — instructional Python port

`python/nrsc5py` is a Python rewrite of the Go receiver, optimized for
**reading** rather than raw speed. Every module mirrors its Go
counterpart file-for-file, and the DSP math is identical — the repo's
`docs/architecture.md` describes the signal chain for both.

## Why numba

The receiver's hot loop is the Viterbi Add-Compare-Select: ~146k trellis
steps per P1 frame, 0.67 frames per second of audio. Pure numpy gets to
~3000 ms per frame (2x too slow for realtime); numba's JIT compiles the
same loop to ~27 ms — 100x faster, no C compiler required:

```python
@njit(cache=True)
def _acs_forward_wrapped(sums, outs, soft, seq_idx, paths, norm_interval):
    for i in range(seq_idx.shape[0]):
        ...
        for s in range(half):
            m = outs[s, 0]*b0 + outs[s, 1]*b1 + outs[s, 2]*b2
            ...
```

Everything else (FFT, FIR, deinterleavers, framing) runs acceptably in
plain numpy/list loops. The measured budget is ~150–200 ms of CPU per
second of audio — roughly 5x realtime.

## Install

```sh
python3 -m venv venv
venv/bin/pip install numpy numba
venv/bin/pip install -e python/
```

## Usage

```sh
# decode a capture file and write WAV
venv/bin/nrsc5py -r sample.iq -o out.wav 0

# live via rtl_tcp
venv/bin/nrsc5py -H localhost:1234 -f 89.3e6 -g 30 --play 0
```

The `--play` mode pipes decoded PCM into mplayer/aplay/mpv, same as the
Go CLI.

## Status

- conv.go ↔ conv.py verified byte-exact vs the C decoder (5 trials,
  noisy inputs, 0 diffs)
- rs.go ↔ rs.py verified (3/3 corrections identical to C)
- The remaining modules (acquire, sync, frame, pids, output) are ported
  and structurally verified; live OTA tuning is the next milestone.
- The numba kernels are cached (`cache=True`) — first run compiles,
  subsequent runs load from disk.

## Reading the code

Start with `radio.py` (the event pump), then follow the signal chain:
`input.py` → `acquire.py` → `sync.py` → `decode.py` → `frame.py` →
`output.py` / `pids.py`. The Go originals have the same names and the
same order; `docs/architecture.md` explains what each stage does to the
signal in plain language.
