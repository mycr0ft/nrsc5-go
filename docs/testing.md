# Testing strategy

`go test ./...` runs three tiers of tests. Understanding which tier a
test belongs to explains why some tests skip on one machine and run on
another.

## Tier 1: self-consistent round-trip (always runs)

Software encoders built into the test files produce valid coded streams;
the decoder under test must recover the original data exactly. These
validate the decoder against its own specification with no external
dependencies:

- `TestConvRoundTrip` — all five convolutional codes (P1/PIDS/P3 K=7,
  AM E1/E2-E3 K=9) via a software tail-biting encoder
- `TestRsRoundTrip` — shortened RS(255,247) with 1–4 symbol errors
- `TestRsGaloisField` — exhaustive GF(256) table check

## Tier 2: byte-exact comparison against the original C (needs reference binaries)

The original C sources (theori-io/nrsc5) are compiled into tiny harness
binaries, and both implementations receive identical inputs. The tests
skip when the harnesses are absent:

| Test | Harness | Compares |
|---|---|---|
| `TestConvAgainstC` | `ref_conv` | decoded bits, 5 codes, noisy inputs |
| `TestRsAgainstC` | `ref_rs` | encoder parity + decoder corrections |
| `TestFirAgainstC` | `ref_fir` | FM matched filter, AM halfband |
| `TestInterleaverAgainstC` | `ref_intl` | PM interleaver types I & II |
| `TestP1ChainAgainstC` | `ref_p1` | interleaver I + Viterbi + descramble |
| `TestP3ChainAgainstC` | `ref_p3` | streaming interleaver + conv + descramble |
| `TestAMPIDSInterleaverAgainstC` | `ref_am` | AM PIDS bit mapping |
| `TestAMMA1InterleaverAgainstC` | `ref_am` | AM MA1 interleaver |

### Building the harnesses

They compile against a checkout of theori-io/nrsc5 (plus one file from
Karn's classic RS distribution for the encoder). The harnesses live in
a scratch directory — the repo records what each does rather than
shipping them. To rebuild:

```sh
git clone --depth 1 https://github.com/theori-io/nrsc5.git /tmp/nrsc5
mkdir /tmp/refbuild && cd /tmp/refbuild
cat > config.h << 'EOF'
#pragma once
#define GIT_COMMIT_HASH "test"
#define LIBRARY_DEBUG_LEVEL 5
#define HAVE_CMPLXF 1
#define HAVE_IMAGINARY_I 1
#define HAVE_COMPLEX_I 1
#define HAVE_STRNDUP 1
EOF
# (then compile the harness .c files listed in each test's comments
#  against /tmp/nrsc5/src — see docs/verification.md for the details)
```

The tests locate harnesses at hard-coded paths under /tmp and skip if
absent — deliberate, so `go test ./...` is always green on machines
without the C sources.

## Tier 3: end-to-end (manual, needs data)

```sh
# offline: the nrsc5 sample capture (48 MB)
xz -dc sample.xz | ./nrsc5 -dump-hdc out.aac 0

# live: with rtl_tcp running
./nrsc5 play -H 127.0.0.1:1234 -f 89.3 -g 30 0
```

Expected for sample.xz: `Synchronized`, station name KUT, `Audio bit
rate: 63–64 kbps`, ~240 clean ADTS frames in out.aac. docs/on-air.md
covers live expectations.

## Benchmarks

```
go test -bench . -benchtime 200x .
```

- `BenchmarkFFT2048` — the OFDM FFT (~86 µs after optimization; runs
  ~350×/s at real time)
- `BenchmarkFirFM` — matched filter per sample
- `BenchmarkConvDecodePIDS` — the small Viterbi frame