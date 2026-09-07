"""SIS/PIDS decoding (mirrors pids.go): station identity, names,
messages, coordinates, service information, parameters, alerts."""

from __future__ import annotations

import math

from .defines import PIDS_FRAME_LEN

MAX_LONG_NAME_LEN = 56
MAX_LONG_NAME_FRAMES = 8
MAX_MESSAGE_LEN = 190
MAX_MESSAGE_FRAMES = 32
MAX_AUDIO_SERVICES = 8
MAX_DATA_SERVICES = 16
NUM_PARAMETERS = 13
MAX_UNIVERSAL_SHORT_NAME_LEN = 12
MAX_UNIVERSAL_SHORT_NAME_FRAMES = 2
MAX_SLOGAN_LEN = 95
MAX_SLOGAN_FRAMES = 16
MAX_ALERT_LEN = 381
MAX_ALERT_FRAMES = 64
MAX_ALERT_CNT_LEN = 63
MAX_ALERT_LOCATIONS = 31

ALERT_TIMEOUT_LIMIT = 16
PIDS_TYPE_SIS = 0

ENCODING_ISO_8859_1 = 0
ENCODING_UCS_2 = 4

CHARS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ ?-*$ "
PAYLOAD_SIZES = [32, 22, 58, 32, 27, 58, 27, 22, 58, 58, 27, -1, -1, -1, -1, -1]


def crc12(bits) -> int:
    poly = 0xD010
    reg = 0x0000
    for i in range(67, -1, -1):
        lowbit = reg & 1
        reg >>= 1
        reg ^= bits[i] << 15
        if lowbit:
            reg ^= poly
    for _ in range(16):
        lowbit = reg & 1
        reg >>= 1
        if lowbit:
            reg ^= poly
    reg ^= 0x955
    return reg & 0xFFF


def check_crc12(bits) -> bool:
    expected = 0
    for i in range(68, 80):
        expected = (expected << 1) | bits[i]
    return expected == crc12(bits)


def crc7(alert, length: int) -> int:
    poly = 0x09
    reg = 0x42
    for byte_index in range(length - 1, -1, -1):
        for bit_index in range(6, -1, -1):
            bit = (alert[byte_index] >> bit_index) & 1
            if bit_index == 0 and byte_index > 0:
                bit ^= (alert[byte_index - 1] >> 7) & 1
            reg = (reg << 1) & 0xFF
            reg ^= bit
            if reg & 0x80:
                reg ^= 0x80 | poly
    for _ in range(6, -1, -1):
        reg = (reg << 1) & 0xFF
        if reg & 0x80:
            reg ^= 0x80 | poly
    return reg


def control_data_crc(control_data, length: int) -> int:
    poly = 0xD010
    reg = 0x7E1B
    for byte_index in range(length - 1, 0, -1):
        for bit_index in range(8):
            bit = (control_data[byte_index] >> bit_index) & 1
            if byte_index == 1 or (byte_index == 2 and bit_index < 4):
                bit = 0
            lowbit = reg & 1
            reg >>= 1
            reg ^= bit << 15
            if lowbit:
                reg ^= poly
    for _ in range(16):
        lowbit = reg & 1
        reg >>= 1
        if lowbit:
            reg ^= poly
    return reg & 0xFFF


class Pids:
    def __init__(self, inp=None):
        self.reset_common()
        self.input = inp

    def reset_common(self):
        self.country_code = ""
        self.fcc_facility_id = -1
        self.short_name = ""
        self.long_name = [""] * MAX_LONG_NAME_FRAMES
        self.long_name_have_frame = [0] * MAX_LONG_NAME_FRAMES
        self.long_name_seq = -1
        self.long_name_displayed = False
        self.latitude = math.nan
        self.longitude = math.nan
        self.altitude = 0
        self.message = bytearray(MAX_MESSAGE_LEN + 1)
        self.message_have_frame = [0] * MAX_MESSAGE_FRAMES
        self.message_seq = -1
        self.message_priority = 0
        self.message_encoding = 0
        self.message_len = 0
        self.message_checksum = 0
        self.message_displayed = False
        self.audio_services = [(-1, -1, -1)] * MAX_AUDIO_SERVICES
        self.data_services = [(-1, -1, -1)] * MAX_DATA_SERVICES
        self.parameters = [-1] * NUM_PARAMETERS
        self.universal_short_name = ""
        self.universal_short_name_have_frame = [0] * MAX_UNIVERSAL_SHORT_NAME_FRAMES
        self.universal_short_name_encoding = 0
        self.universal_short_name_append = -1
        self.universal_short_name_len = -1
        self.universal_short_name_displayed = False
        self.slogan = bytearray(MAX_SLOGAN_LEN + 1)
        self.slogan_have_frame = [0] * MAX_SLOGAN_FRAMES
        self.slogan_seq = -1
        self.slogan_encoding = 0
        self.slogan_len = -1
        self.slogan_displayed = False
        self.alert = bytearray(MAX_ALERT_LEN)
        self.alert_have_frame = [0] * MAX_ALERT_FRAMES
        self.alert_seq = -1
        self.alert_encoding = 0
        self.alert_len = -1
        self.alert_crc = -1
        self.alert_cnt_len = 0
        self.alert_displayed = False
        self.alert_timeout = 0

    def init(self, inp):
        self.reset_common()
        self.input = inp

    # --- helpers -----------------------------------------------------------

    @staticmethod
    def decode_int(bits, off, length: int) -> int:
        result = 0
        for _ in range(length):
            result = (result << 1) | bits[off[0]]
            off[0] += 1
        return result

    @staticmethod
    def decode_int_reverse(bits, off, length: int) -> int:
        result = 0
        for i in range(length):
            result |= bits[off[0]] << i
            off[0] += 1
        return result

    def decode_signed_int(self, bits, off, length: int) -> int:
        result = self.decode_int(bits, off, length)
        if result & (1 << (length - 1)):
            return result - (1 << length)
        return result

    def decode_char5(self, bits, off) -> str:
        return CHARS[self.decode_int(bits, off, 5)]

    def decode_char7(self, bits, off) -> str:
        return chr(self.decode_int(bits, off, 7))

    # --- frame entry --------------------------------------------------------

    def frame_push(self, bits: bytes):
        pids = bytearray(PIDS_FRAME_LEN)
        for i in range(PIDS_FRAME_LEN):
            pids[i] = bits[((i >> 3) << 3) + 7 - (i & 7)]
        if not check_crc12(pids):
            return
        if pids[0] == PIDS_TYPE_SIS:
            self.sis_decode(pids[1:])

    def sis_decode(self, bits):
        off = [0]
        updated = False
        payloads = bits[0] + 1
        off[0] += 1
        if self.alert_displayed:
            self.alert_timeout += 1
        for _ in range(payloads):
            if off[0] > 59:
                break
            msg_id = self.decode_int(bits, off, 4)
            if msg_id >= len(PAYLOAD_SIZES) or PAYLOAD_SIZES[msg_id] == -1:
                break
            payload_size = PAYLOAD_SIZES[msg_id]
            if off[0] > 63 - payload_size:
                break
            sub = bits[off[0]:]
            if msg_id == 0:
                updated |= self.sis_station_id(sub)
                off[0] += 32
            elif msg_id == 1:
                updated |= self.sis_station_name_short(sub)
                off[0] += 22
            elif msg_id == 2:
                updated |= self.sis_station_name_long(sub)
                off[0] += 58
            elif msg_id == 3:
                off[0] += 32
            elif msg_id == 4:
                updated |= self.sis_station_location(sub)
                off[0] += 27
            elif msg_id == 5:
                updated |= self.sis_station_message(sub)
                off[0] += 58
            elif msg_id in (6, 10):
                updated |= self.sis_service_information(sub)
                off[0] += 27
            elif msg_id == 7:
                self.sis_parameter(sub)
                off[0] += 22
            elif msg_id == 8:
                updated |= self.sis_universal_short_name(sub)
                off[0] += 58
            elif msg_id == 9:
                updated |= self.sis_emergency_alerts(sub)
                off[0] += 58
        if self.alert_displayed and self.alert_timeout >= ALERT_TIMEOUT_LIMIT:
            self.alert_displayed = False
            self.alert_timeout = 0
            self.input.radio.report_emergency_alert(None)
            updated = True
        if updated:
            self.report()

    def report(self):
        if self.alert_displayed:
            alert_text = self.utf8(self.alert_encoding,
                                   bytes(self.alert[self.alert_cnt_len:self.alert_len]))
        else:
            alert_text = ""
        self.input.radio.report_sis(dict(
            country_code=self.country_code,
            fcc_facility_id=self.fcc_facility_id,
            name=(self.universal_short_name_final
                  if self.universal_short_name_displayed else self.short_name),
            slogan=self.slogan_str(),
            message=self.utf8(self.message_encoding,
                              bytes(self.message[:self.message_len]))
            if self.message_displayed else "",
            alert=alert_text,
            latitude=self.latitude, longitude=self.longitude,
            altitude=self.altitude,
            audio_services=self.audio_services,
            data_services=self.data_services))

    def slogan_str(self) -> str:
        if self.slogan_displayed:
            return self.utf8(self.slogan_encoding, bytes(self.slogan[:self.slogan_len]))
        if self.long_name_displayed:
            return "".join(self.long_name)
        return ""

    @staticmethod
    def utf8(encoding: int, buf: bytes) -> str:
        if encoding == ENCODING_ISO_8859_1:
            return buf.decode("latin-1")
        if encoding == ENCODING_UCS_2:
            try:
                return buf.decode("utf-16-le")
            except UnicodeDecodeError:
                return ""
        return ""

    # --- message decoders (condensed but complete) ---------------------------

    def sis_station_id(self, bits) -> bool:
        off = [0]
        cc = self.decode_char5(bits, off) + self.decode_char5(bits, off)
        off[0] += 3
        fid = self.decode_int(bits, off, 19)
        if cc != self.country_code or fid != self.fcc_facility_id:
            self.country_code = cc
            self.fcc_facility_id = fid
            self.input.radio.report_station_id(cc, fid)
            return True
        return False

    def sis_station_name_short(self, bits) -> bool:
        off = [0]
        name = self.decode_char5(bits, off) + self.decode_char5(bits, off) \
            + self.decode_char5(bits, off) + self.decode_char5(bits, off)
        if bits[off[0]] == 0 and bits[off[0] + 1] == 1:
            name += "-FM"
        off[0] += 2
        if name != self.short_name:
            self.short_name = name
            self.input.radio.report_station_name(name)
            return True
        return False

    def sis_station_name_long(self, bits) -> bool:
        off = [0]
        tmp = [55]
        last_frame = self.decode_int(bits, off, 3)
        current_frame = self.decode_int(bits, off, 3)
        seq = self.decode_int(bits, tmp, 3)
        if current_frame == 0 and seq != self.long_name_seq:
            self.long_name = [""] * MAX_LONG_NAME_FRAMES
            self.long_name_have_frame = [0] * MAX_LONG_NAME_FRAMES
            self.long_name_seq = seq
            self.long_name_displayed = False
        chars = ""
        for _ in range(7):
            chars += self.decode_char7(bits, off)
        self.long_name[current_frame] = chars
        self.long_name_have_frame[current_frame] = 1
        if self.long_name_seq >= 0 and not self.long_name_displayed:
            complete = all(self.long_name_have_frame[j]
                           for j in range(last_frame + 1))
            if complete:
                self.long_name_displayed = True
                name = "".join(self.long_name)[:MAX_LONG_NAME_LEN + 1]
                if not self.slogan_displayed:
                    self.input.radio.report_station_slogan(
                        name.encode("latin-1").decode("latin-1"))
                return True
        return False

    def sis_station_location(self, bits) -> bool:
        off = [0]
        updated = False
        if bits[off[0]]:
            off[0] += 1
            lat = self.decode_signed_int(bits, off, 22) / 8192.0
            alt_high = self.decode_int(bits, off, 4) << 8
            if lat != self.latitude or alt_high != (self.altitude & 0xF00):
                self.latitude = lat
                self.altitude = (self.altitude & 0x0F0) | alt_high
                updated = True
        else:
            off[0] += 1
            lon = self.decode_signed_int(bits, off, 22) / 8192.0
            alt_low = self.decode_int(bits, off, 4) << 4
            if lon != self.longitude or alt_low != (self.altitude & 0x0F0):
                self.longitude = lon
                self.altitude = (self.altitude & 0xF00) | alt_low
                updated = True
        if updated and not math.isnan(self.latitude) \
                and not math.isnan(self.longitude):
            self.input.radio.report_station_location(
                self.latitude, self.longitude, self.altitude)
        return updated

    def sis_station_message(self, bits) -> bool:
        off = [0]
        updated = False
        current_frame = self.decode_int(bits, off, 5)
        seq = self.decode_int(bits, off, 2)
        if current_frame == 0:
            if seq != self.message_seq:
                self.message = bytearray(MAX_MESSAGE_LEN + 1)
                self.message_have_frame = [0] * MAX_MESSAGE_FRAMES
                self.message_seq = seq
                self.message_displayed = False
            self.message_priority = bits[off[0]]
            off[0] += 1
            self.message_encoding = self.decode_int(bits, off, 3)
            self.message_len = self.decode_int(bits, off, 8)
            self.message_checksum = self.decode_int(bits, off, 7)
            for j in range(4):
                self.message[j] = self.decode_int(bits, off, 8)
        else:
            off[0] += 3
            for j in range(6):
                self.message[current_frame * 6 - 2 + j] = self.decode_int(bits, off, 8)
        self.message_have_frame[current_frame] = 1
        if self.message_seq >= 0 and not self.message_displayed:
            complete = all(self.message_have_frame[j]
                           for j in range((self.message_len + 7) // 6))
            if complete:
                checksum = sum(self.message[:self.message_len]) & 0xFFFFFFFF
                checksum = ((checksum >> 8) & 0x7F) + (checksum & 0xFF) & 0x7F
                if checksum == self.message_checksum:
                    self.message_displayed = True
                    self.input.radio.report_station_message(
                        self.utf8(self.message_encoding,
                                  bytes(self.message[:self.message_len])))
                    updated = True
        return updated

    def sis_service_information(self, bits) -> bool:
        off = [0]
        category = self.decode_int(bits, off, 2)
        if category == 0:
            access = self.decode_int(bits, off, 1)
            prog_num = self.decode_int(bits, off, 6)
            typ = self.decode_int(bits, off, 8)
            off[0] += 5
            sound_exp = self.decode_int(bits, off, 5)
            if prog_num < MAX_AUDIO_SERVICES:
                self.audio_services[prog_num] = (access, typ, sound_exp)
                self.input.radio.report_asd(prog_num, access, typ, sound_exp)
                return True
        elif category == 1:
            access = self.decode_int(bits, off, 1)
            typ = self.decode_int(bits, off, 9)
            off[0] += 3
            mime = self.decode_int(bits, off, 12)
            for j in range(MAX_DATA_SERVICES):
                if self.data_services[j] == (access, typ, mime):
                    break
                if self.data_services[j][1] == -1:
                    self.data_services[j] = (access, typ, mime)
                    self.input.radio.report_dsd(access, typ, mime)
                    return True
        return False

    def sis_parameter(self, bits):
        off = [0]
        index = self.decode_int(bits, off, 6)
        parameter = self.decode_int(bits, off, 16)
        if index < NUM_PARAMETERS:
            self.parameters[index] = parameter

    def sis_universal_short_name(self, bits) -> bool:
        off = [0]
        current_frame = self.decode_int(bits, off, 4)
        if bits[off[0]] == 0:
            off[0] += 1
            if current_frame == 0:
                self.universal_short_name_encoding = self.decode_int(bits, off, 3)
                self.universal_short_name_append = bits[off[0]]
                off[0] += 1
                self.universal_short_name_len = bits[off[0]] + 1
                off[0] += 1
                chars = ""
                for _ in range(6):
                    chars += chr(self.decode_int(bits, off, 8))
                self.universal_short_name = chars
            self.universal_short_name_have_frame[current_frame] = 1
            if self.universal_short_name_len >= 0 \
                    and not self.universal_short_name_displayed:
                if all(self.universal_short_name_have_frame):
                    name = self.universal_short_name
                    if self.universal_short_name_append:
                        name += "-FM"
                    self.universal_short_name_final = name
                    self.universal_short_name_displayed = True
                    self.input.radio.report_station_name(name)
                    return True
        return False

    def sis_emergency_alerts(self, bits) -> bool:
        off = [0]
        current_frame = self.decode_int(bits, off, 6)
        seq = self.decode_int(bits, off, 2)
        off[0] += 2
        self.alert_timeout = 0
        if current_frame == 0:
            if seq != self.alert_seq:
                self.alert = bytearray(MAX_ALERT_LEN)
                self.alert_have_frame = [0] * MAX_ALERT_FRAMES
                self.alert_seq = seq
                self.alert_displayed = False
            self.alert_encoding = self.decode_int(bits, off, 3)
            self.alert_len = self.decode_int(bits, off, 9)
            self.alert_crc = self.decode_int(bits, off, 7)
            self.alert_cnt_len = 1 + 2 * self.decode_int(bits, off, 5)
            for j in range(3):
                self.alert[j] = self.decode_int(bits, off, 8)
        else:
            for j in range(6):
                self.alert[current_frame * 6 - 3 + j] = self.decode_int(bits, off, 8)
        self.alert_have_frame[current_frame] = 1
        if self.alert_len >= 0 and not self.alert_displayed:
            if all(self.alert_have_frame[j]
                   for j in range((self.alert_len + 8) // 6)):
                if crc7(self.alert, self.alert_len) != self.alert_crc:
                    return False
                if self.alert_cnt_len < 7 or self.alert_len < self.alert_cnt_len:
                    return False
                actual = ((self.alert[2] & 0xF) << 8) | self.alert[1]
                expected = control_data_crc(self.alert[:self.alert_cnt_len],
                                            self.alert_cnt_len)
                if actual == expected:
                    self.alert_displayed = True
                    self.input.radio.report_emergency_alert(dict(
                        message=self.utf8(self.alert_encoding,
                                          bytes(self.alert[self.alert_cnt_len:self.alert_len])),
                        control_data=bytes(self.alert[:self.alert_cnt_len])))
                    return True
        return False
