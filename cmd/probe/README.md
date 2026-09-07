# probe

A diagnostic tool for RTL-SDR dongles over rtl_tcp. Connects to a
running `rtl_tcp -a 127.0.0.1 -p 1234` and runs:

1. **Gain sweep** at 89.3 MHz (WLRH) — total power and rail fraction
   per gain step. A healthy dongle shows power climbing ~49 dB across
   the sweep; a broken tuner shows a flat line.
2. **Band scan** — average power 88–108 MHz at max gain. Real stations
   stand out; a flat band means no RF is reaching the tuner.

Built while diagnosing a dead R820T tuner: USB streamed fine, the
RTL2832 ADC worked (verified via direct-sampling mode), but no RF
passed the tuner at any gain.

Usage:

    rtl_tcp -a 127.0.0.1 -p 1234 &
    go run ./cmd/probe
