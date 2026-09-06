# Verification: proving the Go port matches the C original

A port is only trustworthy if you can show the new code behaves the same
as the old. This document records how each component was verified, and
the interesting bugs the process uncovered.

## Method

For every DSP component, we built a small **reference harness** from the
original C sources (compiled standalone with gcc — no cmake needed for
the individual files), then compared Go output byte-for-byte against C
output on identical inputs:

1. **Random-input equivalence**: feed both implementations the same
   pseudo-random input (deterministic seed), diff the outputs.
2. **Round-trip tests**: encode with a software encoder matching the
   decoder's conventions, decode, and check the original data comes
   back. These run everywhere (no C toolchain needed) and validate the
   decoder self-consistently.
3. **End-to-end**: run the whole receiver over nrsc5's `sample.xz`
   capture and compare against known-good behavior (sync, SIS content,
   audio bit rate, decodable AAC output).

The test files in the repo keep this process alive: `go test ./...`
re-runs everything, and the C-comparison tests skip gracefully when the
reference binaries aren't available.

## Component-by-component results

### Viterbi decoder (`conv.go`)

- **Round-trip**: software encoders for all five code variants
  (K=7 rate 1/3 for P1/PIDS/P3; K=9 for E1 and E2/E3) produce clean
  soft-bit streams the Go decoder decodes with zero errors.
- **C equivalence**: identical decoded bits on random inputs with ~7%
  flipped soft bits (noise), for every code: P1 (146176 bits), PIDS
  (80), P3 (2304), AM E1 (3750), AM E2/E3.
- Reference binary: `ref_conv` harness around `conv_dec.c`.

### Reed-Solomon (`rs.go`)

- GF(256) log/antilog tables validated exhaustively (alpha^i * alpha^j
  == alpha^(i+j) for all i,j).
- Encoder parity vs Karn's `encode_rs_char`: byte-identical.
- Decoder vs `decode_rs_char`: identical correction counts and output
  blocks on codewords with 1–4 symbol errors, across 5 random trials,
  including the shortened-code zero-padding convention.
- Reference binary: `ref_rs` harness around `rs_init.c`/`rs_decode.c`.

### FIR filters (`acquire.go`)

- FM 32-tap matched filter: float-exact within 1e-4 against
  `fir_cf32_execute`.
- AM halfband decimator: same, against `halfband_cf32_execute`
  (including the quirks — reversed tap storage, window wraparound at
  2048 samples, the hardcoded center-tap gain).
- Reference binary: `ref_fir`.

### Deinterleavers (`decode.go`)

- Types I and II: byte-identical over the full PM buffer for all 16
  block counters.
- Type IV (streaming, PX1/PX2): byte-identical viterbi input after the
  interleaver becomes "ready" (73728 soft bits pushed).
- AM MA1 (`interleaverMA1`): byte-identical `viterbiP1AM` and
  `viterbiP3AM` outputs including the depuncture placement.
- AM PIDS mapping: byte-identical.
- Reference binaries: `ref_intl`, `ref_p3`, `ref_am`.

### Full chains

- **P1 chain** (interleaver I → Viterbi → descramble): byte-identical
  with the C (`ref_p1`).
- **P3 chain** (streaming interleaver → conv decode → descramble):
  byte-identical (`ref_p3`).
- **AM PIDS chain**: byte-identical (`ref_am`).
- **HDC audio**: FAAD2-HDC decodes the Go receiver's HDC output into
  the same PCM as expected (237/238 frames from the sample capture).

### End-to-end

- sample.xz (KUT, FM): sync, SIS (country/facility ID, slogan, name),
  ID3 metadata, 63–64 kbps audio rate, 238 clean ADTS frames in the
  dump — matches what the C tool reports for this capture.
- Live OTA (WLRH 89.3 HD1, Huntsville AL): stable sync, all three audio
  programs decode, minutes of continuous audio at 44.1 kHz.

## Bugs the verification process found

The byte-exact comparisons earned their keep. Each of these would have
produced a receiver that "mostly works" or fails in confusing ways:

1. **Descrambler LFSR width** (`decode.go`): the C descrambler uses an
   11-bit register (`val |= bit << 11`) — the first port shifted into
   bit 10, producing almost-correct-looking but wrong descrambled data.
   This single bit was why P1 frames failed the PCI check and the
   receiver dropped sync every block. Found by comparing the PCI header
   bits against expected constants.

2. **RS encoder feedback edge case** (test harness): Karn's encoder
   converts `feedback` to its index form before testing `feedback != 0`,
   so a symbol value of 1 (index 0) silently takes the wrong branch.
   Our first reference encoder had the same bug and disagreed with its
   own decoder — a useful reminder that the reference must be validated
   against itself before comparing to the port.

3. **e2e3 input length** (test harness): the harness read `len*12/5`
   bytes for the AM e2/e3 soft-bit stream instead of `len*3`, leaving
   48 bytes of garbage at the end. The C decoder appeared to "fail"
   clean codewords — after the fix both implementations agreed exactly.

4. **Interleaver IV call count** (test harness): the C harness ran the
   interleaver 17 times instead of 32 (16 pairs × 2 halves), comparing
   states at different stream positions. The receiver's actual call
   pattern (two half-pushes per pair) must be replicated exactly for
   the streaming interleaver.

5. **AAS slice length** (`frame.go`/`output.go`): the Go port passed
   `length-4` as the data length but a slice with `length-8` bytes of
   payload. Sample captures never triggered it; live WLRH ID3 tags did
   (index out of range inside the ID3 parser). Classic C-to-Go
   pointer-vs-slice translation bug.

6. **E4000 gain steps**: not a code bug, but live testing revealed the
   E4000's coarse LNA steps (0/3/6/9/12 dB then ~9 dB jumps) mean the
   gain sweet spot is narrow — a quick per-gain power sweep (see
   docs/on-air.md) finds it much faster than guessing.

## The C reference harnesses

All harnesses live outside the repo (they compile against a checkout of
theori-io/nrsc5) but the test files document exactly what they do:

- `ref_conv` — raw soft-bits → decoded bits, for each of the 5 codes
- `ref_rs` — `enc` (parity bytes) and `fix` (decode in place) modes
- `ref_fir` — FM matched filter and AM halfband
- `ref_intl` — interleaver types I and II
- `ref_p1` — interleaver I + conv decode + descramble
- `ref_p3` — streaming type-IV interleaver + conv decode + descramble
- `ref_am` — AM MA1 interleaver and PIDS bit mapping

Each test file names the harness it uses and skips when the binary is
absent, so `go test ./...` is safe on any machine.