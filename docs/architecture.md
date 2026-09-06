# Architecture: how nrsc5-go decodes HD Radio

This document walks through the receive chain the way the signal
experiences it. The NRSC-5 specification documents referenced throughout
are NRSC-5-DOC-1011s ("FM IBOC Transmission Specification") and
NRSC-5-DOC-1012s ("Audio and Data Transport"), available from the NRSC.

## What the transmitter sends

An FM HD Radio station transmits a hybrid signal: the classic analog FM
carrier in the center, plus **OFDM digital subcarriers** squeezed into
the ±130–280 kHz region around it (the "IBOC" — In-Band On-Channel —
sidebands). Each digital subcarrier carries QPSK or QAM symbols at a
symbol rate of 3679.2 symbols/s. The digital payload is layered:

```
audio (AAC/HDC frames)
   └── L3 packets (programs, files, station data)
        └── L2 frames (PDUs with CRC, Reed-Solomon protected headers)
             └── L1 logical channels (P1 main, P3/P4 supplemental, PIDS)
                  ├── convolutional coding (rate 1/3, punctured to 2/5)
                  ├── bit interleaving (types I, II, IV)
                  ├── OFDM modulation (2048-point, 112-sample cyclic prefix)
```

P1 carries the main audio program (~64 kbps), PIDS carries Station
Information Service, and P3/P4 carry optional supplemental programs and
data. The receiver's job is to run that stack in reverse.

## The signal chain

### 1. Input — `input.go`

IQ samples arrive as unsigned 8-bit (cu8, from rtl_tcp), signed 16-bit,
or float pairs. `input.decimateSamples` converts to complex float and
decimates:

- **FM**: one 2:1 halfband stage → 744188 sps (the OFDM sample rate)
- **AM**: five 2:1 halfband stages → 46512 sps (AM's OFDM rate)

The halfband filters use the 7-tap design from nrsc5 (`decimTaps`,
applied with the center-tap gain of 1.0 — see `executeHalfband`, which
mirrors the symmetric dot product of the C code). The FM path also has a
32-tap matched filter (`filterTapsFM`) applied inside acquire.

### 2. Acquisition — `acquire.go`

`acquire.process` runs once per 33 OFDM symbols (enough to slide a
2048-sample window across the 2048+112-sample symbol period):

- **Coarse timing**: correlates the symbol against itself offset by the
  FFT length (`sums[i] += buffer[i+j*fftcp] * conj(buffer[i+j*fftcp+fft])`)
  over the cyclic prefix, weighted by the pulse-shaping window
  (`shapeFM`/`shapeAM` — raised-cosine ramps over the CP region). The
  strongest correlation peak gives the sample error.
- **Carrier frequency offset (CFO)**: the phase of the correlation peak
  is the CFO estimate; `detect_cfo`-style search in sync sweeps candidate
  offsets by looking for the reference subcarriers' known sync patterns.
- **Phase tracking**: `phase` accumulates rotations at
  `angle/fft` per sample so each FFT sees a phase-coherent window.
- Per symbol: assemble FFT input by folding the cyclic prefix
  (`fftin[j] = shape[j] * sample` for j < CP, plus the wrap contribution
  for j ≥ fft), run the 2048-point (FM) or 256-point (AM) forward FFT
  (`fftPlan.forward` — radix-2 with precomputed twiddles), and FFT-shift
  so subcarrier indices are centered.

Each FFT output is pushed to sync.

### 3. Sync and demodulation — `sync.go`

`sync` keeps a 32-symbol × subcarrier matrix (`buffer[FFT_FM][BLKSZ]`)
and processes it once full (one L1 block):

- **Costas loops** (`adjustRef`) track residual phase on the reference
  subcarriers — the ones broadcasting a known 32-bit sync pattern plus
  parity. The loop gains come from a standard second-order loop
  (loop_bw=0.05, damping=0.707): `alpha`, `beta`.
- **Reference decode** (`decodeRefFM`): when enough reference
  subcarriers match the expected sync pattern, the block counter `bc`
  and service mode (psmi) are recovered by majority vote; the receiver
  declares FINE sync.
- **Partition calibration** (`adjustData`): each partition's 18 data
  carriers are phase- and gain-corrected using the amplitude of the
  flanking reference subcarriers.
- **MER** (modulation error ratio) is computed against ideal QPSK
  constellation points and reported every 16 blocks.
- **Soft demod**: each data subcarrier's I/Q is converted to soft bits
  (`demod` clamps to ±1 and scales by MER-derived multipliers),
  producing the rate-1/3 soft bits that decode.go consumes.

FM PIDS/P1/PM data flows out via `decode.pushPM` / `pushPX1` /
`pushPX2`. AM mode (`processAM`) instead uses QAM16/QAM64 demod with
per-partition multipliers derived from transmitted training symbols.

### 4. Deinterleaving — `decode.go`

The transmitter spreads each FEC frame over time and frequency. Four
interleaver types undo this (all from 1012s figures 10-4/10-5):

- **Type I** (`interleaverI`): P1 main channel. Reads
  `buffer_pm[block*32+row][partition*C + column]` with a block/row/
  column walk driven by the `PM_V` partition schedule.
- **Type II** (`interleaverII`): PIDS, one 16-block window per `bc`.
- **Type IV** (`interleaverIV`): streaming interleaver for P3/P4 —
  pushes soft bits into a 147456-slot history and drains them in a
  scrambled order spanning many frames.
- **MA1/MA3** (`interleaverMA1`): AM's block-column mapping with
  diversity delay (bl/ml/bu/mu streams).

All of them **depuncture** on the way out: the rate-2/5 puncture pattern
([1,1,1,1,1,0]) is filled with zeros so the Viterbi decoder always sees
full rate-1/3 soft bits.

### 5. Convolutional decoding — `conv.go`

`convDecode` is a textbook Add-Compare-Select Viterbi decoder:

- K=7 (FM, generators 0133/0171/0165 octal) and K=9 (AM, 0561/0657/0711
  and 0561/0753/0711), both rate 1/3, both **tail-biting** (the encoder's
  register is initialized with the last K−1 data bits, so the decoder
  runs the trellis an extra 32 steps on each end and picks the best
  starting state).
- Branch metrics are soft-bit correlation against the trellis outputs;
  path metrics normalize every `INT16_MAX/(3*127) − K` steps to avoid
  overflow.
- The returned value is the difference between the best and second-best
  final path metrics — a confidence measure, used for nothing critical
  in nrsc5 but reported as part of the BER estimate path.

`bitErrors` re-encodes the decoded bits and counts symbol disagreements
with the puncture-aware pattern to feed the BER report.

### 6. Scrambling and framing — `frame.go`

Decoded bits are XORed with the standard L2 scrambler (11-bit LFSR,
`descramble`), then `frame.push` scans for the 24-bit PCI (Physical
Channel Indicator) at fixed offsets. The PCI identifies the frame type
(audio PDU, fixed data, etc.) and is checked fuzzily (`fuzzyPCI`,
≤4 bit errors) because it rides on noisy data. Frame layouts differ per
logical channel; the offsets (`start`, `offset`, `pci_len`) are the
constants at the top of `frame.push`.

`frame.process` then:

- runs **Reed-Solomon** on the 96-byte header (RS(255,247) shortened —
  the first 159 symbols are forced zero, giving 8-symbol error
  correction on the header) via `fixHeader`
- parses the L2 header (`parseHeader`: codec, stream, sequence,
  blend control, delay, number of packets)
- reads the per-packet location table (`parseLocation`)
- pushes audio packets into the output's elastic buffer
- runs an **HDLC deframer** (`parseHDLC`) on the interleaved data
  portion for AAS (advanced application services): 0x7E-framed,
  byte-escaped, CRC-16 checked.

### 7. Reed-Solomon — `rs.go`

A faithful port of Phil Karn's `decode_rs_char`. GF(256) with primitive
polynomial 0x11d, first consecutive root fcr=1, primitive element 1,
8 parity symbols (t=4 corrections). Berlekamp-Massey finds the error
locator polynomial, Chien search finds its roots, Forney computes the
error magnitudes. Erasure decoding support is ported but unused by
nrsc5.

The shortened-code trick: nrsc5's 96-byte header sits at the END of a
virtual 255-symbol block whose first 159 symbols are zero — the decoder
just sees a full-length codeword.

### 8. Audio output — `output.go`

Audio packets land in a per-program **elastic buffer** (64 slots,
`ELASTIC_BUFFER_LEN`) indexed by a transmit-side sequence number. The
buffer absorbs network-style jitter: `output.align` positions the read
pointer by pdu_seq and latency, `output.push` fills slots (handling
PACKET_HALF_FRONT/HALF_BACK reassembly), and `output.advance` drains
two (FM) or four (AM) slots per symbol block, reporting HDC packets and
(substituting silence where packets are missing).

With `-tags aac`, decoded PCM (44.1 kHz stereo) is reported as
`EventTypeAudio`. Without it, the HDC packet is still reported and can
be dumped.

### 9. Data services — `output.go`, `pids.go`

- **ID3** (`output.id3`): title/artist/album/genre, UFID, XHDR (which
  links to a LOT file), COMR commercial tags, COMM comments.
- **SIG** (`output.parseSIG`): the Station Information Guide — the
  service/component directory that maps data ports to MIME types.
- **LOT** (`processPort`): file transfer (album art, traffic maps) —
  fragments reassembled with per-fragment headers, LRU eviction of
  stale files, HERE traffic/weather images parsed separately.
- **SIS/PIDS** (`pids.go`): station ID (country code + FCC facility
  ID), short/universal/long names, slogans, messages, coordinates,
  audio/data service descriptors, leap second and local time parameters,
  and emergency alerts (with SAME/FIPS/ZIP location encoding).

### 10. rtl_tcp — `rtltcp.go`

A minimal client for steve-m's rtl_tcp: the 12-byte dongle greeting
(magic "RTL0", tuner type, gain count), 5-byte commands (1-byte opcode,
4-byte big-endian parameter), and a raw cu8 sample stream. Gain tables
per tuner are reproduced from librtlsdr so `doAutoGain`'s binary search
works the same way.

## Mode differences: FM vs AM

| Aspect | FM | AM |
|---|---|---|
| Sample rate | 744188 sps | 46512 sps |
| FFT size | 2048 | 256 |
| Cyclic prefix | 112 | 14 |
| Symbol sync | reference subcarrier patterns | 4-bit block sync field |
| Modulation | QPSK on PM partitions | QPSK/QAM16/QAM64 partitions |
| Interleavers | types I, II, IV | type MA1 (block columns, diversity delay) |
| Conv. code | K=7 (P1/PIDS/P3), K=9 (E1/E2/E3 modes) | K=9 |
| Audio frame | 2 frames/block | 4 frames/block |

## Design notes for C readers

- **No manual memory management**: the C version frees every buffer
  explicitly; here the GC handles it. The FFT keeps one reusable scratch
  buffer (`fftPlan.tmp`) because allocating per symbol is measurable.
- **Slices instead of pointer arithmetic**: `buffer[ref][n]` becomes
  `st.buffer[ref][n]` — the C two-dimensional arrays are fixed-size
  arrays of arrays, same as Go.
- **Events**: the C `nrsc5_event_t` is a union of structs selected by
  `event`; the Go `Event` is one struct with all fields (simpler, a bit
  larger). The callback contract is identical.
- **Threads**: the C worker thread with mutex/condvar maps to a
  goroutine; `startWorker` waits on a `sync.Cond`, reads samples with
  the state lock released, and pushes into the pipeline.
- **Fixed-point quirks preserved**: the C decoder's exact arithmetic
  (int16 path metrics with interval normalization, the 11-bit
  descrambler LFSR, Karn's index-form RS tables) is reproduced
  bit-for-bit — see docs/verification.md for how that was proven.