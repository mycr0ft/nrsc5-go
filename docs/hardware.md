# SDR hardware guide for goradio / nrsc5-go

Recommendations for the next purchase, grounded in what this software
stack supports today and what a HAM technician can actually use.

## What you already have

| Equipment | Coverage | Notes |
|---|---|---|
| E4000 dongle (working) | 100 kHz – 1.7 GHz | verified live with nrsc5-go + goradio |
| R820T dongle (dead tuner) | — | demodulator fine, tuner stage dead |
| Ham It Up upconverter | HF: 0–30 MHz | upconverts HF to a ~125 MHz IF for any dongle |
| SoapySDR (system) | — | modules installed for airspy, HackRF, rtlsdr, miri, remote, uhd, LimeSDR, bladeRF |

**HF reception is already covered** by the dongle + Ham It Up combo, so
the "buy more ADCs" question is really about: better dynamic range,
TX capability, or wider bandwidth.

## The candidates

### 1. RTL-SDR Blog V4 dongle — $40–45

The straightforward upgrade from your E4000 dongle:

- 500 kHz – 1.7 GHz with HF via built-in direct sampling (no upconverter
  needed for HF)
- built-in bias-T, better filtering, thermal stability
- **works with this software stack today** (standard rtl_tcp)
- same R820T2-class 8-bit ADC — the improvement is RF front-end, not
  digitizer quality

Best if: you want a reliable daily driver dongle and a spare.

### 2. SDRplay RSP1A — ~$115

- 14-bit ADC (huge dynamic-range win over any dongle's 8-bit)
- 1 kHz – 2 GHz, multiple switchable antenna inputs
- handles strong adjacent signals far better — the exact weakness that
  killed your E4000 reception at 90.3 MHz
- caveat: **not rtl_tcp** — speaks SDRplay's own API. Works with
  SoapySDR (`libmiri`/`libsdrplay` modules), SDR#, GQRX, SDRuno
- our Go tools would need a bridge (see "software path" below)

Best if: your main pain is weak-signal RX among strong neighbors.

### 3. HackRF One (or a clone) — $330 (clone ~$100–150)

- 1 MHz – 6 GHz, **TX and RX**, half-duplex
- 8-bit ADC like a dongle, but with proper RF front-end, amps, filters
- TX up to 20 MHz bandwidth — the natural "next step" for a HAM who
  wants to experiment (digital modes via GNUradio, ADS-B TX testing on
  dummy loads, etc.)
- system already has the HackRF SoapySDR module installed
- TX **requires** proper filtering and antennas per Part 97 — never
  transmit harmonics into the air

Best if: TX experiments and 6 GHz coverage matter. Note: 8-bit ADC =
same weak-signal limits as a dongle.

### 4. ADALM-Pluto (PlutoSDR) — ~$230 (often $99 educational on sale)

- **TX and RX**, 325 MHz – 3.8 GHz (hackable to 70 MHz – 6 GHz)
- 12-bit ADC/DAC — much better dynamic range than a dongle
- full-duplex (simultaneous TX + RX on separate channels)
- speaks its own IP-based protocol over USB ethernet; GNUradio-native
- HAM-friendly: WSPR/FT8 TX via GNUradio, satellites with the right
  filters

Best if: you want a real transceiver-class device for HAM work.

### 5. Airspy HF+ Discovery — ~$169

- HF + VHF specialized, 18-bit equivalent SNR in HF mode
- best-in-class weak-signal HF reception at this price
- caveat: no TX, and two fixed bands (HF and VHF) rather than continuous

Best if: HF DX and weak-signal work is the goal.

## The recommendation, in order

1. **SDRplay RSP1A** if the next step is *reception quality* — the
   14-bit ADC is the single biggest upgrade you can buy. Pairs
   beautifully with the Ham It Up for HF and handles your local FM
   overload problem (the one that clipped the E4000 at 90.3).
2. **ADALM-Pluto** if TX is the goal — a real transceiver for HAM
   bands with GNUradio integration.
3. **RTL-SDR Blog V4** if you just want a solid replacement daily
   driver dongle and keep saving for something bigger.
4. **HackRF** if the 1 MHz–6 GHz range and protocol experiments matter
   more than weak-signal performance.

## Software compatibility with this repo

| Device | goradio / nrsc5-go today | Path |
|---|---|---|
| RTL-SDR Blog V4 | ✅ works (rtl_tcp) | none needed |
| SDRplay RSP1A | via SoapySDR | add a Soapy TCP bridge, or use SoapySDR Remote |
| HackRF | via SoapySDR | same |
| Pluto | native IP protocol | small Go client possible (IIO/SDR protocol) |
| Airspy | via SoapySDR | same |

The system already has SoapySDR with modules for all of these —
`SoapySDRUtil --info` lists airspy, HackRF, rtlsdr, miri, remote, uhd,
LimeSDR, bladeRF, RedPitaya. A `soapy_tcp` companion tool in Go (Soapy
device → rtl_tcp-compatible server) would make every future SDR work
with both receivers with zero changes to the DSP code.

## The Ham It Up + V4 combo

If a V4 dongle is the choice, HF goes fully direct-sampling:

```
antenna → Ham It Up (upconvert to ~125 MHz) → V4 dongle
   or: antenna → V4 direct-sampling input (0–28 MHz, bias-T powered)
```

The V4 has a dedicated direct-sampling HF input, making the Ham It Up
optional — but the upconverter generally outperforms direct sampling
below 15 MHz because it avoids the ADC's 1/f noise region.