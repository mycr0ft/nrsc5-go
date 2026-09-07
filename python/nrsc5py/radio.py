"""Top-level receiver (mirrors radio.go).

Radio wires the pipeline: rtltcp (or piped IQ) → input (decimation) →
acquire → sync → decode → frame → output. Events surface through a
single callback, mirroring libnrsc5's nrsc5_callback_t.
"""

from __future__ import annotations

import threading

import numpy as np

from .defines import ModeFM, SAMPLE_RATE_CU8, SAMPLE_RATE_CS16_FM, \
    SAMPLE_RATE_CS16_AM


class Radio:
    def __init__(self):
        self.mode = ModeFM
        self.callback = None
        self.stopped = True
        self.closed = False
        self.freq = 0.0
        self.gain = -1.0
        self.auto_gain = True
        self.tcp = None
        self.iq_reader = None
        from .output import Output
        from .input import Input
        self.out = Output(self)
        self.iq = Input(self, self.out)
        self._worker = None

    @classmethod
    def open_rtltcp(cls, host: str, port: int = 1234) -> "Radio":
        radio = cls()
        tcp = _RtlTcpLazy(host, port)
        tcp.sock.set_sample_rate(SAMPLE_RATE_CU8)
        tcp.sock.set_tuner_gain_mode(True)
        tcp.sock.set_offset_tuning(True)
        radio.tcp = tcp
        return radio

    @classmethod
    def open_file(cls, reader) -> "Radio":
        radio = cls()
        radio.iq_reader = reader
        return radio

    # --- events -------------------------------------------------------------

    def set_callback(self, cb):
        self.callback = cb

    def _emit(self, event: dict):
        if self.callback:
            self.callback(event)

    def report_iq(self, data):
        self._emit(dict(type="iq", data=data))

    def report_lost_sync(self):
        self._emit(dict(type="lost_sync"))

    def report_sync(self, freq_offset, psmi, pli, hppi, aabi, rdbi):
        self._emit(dict(type="sync", freq_offset=freq_offset, psmi=psmi,
                        pli=pli, hppi=hppi, aabi=aabi, rdbi=rdbi))

    def report_mer(self, lower, upper):
        self._emit(dict(type="mer", lower=lower, upper=upper))

    def report_ber(self, cber):
        self._emit(dict(type="ber", cber=cber))

    def report_lost_device(self):
        self._emit(dict(type="lost_device"))

    def report_audio(self, program, samples):
        self._emit(dict(type="audio", program=program, samples=samples))

    def report_hdc(self, program, data, flags):
        self._emit(dict(type="hdc", program=program, data=data, flags=flags))

    def report_sis(self, sis):
        self._emit(dict(type="sis", sis=sis))

    def report_emergency_alert(self, ea):
        self._emit(dict(type="emergency_alert", ea=ea))

    def report_station_id(self, cc, fid):
        self._emit(dict(type="station_id", country_code=cc,
                        fcc_facility_id=fid))

    def report_station_name(self, name):
        self._emit(dict(type="station_name", name=name))

    def report_station_slogan(self, slogan):
        self._emit(dict(type="station_slogan", slogan=slogan))

    def report_station_message(self, message):
        self._emit(dict(type="station_message", message=message))

    def report_station_location(self, lat, lon, alt):
        self._emit(dict(type="station_location", latitude=lat,
                        longitude=lon, altitude=alt))

    def report_asd(self, program, access, typ, sound_exp):
        self._emit(dict(type="asd", program=program, access=access,
                        service_type=typ, sound_exp=sound_exp))

    def report_dsd(self, access, typ, mime):
        self._emit(dict(type="dsd", access=access, service_type=typ, mime=mime))

    def report_audio_service(self, program, access, typ, codec, blend,
                             gain, delay, latency):
        self._emit(dict(type="audio_service", program=program, access=access,
                        service_type=typ, codec=codec, blend=blend,
                        gain=gain, delay=delay, latency=latency))

    def report_id3(self, evt):
        self._emit(dict(type="id3", **evt))

    def report_sig(self, services):
        self._emit(dict(type="sig", services=services))

    def report_stream(self, seq, size, data, component):
        self._emit(dict(type="stream", seq=seq, size=size, data=data))

    def report_packet(self, seq, size, data, component):
        self._emit(dict(type="packet", seq=seq, size=size, data=data))

    def report_lot(self, lot, file):
        self._emit(dict(type="lot", lot=lot, name=file.name, size=file.size,
                        mime=file.mime, data=file.data
                        if file.data is not None else b"".join(
                            f for f in file.fragments if f)))

    # --- control --------------------------------------------------------------

    def set_mode(self, mode: int) -> bool:
        if mode in (0, 1):
            self.mode = mode
            self.iq.set_mode()
            return True
        return False

    def set_frequency(self, freq: float) -> bool:
        if self.freq == freq or not self.stopped:
            return self.freq == freq
        if self.tcp:
            self.tcp.sock.set_center_freq(int(freq))
        self.reset()
        self.freq = freq
        return True

    def set_gain(self, gain: float) -> bool:
        if self.gain == gain or not self.stopped:
            return self.gain == gain
        if self.tcp:
            self.tcp.sock.set_tuner_gain(int(gain * 10))
        self.gain = gain
        return True

    def set_auto_gain(self, enabled: bool):
        self.auto_gain = enabled
        self.gain = -1

    def reset(self):
        self.iq.reset()
        self.out.reset()

    def start(self):
        self.stopped = False
        if self.tcp is not None and self._worker is None:
            self._start_worker()

    def stop(self):
        self.stopped = True

    def close(self):
        if self._worker:
            self._worker.join()
        if self.tcp:
            self.tcp.sock.close()

    # --- worker (rtl_tcp sample pump) -------------------------------------------

    def _start_worker(self):
        import threading
        def run():
            while not self.stopped:
                if self.tcp is None:
                    break
                data = self.tcp.sock.read(128 * 256)
                if not data:
                    self.report_lost_device()
                    return
                self.iq.push_cu8(np.frombuffer(data, dtype=np.uint8))
        self._worker = threading.Thread(target=run, daemon=True)
        self._worker.start()

    def run_file(self):
        """Blocking pump for file/pipe input."""
        buf = np.zeros(128 * 256, dtype=np.uint8)
        while not self.stopped:
            n = self.iq_reader.readinto(buf)
            if not n:
                self.report_lost_device()
                return
            self.iq.push_cu8(buf[:n & ~3])

    def pipe_samples_cu8(self, samples):
        self.iq.push_cu8(np.asarray(samples, dtype=np.uint8))

    # --- auto gain ---------------------------------------------------------

    def do_auto_gain(self) -> bool:
        gains = self.tcp.sock.get_tuner_gains()
        if not gains:
            return False
        best_gain = 0
        low, high = 0, len(gains) - 1
        while low <= high:
            mid = (low + high) // 2
            gain = gains[mid]
            self.tcp.sock.set_tuner_gain(gain)
            self.tcp.sock.reset_buffer(SAMPLE_RATE_CU8 // 4 * 2)
            buf = self.tcp.sock.read(128 * 256)
            body = np.frombuffer(buf[4 * len(buf) // 4:], dtype=np.uint8)
            amp_db = 20 * np.log10((int(body.max()) - int(body.min()) + 1) / 256)
            if amp_db < -6:
                best_gain = gain
                low = mid + 1
            else:
                high = mid - 1
            if high == -1:
                best_gain = gain
        self.gain = best_gain / 10
        self.tcp.sock.set_tuner_gain(best_gain)
        return True


class _RtlTcpLazy:
    def __init__(self, host: str, port: int):
        from .rtltcp import RtlTcp
        self.sock = RtlTcp(host, port)
        self.host = host
        self.port = port
