# C reference decoder build (for comparisons)

The component tests and this comparison workflow use a manually-built
C nrsc5 binary (no cmake/autotools required on minimal machines):

```sh
git clone --depth 1 https://github.com/theori-io/nrsc5.git
git clone --depth 1 https://gitea.osmocom.org/sdr/rtl-sdr.git
git clone --depth 1 https://github.com/libusb/libusb.git

# librtlsdr static lib (see main repo README for the simple gcc Makefile)
cd rtl-sdr/src && make -f Makefile.manual

# nrsc5: libao is only needed for live audio output; a stub header
# (ao_stub.h with no-op functions) lets us build a file-decode-only CLI.
cd ../nrsc5/src
# config.h with LIBRARY_DEBUG_LEVEL=3, then:
make -f Makefile.manual    # produces ./nrsc5
```

The resulting binary is used to cross-check warnings and events against
the Go implementation.
