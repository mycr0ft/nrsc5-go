# On-air operation: hardware notes

Everything learned the hard way getting the receiver to decode a real
station, with a ~$30 RTL-SDR dongle.

## Setup

1. **Free the dongle from the kernel DVB driver.** Linux's
   `dvb_usb_rtl28xxu` module claims RTL2832 devices at boot and blocks
   libusb access (`usb_claim_interface error -6`). Detach it:

   ```sh
   # find the interface ID
   ls /sys/bus/usb/drivers/dvb_usb_rtl28xxu/     # e.g. 2-1:1.0
   echo '2-1:1.0' | sudo tee /sys/bus/usb/drivers/dvb_usb_rtl28xxu/unbind
   sudo modprobe -r dvb_usb_rtl28xxu
   ```

   You must redo this every time the dongle is unplugged/replugged or
   suspended/resumed. To make it permanent, blacklist the module:

   ```sh
   echo 'blacklist dvb_usb_rtl28xxu' | sudo tee /etc/modprobe.d/rtl-sdr.conf
   ```

2. **Start rtl_tcp.** The `rtl-sdr` Debian package provides it, or build
   from source (the repo's autotools build needs autotools installed;
   a two-line gcc Makefile over `src/*.c` + `getopt/` +
   `convenience/` + libusb headers works fine — that's how we built it
   on a machine without them):

   ```sh
   rtl_tcp -a 127.0.0.1 -p 1234
   # Found 1 device(s)... Found Rafael Micro R820T tuner... Tuned to 100 MHz
   ```

3. **Run the receiver.**

   ```sh
   nrsc5 play -H 127.0.0.1:1234 -f 89.3 -g 30 0
   ```

## Finding the right gain — the important part

SDR gain is where most "it doesn't work" sessions go to die. The ADC
clips long before the tuner gain maxes out, and **clipped OFDM looks
like noise** in the spectrum — indistinguishable from no signal except
that nothing decodes.

The E4000 (and R820T) gain steps are coarse, so the usable range is
narrow. The fastest approach is a per-gain power sweep at your target
frequency:

```
gain   0 dB: rms²  13658 (41.4 dB) rails 35.1%   ← clipped
gain   9 dB: rms²   1488 (31.7 dB) rails  3.8%
gain  12 dB: rms²     47 (16.7 dB) rails  0.0%   ← no signal at this step
gain  18 dB: rms²     47 (16.8 dB) rails  0.0%   ← E4000 step gap
gain  24 dB: rms²   1022 (30.1 dB) rails  0.0%   ← sweet spot begins
gain  30 dB: rms²   1149 (30.6 dB) rails  0.0%
gain  42 dB: rms²  12656 (41.0 dB) rails 34.3%   ← clipped again
gain  49 dB: rms²  14777 (41.7 dB) rails 41.5%
```

`rails` is the fraction of samples pinned at 0 or 255 — anything above
~2% is destroying the OFDM carriers. Pick the highest gain with ~0%
rails, then back off 6 dB for headroom. For our E4000 on a strong local
station that was **20–30 dB**, not the 49.6 dB maximum.

A quick sanity check that RF is reaching the tuner at all: run the same
sweep on a nearby frequency with no station. Total rms² should rise with
gain and the band should look like smooth noise. If rms² never changes
with gain, check the antenna connection.

## Spectrum sanity checks

Two python one-liners that diagnose most problems from a raw capture:

```python
import numpy as np
data = np.fromfile('capture.iq', dtype=np.uint8, count=400000)
iq = (data[0::2].astype(np.float32)-127.5) + 1j*(data[1::2].astype(np.float32)-127.5)
spec = np.abs(np.fft.fftshift(np.fft.fft(iq[:262144])))**2
freqs = np.fft.fftshift(np.fft.fftfreq(len(spec), 1/1488375))
for lo in range(-280, 280, 40):
    m = (freqs >= lo*1e3) & (freqs < (lo+40)*1e3)
    print("%+4d..%+4d kHz: %6.1f dB" % (lo, lo+40, 10*np.log10(np.sum(spec[m]))))
```

What a healthy FM HD signal looks like (relative levels):

- big hump at **±0–100 kHz** — the analog FM carrier (you'll also see a
  DC spike at exactly 0 from the Zero-IF tuner; that's normal)
- **IBOC sidebands at ±130–280 kHz** clearly above the noise floor —
  those are the digital carriers you're decoding
- noise floor smoothly falling toward the band edges

What a broken capture looks like:

- **flat spectrum at every gain** — no antenna, or the kernel driver is
  still attached (check `rtl_tcp`'s output for
  `usb_claim_interface error -6`)
- flat spectrum **with rails** (samples pinned at 0/255) — overloading;
  back off the gain (see above)
- a single giant peak tens of dB above everything — a strong adjacent
  station overloading the front end (E4000 has poor adjacent rejection
  at wideband settings); retune closer to the target or lower gain

## Live results (Huntsville, AL)

Received with nrsc5-go + rtl_tcp on an RTL2838 with E4000 tuner:

- **WLRH 89.3 HD1** (Huntsville public radio): stable sync, MER 4–5 dB,
  all three audio services decoded (HD1/HD2 classical, HD3 news),
  3 minutes of continuous stereo audio, SIS decoded (facility ID 21,
  slogan "HD-1"), ID3 metadata parsed. MER was marginal — an antenna
  upgrade would improve it; sample-capture reception runs ~13 dB.
- The same session through `play` produced clean audio through
  PipeWire.

## Troubleshooting checklist

| Symptom | Likely cause |
|---|---|
| `usb_claim_interface error -6` | kernel DVB driver holds the device — unbind (above) |
| `PLL not locked` | rtl_tcp's default tuning; harmless if it tunes later |
| No sync, flat spectrum | no antenna, or wrong frequency |
| No sync, rails > 2% | overloading — reduce gain |
| Syncs then drops repeatedly | marginal signal or gain too low/high; check MER |
| Sync but no audio program | wrong program number, or the station's HD2/HD3 uses a mode not yet handled |
| Audio has gaps | packet loss — usually gain/signal; check `Audio packet CRC mismatches` |