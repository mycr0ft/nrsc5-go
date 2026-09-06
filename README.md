# nrsc5-go

[![Go Reference](https://pkg.go.dev/badge/github.com/mycr0ft/nrsc5-go.svg)](https://pkg.go.dev/github.com/mycr0ft/nrsc5-go)

A Go implementation of an NRSC-5 (HD Radio) receiver — a port of
[theori-io/nrsc5](https://github.com/theori-io/nrsc5) from C, with the
same DSP pipeline, bit-exact component behavior, and a cleaner, garbage
collected codebase.

NRSC-5 is the digital radio standard broadcast alongside analog FM/AM in
the US and Canada ("HD Radio"). It carries one or more digital audio
programs, station information (SIS), song metadata (ID3), and data
services (traffic maps, album art, files) on subcarriers next to the
analog signal. An ~$30 RTL-SDR dongle is all the hardware you need.

```
antenna ──► RTL-SDR ──► rtl_tcp ──► nrsc5-go ──► audio + station info + data
            (dongle)     (TCP)       (this repo)
```

## Status

- **FM reception works end-to-end**, verified two ways:
  - offline, against nrsc5's 48 MB sample capture (station KUT):
    full sync, SIS, ID3, ~64 kbps audio, 2245+ HDC frames decoded
  - **live over the air** (WLRH 89.3 HD1, Huntsville AL): stable sync,
    SIS + ID3 + 3 audio programs decoded, 3 minutes of continuous audio
- DSP components verified **byte-exact** against the original C code
  (see [Testing](#testing) and [docs/testing.md](docs/testing.md))
- AM mode: interleavers verified against C; full-chain AM capture pending
- HDC/AAC audio decoding via an optional cgo build (see below); without
  it, the CLI dumps the audio stream as an ADTS `.aac` file any player
  can handle

## Quick start

### Building

```
go build -o nrsc5 ./cmd/nrsc5
```

No external dependencies — this is a pure-Go build. It receives IQ
samples over stdin, from a file, or from `rtl_tcp`, and can dump the
audio stream (`-dump-hdc`) but doesn't decode audio in-process.

### With live audio decoding (optional cgo)

HD Radio audio uses HDC — a proprietary variant of AAC-LC. Decoding it
requires [FAAD2](https://github.com/knik0/faad2) 2.11.2 built with the
nrsc5 HDC patch. Build it once as a static library and link it in:

```
git clone --depth 1 --branch 2.11.2 https://github.com/knik0/faad2.git
cd faad2 && git apply /path/to/nrsc5-go/support/faad2-hdc-support.patch
gcc -O2 -DHDC_SUPPORT -DHAVE_INTTYPES_H -DPACKAGE_VERSION='"2.11.2"' \
    -Iinclude -Ilibfaad -c libfaad/*.c
ar rcs libfaad_hdc.a *.o

cd ../nrsc5-go
CGO_LDFLAGS="-L<dir-with-lib> -l:libfaad_hdc.a -lm" \
CGO_CPPFLAGS="-I<faad2>/include" \
    go build -tags aac -o nrsc5 ./cmd/nrsc5
```

The resulting binary has **no runtime library dependency** (`ldd` shows
nothing faad-related) — the decoder is baked in. Alternatively build a
shared library and use `LD_LIBRARY_PATH`. Without `-tags aac`, everything
still works; audio packets are reported as `EventTypeHDC` events and can
be dumped to ADTS for external decoding.

A copy of the HDC patch is included in `support/` for convenience
(original: nrsc5 `support/faad2-hdc-support.patch`, GPL).

### Usage

Start `rtl_tcp` (from the `rtl-sdr` package, or built from
[osmocom rtl-sdr sources](https://gitea.osmocom.org/sdr/rtl-sdr)), then:

```sh
# listen to WLRH 89.3 HD1 live (PipeWire/PulseAudio)
nrsc5 play -H 127.0.0.1:1234 -f 89.3 -g 30 0

# or record HD2 to a WAV file
nrsc5 -H 127.0.0.1:1234 -f 89.3 -g 30 -o hd2.wav 1

# decode a capture file (cu8 IQ at 1488375 sps)
nrsc5 -r sample.iq -dump-hdc program.aac 0

# pipe into any player
nrsc5 -H localhost:1234 -f 89.3 -g 30 -o - -t raw 0 |
    mplayer -ao pulse -format s16le -channels 2 -srate 44100 -demuxer rawaudio -
```

`play` auto-selects the first of `mplayer | mpv | ffplay | aplay | paplay`,
or force one with `-p mpv`.

Full option list (mirrors the C tool):

| Option | Description |
|---|---|
| `-H host:port` | receive from an rtl_tcp server |
| `-f FREQ` | center frequency in MHz or Hz (with `-H`) |
| `-g GAIN` | tuner gain in dB (with `-H`; auto-gain if omitted) |
| `-p PPM` | frequency correction in ppm (with `-H`) |
| `-T` | bias-T power (with `-H`) |
| `-D MODE` | direct sampling 1=I-ADC 2=Q-ADC (with `-H`) |
| `-r FILE` | read cu8 IQ from file |
| `-o FILE` | write audio (`-o -` with `-t raw` = raw PCM to stdout) |
| `-t raw\|wav` | audio output format |
| `-w FILE` | dump raw IQ |
| `-dump-hdc FILE` | dump HDC audio packets (ADTS) |
| `-dump-aas-files DIR` | save received data files (LOT, HERE images) |
| `-am` | AM mode |

## Library API

The package exposes the same event-driven API as libnrsc5:

```go
radio, err := nrsc5.NewRadioRTLTCP("127.0.0.1:1234")  // device input
// or
radio := nrsc5.NewRadio()                              // pipe IQ yourself

radio.SetCallback(func(e *nrsc5.Event) {
    switch e.Type {
    case nrsc5.EventTypeAudio:        // e.Audio ([]int16, 44.1 kHz stereo)
    case nrsc5.EventTypeHDC:          // e.HDC (raw AAC frame)
    case nrsc5.EventTypeStationName:  // e.Message
    case nrsc5.EventTypeSIS:          // e.SIS (country, slogan, alerts...)
    case nrsc5.EventTypeID3:          // e.ID3 (title, artist, album, XHDR)
    case nrsc5.EventTypeLOT:          // e.Name, e.Data, e.MIME (files)
    case nrsc5.EventTypeSIG:          // e.Services (service guide)
    }
})
radio.Start()
// feed IQ: radio.PipeSamplesCU8 / PipeSamplesCS16 / PipeSamplesCF32
```

## Project layout

| File | Purpose |
|---|---|
| `defines.go` | NRSC-5 constants, Q15 fixed-point helpers |
| `radio.go` | `Radio` — top-level receiver, worker, gain control |
| `input.go` | cu8/cs16/cf32 conversion, halfband decimation chain |
| `acquire.go` | matched filter, CFO estimation, OFDM FFT, cyclic prefix |
| `sync.go` | symbol sync, Costas loops, MER, QPSK/QAM soft demod |
| `decode.go` | deinterleavers (types I/II/IV/MA1), depuncture, FEC driver |
| `conv.go` | Viterbi decoder (K=7/K=9, tail-biting) |
| `rs.go` | Reed-Solomon decoder (GF(256), Karn-style) |
| `frame.go` | L2 framing: PCI, RS headers, HDLC, audio PDUs |
| `pids.go` | SIS/PIDS: station ID, messages, alerts, service info |
| `output.go` | audio elastic buffer, ID3, SIG, LOT files, HERE images |
| `rtltcp.go` | rtl_tcp protocol client |
| `radio.go` | receiver wiring, worker, auto-gain |
| `unicode.go` | ISO-8859-1 / UCS-2 → UTF-8 |
| `cmd/nrsc5` | CLI |

Deep-dive documentation:

- [docs/architecture.md](docs/architecture.md) — how the receiver works,
  signal chain by signal chain, with the spec references
  (NRSC-5 docs 1011s/1012s) the algorithms come from
- [docs/verification.md](docs/verification.md) — how each component was
  proven equivalent to the C original, and the bugs found along the way
- [docs/on-air.md](docs/on-air.md) — hardware notes: rtl_tcp setup, gain
  tuning for your dongle, live results

## Testing

```
go test ./...
```

Component tests compare the Go DSP against reference binaries built from
the original C sources (Viterbi, Reed-Solomon, FIR filters,
deinterleavers, full P1/P3 chains). When the reference binaries aren't
present (e.g. on CI), those tests skip themselves; the round-trip tests
(encoder→decoder) always run. See [docs/testing.md](docs/testing.md).

An end-to-end smoke test needs nrsc5's `sample.xz` capture:

```
xz -dc sample.xz | ./nrsc5 0
```

## Known limitations

- AM mode: interleavers and FEC verified at component level; needs a
  real AM capture for end-to-end validation
- P3/P4 (supplemental program channels) verified but rarely exercised
  on air; PIDs for enhanced streams (`stream_id != 0`) are TODO (as in C)
- The FFT is a straightforward radix-2 implementation (~86 µs/frame) —
  fine for real time, but there's headroom for further optimization

## License

This is a port of GPLv3 code: nrsc5, Phil Karn's Reed-Solomon codec, and
the Ettus Research Viterbi decoder. Distributed under the GPL.