use super::{
    codec::{Codec, Frame, Packet, checked},
    devices::Device,
};
use crate::ffi;
use std::ptr;

fn jpeg_header(bytes: &[u8]) -> Result<(u32, u32, bool), String> {
    if bytes.get(..2) != Some(&[255, 216]) {
        return Err("The capture packet is not a complete JPEG frame.".into());
    }
    let mut position = 2;
    while position + 4 <= bytes.len() {
        if bytes[position] != 255 {
            position += 1;
            continue;
        }
        while position < bytes.len() && bytes[position] == 255 {
            position += 1;
        }
        let Some(&marker) = bytes.get(position) else {
            break;
        };
        position += 1;
        if matches!(marker, 1 | 0xd0..=0xd9) {
            continue;
        }
        if marker == 0xda {
            break;
        }
        let length = bytes
            .get(position..position + 2)
            .map(|value| u16::from_be_bytes([value[0], value[1]]) as usize)
            .ok_or("Truncated JPEG segment.")?;
        if length < 2 {
            return Err("Invalid JPEG segment length.".into());
        }
        let segment = bytes
            .get(
                position
                    ..position
                        .checked_add(length)
                        .ok_or("JPEG segment overflow.")?,
            )
            .ok_or("Truncated JPEG segment.")?;
        if matches!(marker, 0xc0..=0xc3 | 0xc5..=0xc7 | 0xc9..=0xcb | 0xcd..=0xcf) {
            if segment.len() < 8 {
                return Err("Truncated JPEG dimensions.".into());
            }
            let height = u16::from_be_bytes([segment[3], segment[4]]) as u32;
            let width = u16::from_be_bytes([segment[5], segment[6]]) as u32;
            let components = segment[7] as usize;
            if components == 0 || segment.len() < 8 + components * 3 {
                return Err("Truncated JPEG components.".into());
            }
            let hardware = marker == 0xc0
                && segment[2] == 8
                && components == 3
                && segment[9] == 0x22
                && segment[12] == 0x11
                && segment[15] == 0x11;
            return Ok((width, height, hardware));
        }
        position += length;
    }
    Err("JPEG frame dimensions are missing.".into())
}

unsafe extern "C" fn cuda_format(
    _context: *mut ffi::AVCodecContext,
    formats: *const ffi::AVPixelFormat,
) -> ffi::AVPixelFormat {
    for index in 0..64 {
        let value = unsafe { *formats.add(index) };
        if value == ffi::AVPixelFormat_AV_PIX_FMT_NONE {
            break;
        }
        if value == ffi::AVPixelFormat_AV_PIX_FMT_CUDA {
            return value;
        }
    }
    ffi::AVPixelFormat_AV_PIX_FMT_NONE
}

pub(crate) struct Decoder {
    context: Option<Codec>,
    frame: Frame,
    downloaded: Frame,
    packet: Packet,
    device: Device,
    width: u32,
    height: u32,
    hardware: bool,
    failures: u32,
    pub name: String,
}
impl Decoder {
    pub fn new(device: &Device, width: u32, height: u32) -> Result<Self, String> {
        Ok(Self {
            context: None,
            frame: Frame::new()?,
            downloaded: Frame::new()?,
            packet: Packet::new()?,
            device: device.clone(),
            width,
            height,
            hardware: false,
            failures: 0,
            name: "MJPEG awaiting input".into(),
        })
    }
    fn initialize(&mut self, hardware: bool) -> Result<(), String> {
        self.context = None;
        let codec = unsafe {
            if hardware {
                ffi::avcodec_find_decoder_by_name(c"mjpeg_cuvid".as_ptr())
            } else {
                ffi::avcodec_find_decoder(ffi::AVCodecID_AV_CODEC_ID_MJPEG)
            }
        };
        if codec.is_null() {
            return Err("The requested MJPEG decoder is unavailable.".into());
        }
        let context = Codec(unsafe { ffi::avcodec_alloc_context3(codec) });
        if context.0.is_null() {
            return Err("Unable to allocate a recording decoder.".into());
        }
        unsafe {
            (*context.0).width = self.width as i32;
            (*context.0).height = self.height as i32;
            (*context.0).max_pixels = i64::from(self.width) * i64::from(self.height);
            (*context.0).thread_count = 1;
            (*context.0).sample_aspect_ratio = ffi::AVRational { num: 1, den: 1 };
            // MJPEG frames are independent, so CUVID's default display queue would add avoidable delay.
            (*context.0).flags |= ffi::AV_CODEC_FLAG_LOW_DELAY as i32;
            if hardware {
                let target = std::ffi::CString::new(self.device.device.as_str()).unwrap();
                checked(
                    ffi::av_hwdevice_ctx_create(
                        &mut (*context.0).hw_device_ctx,
                        ffi::AVHWDeviceType_AV_HWDEVICE_TYPE_CUDA,
                        target.as_ptr(),
                        ptr::null_mut(),
                        0,
                    ),
                    "Initialize NVDEC recording device",
                )?;
                (*context.0).get_format = Some(cuda_format);
                let _ = ffi::av_opt_set_int((*context.0).priv_data, c"surfaces".as_ptr(), 8, 0);
            }
            checked(
                ffi::avcodec_open2(context.0, codec, ptr::null_mut()),
                "Open recording MJPEG decoder",
            )?;
        }
        self.context = Some(context);
        self.hardware = hardware;
        self.name = if hardware {
            "NVDEC/CUDA MJPEG"
        } else {
            "Software MJPEG"
        }
        .into();
        Ok(())
    }
    pub fn decode(&mut self, bytes: &[u8]) -> Result<Option<*mut ffi::AVFrame>, String> {
        let payload = bytes
            .get(..bytes.len().saturating_sub(64))
            .ok_or("Missing JPEG packet padding.")?;
        let (width, height, supported) = match jpeg_header(payload) {
            Ok(header) => header,
            Err(error) => {
                self.failures += 1;
                if self.failures >= 120 {
                    return Err(error);
                } else {
                    return Ok(None);
                }
            }
        };
        if width != self.width || height != self.height {
            return Err("JPEG dimensions changed during recording.".into());
        }
        if self.context.is_none()
            && !(supported && self.device.backend == "nvenc" && self.initialize(true).is_ok())
        {
            self.initialize(false)?;
        }
        let result = self.decode_packet(payload);
        let result = if result.is_err() && self.hardware {
            self.initialize(false)?;
            self.decode_packet(payload)
        } else {
            result
        };
        match result {
            Ok(value) => {
                self.failures = 0;
                Ok(value)
            }
            Err(error) => {
                self.failures += 1;
                if self.failures >= 120 {
                    Err(error)
                } else {
                    Ok(None)
                }
            }
        }
    }
    fn decode_packet(&mut self, bytes: &[u8]) -> Result<Option<*mut ffi::AVFrame>, String> {
        let context = &self.context.as_ref().unwrap();
        unsafe {
            ffi::av_packet_unref(self.packet.0);
            checked(
                ffi::av_new_packet(
                    self.packet.0,
                    i32::try_from(bytes.len()).map_err(|_| "JPEG packet is too large.")?,
                ),
                "Allocate padded JPEG packet",
            )?;
            ptr::copy_nonoverlapping(bytes.as_ptr(), (*self.packet.0).data, bytes.len());
            checked(
                ffi::avcodec_send_packet(context.0, self.packet.0),
                "Submit recording JPEG",
            )?;
            ffi::av_frame_unref(self.frame.0);
            let code = ffi::avcodec_receive_frame(context.0, self.frame.0);
            if code == -11 {
                return Err("The JPEG decoder delayed an independent capture frame.".into());
            }
            checked(code, "Decode recording JPEG")?;
            if (*self.frame.0).width != self.width as i32
                || (*self.frame.0).height != self.height as i32
            {
                return Err("Decoded JPEG dimensions changed.".into());
            }
            if (*self.frame.0).format == ffi::AVPixelFormat_AV_PIX_FMT_CUDA {
                ffi::av_frame_unref(self.downloaded.0);
                checked(
                    ffi::av_hwframe_transfer_data(self.downloaded.0, self.frame.0, 0),
                    "Transfer NVDEC output to recording staging",
                )?;
                checked(
                    ffi::av_frame_copy_props(self.downloaded.0, self.frame.0),
                    "Copy decoded color metadata",
                )?;
                Ok(Some(self.downloaded.0))
            } else {
                Ok(Some(self.frame.0))
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn jpeg_dimensions_are_bounded_before_native_decoding() {
        let packet = [
            255, 216, 255, 192, 0, 17, 8, 2, 208, 5, 0, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1, 255,
            217,
        ];
        assert_eq!(jpeg_header(&packet).unwrap(), (1280, 720, true));
        for length in 0..20 {
            assert!(jpeg_header(&packet[..length]).is_err());
        }
    }
}
