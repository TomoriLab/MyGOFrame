#[cfg(test)]
use crate::capture::domain::layout::MAX_FRAME_BYTES;
use crate::capture::domain::layout::{BUFFER_COUNT, MAX_DIMENSION, frame_layout};
use crate::capture::infrastructure::frame_pool::{FramePool, FrameSlot};
use crate::platform::infrastructure::notification::{self, NotificationSlot};
use crate::{ffi, make_c_string};
use std::collections::HashSet;
use std::ffi::{CStr, c_char, c_void};
use std::fs::{File, OpenOptions, read_dir};
use std::os::fd::AsRawFd;
use std::os::unix::fs::OpenOptionsExt;
use std::ptr;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread::{self, JoinHandle};
use std::time::Duration;

const SOURCE_PADDING: usize = 64;

const CAPTURE_TYPE: u32 = ffi::v4l2_buf_type_V4L2_BUF_TYPE_VIDEO_CAPTURE;
const MMAP_MEMORY: u32 = ffi::v4l2_memory_V4L2_MEMORY_MMAP;
const O_NONBLOCK: i32 = 0o4000;
const PROT_READ_WRITE: i32 = 3;
const MAP_SHARED: i32 = 1;
const POLLIN: i16 = 1;
const POLL_FAILURE: i16 = 0x38;
const EAGAIN: i32 = 11;
const EINTR: i32 = 4;
const EINVAL: i32 = 22;
const AVERROR_INVALIDDATA: i32 = -1_094_995_529;
const AVERROR_EOF: i32 = -541_478_725;

const fn fourcc(code: &[u8; 4]) -> u32 {
    (code[0] as u32) | ((code[1] as u32) << 8) | ((code[2] as u32) << 16) | ((code[3] as u32) << 24)
}

const YUYV: u32 = fourcc(b"YUYV");
const UYVY: u32 = fourcc(b"UYVY");
const NV12: u32 = fourcc(b"NV12");
const P010: u32 = fourcc(b"P010");
const BGR24: u32 = fourcc(b"BGR3");
const RGB24: u32 = fourcc(b"RGB3");
const MJPEG: u32 = fourcc(b"MJPG");

const fn ioctl_request(direction: u64, number: u64, size: usize) -> u64 {
    (direction << 30) | ((size as u64) << 16) | ((b'V' as u64) << 8) | number
}

const fn read_ioctl<T>(number: u64) -> u64 {
    ioctl_request(2, number, std::mem::size_of::<T>())
}

pub(crate) const fn write_ioctl<T>(number: u64) -> u64 {
    ioctl_request(1, number, std::mem::size_of::<T>())
}

pub(crate) const fn read_write_ioctl<T>(number: u64) -> u64 {
    ioctl_request(3, number, std::mem::size_of::<T>())
}

const QUERY_CAPABILITY: u64 = read_ioctl::<ffi::v4l2_capability>(0);
const ENUMERATE_FORMAT: u64 = read_write_ioctl::<ffi::v4l2_fmtdesc>(2);
const SET_FORMAT: u64 = read_write_ioctl::<ffi::v4l2_format>(5);
const REQUEST_BUFFERS: u64 = read_write_ioctl::<ffi::v4l2_requestbuffers>(8);
const QUERY_BUFFER: u64 = read_write_ioctl::<ffi::v4l2_buffer>(9);
const QUEUE_BUFFER: u64 = read_write_ioctl::<ffi::v4l2_buffer>(15);
const DEQUEUE_BUFFER: u64 = read_write_ioctl::<ffi::v4l2_buffer>(17);
const STREAM_ON: u64 = write_ioctl::<i32>(18);
const STREAM_OFF: u64 = write_ioctl::<i32>(19);
const GET_PARAMETERS: u64 = read_write_ioctl::<ffi::v4l2_streamparm>(21);
const SET_PARAMETERS: u64 = read_write_ioctl::<ffi::v4l2_streamparm>(22);
const ENUMERATE_SIZES: u64 = read_write_ioctl::<ffi::v4l2_frmsizeenum>(74);
const ENUMERATE_INTERVALS: u64 = read_write_ioctl::<ffi::v4l2_frmivalenum>(75);

pub(crate) fn last_os_error(operation: &str) -> String {
    format!("{operation}: {}", std::io::Error::last_os_error())
}

pub(crate) fn checked_ioctl<T>(
    descriptor: i32,
    request: u64,
    value: &mut T,
    operation: &str,
) -> Result<(), String> {
    loop {
        // Linux ioctl receives the address of a C-layout V4L2 structure.
        let result = unsafe { ffi::ioctl(descriptor, request, value as *mut T) };
        if result >= 0 {
            return Ok(());
        }
        if std::io::Error::last_os_error().raw_os_error() != Some(EINTR) {
            return Err(last_os_error(operation));
        }
    }
}

pub(crate) fn try_ioctl<T>(descriptor: i32, request: u64, value: &mut T) -> Result<(), i32> {
    loop {
        let result = unsafe { ffi::ioctl(descriptor, request, value as *mut T) };
        if result >= 0 {
            return Ok(());
        }
        let error = std::io::Error::last_os_error().raw_os_error().unwrap_or(-1);
        if error != EINTR {
            return Err(error);
        }
    }
}

pub(crate) struct VideoFile {
    file: File,
    name: String,
}

impl VideoFile {
    pub(crate) fn open(path: &str) -> Result<Self, String> {
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .custom_flags(O_NONBLOCK)
            .open(path)
            .map_err(|error| format!("Open capture device: {error}"))?;
        // The C layout permits zero initialization before ioctl fills the fields.
        let mut capability: ffi::v4l2_capability = unsafe { std::mem::zeroed() };
        checked_ioctl(
            file.as_raw_fd(),
            QUERY_CAPABILITY,
            &mut capability,
            "Read capture capabilities",
        )?;
        let flags = if capability.capabilities & ffi::V4L2_CAP_DEVICE_CAPS != 0 {
            capability.device_caps
        } else {
            capability.capabilities
        };
        if flags & ffi::V4L2_CAP_VIDEO_CAPTURE == 0 || flags & ffi::V4L2_CAP_STREAMING == 0 {
            return Err(
                "The device does not support single-planar streaming video capture.".into(),
            );
        }
        let name_end = capability
            .card
            .iter()
            .position(|byte| *byte == 0)
            .unwrap_or(capability.card.len());
        let name = String::from_utf8_lossy(&capability.card[..name_end]).into_owned();
        Ok(Self { file, name })
    }

    pub(crate) fn descriptor(&self) -> i32 {
        self.file.as_raw_fd()
    }
}

pub(crate) struct MappedBuffer {
    pub(crate) pointer: *mut u8,
    pub(crate) length: usize,
}

// A dequeued V4L2 buffer is moved to one capture thread and unmapped after that thread stops.
unsafe impl Send for MappedBuffer {}

impl Drop for MappedBuffer {
    fn drop(&mut self) {
        if !self.pointer.is_null() {
            unsafe { ffi::munmap(self.pointer.cast(), self.length) };
        }
    }
}

struct VideoStatus {
    recording: Arc<crate::media::infrastructure::packets::VideoTap>,
    notification: NotificationSlot,
    running: AtomicBool,
    stop: AtomicBool,
    frames: AtomicU64,
    dropped_frames: AtomicU64,
    error: Mutex<String>,
}

pub struct VideoEngine {
    pool: Arc<FramePool>,
    status: Arc<VideoStatus>,
    worker: Option<JoinHandle<()>>,
}

impl Drop for VideoEngine {
    fn drop(&mut self) {
        self.status.stop.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

#[derive(Clone, Copy)]
struct PixelConfiguration {
    fourcc: u32,
    width: u32,
    height: u32,
    bytes_per_line: u32,
    quantization: u32,
    encoding: u32,
    colorspace: u32,
    color_range: i32,
}

fn pixel_format(fourcc: u32) -> Option<ffi::AVPixelFormat> {
    match fourcc {
        YUYV => Some(ffi::AVPixelFormat_AV_PIX_FMT_YUYV422),
        UYVY => Some(ffi::AVPixelFormat_AV_PIX_FMT_UYVY422),
        NV12 => Some(ffi::AVPixelFormat_AV_PIX_FMT_NV12),
        P010 => Some(ffi::AVPixelFormat_AV_PIX_FMT_P010LE),
        BGR24 => Some(ffi::AVPixelFormat_AV_PIX_FMT_BGR24),
        RGB24 => Some(ffi::AVPixelFormat_AV_PIX_FMT_RGB24),
        _ => None,
    }
}

fn supports_format(fourcc: u32) -> bool {
    fourcc == MJPEG || pixel_format(fourcc).is_some()
}

fn ffmpeg_error(code: i32) -> String {
    let mut text = [0_i8; 128];
    let result = unsafe { ffi::av_strerror(code, text.as_mut_ptr(), text.len()) };
    if result < 0 {
        return format!("FFmpeg error {code}");
    }
    unsafe { CStr::from_ptr(text.as_ptr()) }
        .to_string_lossy()
        .into_owned()
}

struct PaddedInput {
    pointer: *mut u8,
    capacity: usize,
}

impl Drop for PaddedInput {
    fn drop(&mut self) {
        unsafe { ffi::av_free(self.pointer.cast()) };
    }
}

struct PixelConverter {
    configuration: PixelConfiguration,
    format: ffi::AVPixelFormat,
    scaler: *mut ffi::SwsContext,
    decoder: *mut ffi::AVCodecContext,
    frame: *mut ffi::AVFrame,
    packet: *mut ffi::AVPacket,
    padded_input: Option<PaddedInput>,
}

impl Drop for PixelConverter {
    fn drop(&mut self) {
        unsafe {
            ffi::sws_freeContext(self.scaler);
            ffi::avcodec_free_context(&mut self.decoder);
            ffi::av_frame_free(&mut self.frame);
            ffi::av_packet_free(&mut self.packet);
        }
    }
}

enum Conversion {
    Converted,
    Discarded(String),
}

impl PixelConverter {
    fn new(configuration: PixelConfiguration) -> Result<Self, String> {
        if configuration.width == 0
            || configuration.height == 0
            || configuration.width > MAX_DIMENSION
            || configuration.height > MAX_DIMENSION
            || !(0..=2).contains(&configuration.color_range)
            || !supports_format(configuration.fourcc)
        {
            return Err("Invalid pixel conversion configuration.".into());
        }
        let mut converter = Self {
            configuration,
            format: pixel_format(configuration.fourcc)
                .unwrap_or(ffi::AVPixelFormat_AV_PIX_FMT_NONE),
            scaler: ptr::null_mut(),
            decoder: ptr::null_mut(),
            frame: ptr::null_mut(),
            packet: ptr::null_mut(),
            padded_input: None,
        };
        if configuration.fourcc == MJPEG {
            let codec = unsafe { ffi::avcodec_find_decoder(ffi::AVCodecID_AV_CODEC_ID_MJPEG) };
            if codec.is_null() {
                return Err("The FFmpeg MJPEG decoder is unavailable.".into());
            }
            converter.decoder = unsafe { ffi::avcodec_alloc_context3(codec) };
            converter.frame = unsafe { ffi::av_frame_alloc() };
            converter.packet = unsafe { ffi::av_packet_alloc() };
            if converter.decoder.is_null()
                || converter.frame.is_null()
                || converter.packet.is_null()
            {
                return Err("Unable to allocate the MJPEG decoder.".into());
            }
            unsafe {
                (*converter.decoder).thread_count = 1;
                (*converter.decoder).flags |= ffi::AV_CODEC_FLAG_LOW_DELAY as i32;
                (*converter.decoder).max_pixels =
                    i64::from(configuration.width) * i64::from(configuration.height);
            }
            let result = unsafe { ffi::avcodec_open2(converter.decoder, codec, ptr::null_mut()) };
            if result < 0 {
                return Err(format!("Open MJPEG decoder: {}", ffmpeg_error(result)));
            }
        }
        Ok(converter)
    }

    fn padded_source(&mut self, source: &[u8], required: usize) -> Result<*const u8, String> {
        if source.len() - required >= SOURCE_PADDING {
            return Ok(source.as_ptr());
        }
        let allocation_size = required
            .checked_add(SOURCE_PADDING)
            .ok_or("Video frame length overflowed.")?;
        if self
            .padded_input
            .as_ref()
            .is_none_or(|storage| storage.capacity < allocation_size)
        {
            let allocation = unsafe { ffi::av_malloc(allocation_size) }.cast::<u8>();
            if allocation.is_null() {
                return Err("Unable to allocate a padded raw video frame.".into());
            }
            self.padded_input = Some(PaddedInput {
                pointer: allocation,
                capacity: allocation_size,
            });
        }
        let pointer = self.padded_input.as_ref().unwrap().pointer;
        unsafe {
            ptr::copy_nonoverlapping(source.as_ptr(), pointer, required);
            ptr::write_bytes(pointer.add(required), 0, SOURCE_PADDING);
        }
        Ok(pointer)
    }

    fn convert(
        &mut self,
        source: &[u8],
        used: usize,
        destination: &mut [u8],
    ) -> Result<Conversion, String> {
        let configuration = self.configuration;
        let width = configuration.width as usize;
        let height = configuration.height as usize;
        let required_output = width
            .checked_mul(height)
            .and_then(|count| count.checked_mul(4))
            .ok_or("RGBA destination length overflowed.")?;
        if destination.len() < required_output || used > source.len() {
            return Err("RGBA destination is too small or the input length is invalid.".into());
        }
        let mut format = self.format;
        let is_rgb = matches!(
            format,
            ffi::AVPixelFormat_AV_PIX_FMT_RGB24 | ffi::AVPixelFormat_AV_PIX_FMT_BGR24
        );
        let mut full_range =
            configuration.quantization == ffi::v4l2_quantization_V4L2_QUANTIZATION_FULL_RANGE;
        let mut encoding = if configuration.encoding != 0 {
            configuration.encoding
        } else {
            match configuration.colorspace {
                ffi::v4l2_colorspace_V4L2_COLORSPACE_REC709
                | ffi::v4l2_colorspace_V4L2_COLORSPACE_DCI_P3 => {
                    ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_709
                }
                ffi::v4l2_colorspace_V4L2_COLORSPACE_BT2020 => {
                    ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_BT2020
                }
                ffi::v4l2_colorspace_V4L2_COLORSPACE_SMPTE240M => {
                    ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_SMPTE240M
                }
                _ => ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_601,
            }
        };
        if configuration.quantization == 0 {
            full_range =
                is_rgb || configuration.colorspace == ffi::v4l2_colorspace_V4L2_COLORSPACE_JPEG;
        }

        let mut planes = [ptr::null(); 4];
        let mut strides = [0_i32; 4];
        if !self.decoder.is_null() {
            if used > i32::MAX as usize {
                return Err("MJPEG packet length exceeds the decoder limit.".into());
            }
            if used < 2 || source[0] != 0xff || source[1] != 0xd8 {
                return Ok(Conversion::Discarded(
                    "MJPEG packet is missing its start-of-image marker.".into(),
                ));
            }
            unsafe { ffi::av_packet_unref(self.packet) };
            if unsafe { ffi::av_new_packet(self.packet, used as i32) } < 0 {
                return Err("Unable to allocate an MJPEG packet.".into());
            }
            // FFmpeg allocates and clears the compressed packet's required padding.
            unsafe { ptr::copy_nonoverlapping(source.as_ptr(), (*self.packet).data, used) };
            unsafe { ffi::avcodec_flush_buffers(self.decoder) };
            let submitted = unsafe { ffi::avcodec_send_packet(self.decoder, self.packet) };
            if submitted < 0 {
                return decoder_failure("Submit MJPEG packet", submitted);
            }
            let decoded = unsafe { ffi::avcodec_receive_frame(self.decoder, self.frame) };
            if decoded < 0 {
                return decoder_failure("Decode MJPEG frame", decoded);
            }
            let frame = unsafe { &*self.frame };
            if frame.width != configuration.width as i32
                || frame.height != configuration.height as i32
            {
                return Err("MJPEG dimensions changed; restart capture.".into());
            }
            format = frame.format;
            for index in 0..4 {
                planes[index] = frame.data[index].cast_const();
                strides[index] = frame.linesize[index];
            }
            full_range = frame.color_range != ffi::AVColorRange_AVCOL_RANGE_MPEG;
            format = match format {
                ffi::AVPixelFormat_AV_PIX_FMT_YUVJ420P => ffi::AVPixelFormat_AV_PIX_FMT_YUV420P,
                ffi::AVPixelFormat_AV_PIX_FMT_YUVJ422P => ffi::AVPixelFormat_AV_PIX_FMT_YUV422P,
                ffi::AVPixelFormat_AV_PIX_FMT_YUVJ444P => ffi::AVPixelFormat_AV_PIX_FMT_YUV444P,
                ffi::AVPixelFormat_AV_PIX_FMT_YUVJ440P => ffi::AVPixelFormat_AV_PIX_FMT_YUV440P,
                ffi::AVPixelFormat_AV_PIX_FMT_YUVJ411P => ffi::AVPixelFormat_AV_PIX_FMT_YUV411P,
                _ => format,
            };
            if frame.colorspace == ffi::AVColorSpace_AVCOL_SPC_BT709 {
                encoding = ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_709;
            } else if frame.colorspace == ffi::AVColorSpace_AVCOL_SPC_BT2020_NCL {
                encoding = ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_BT2020;
            }
        } else {
            let planar = matches!(
                format,
                ffi::AVPixelFormat_AV_PIX_FMT_NV12 | ffi::AVPixelFormat_AV_PIX_FMT_P010LE
            );
            let minimum_stride = width
                * if is_rgb {
                    3
                } else if format == ffi::AVPixelFormat_AV_PIX_FMT_NV12 {
                    1
                } else {
                    2
                };
            let row_stride = if configuration.bytes_per_line == 0 {
                minimum_stride
            } else {
                configuration.bytes_per_line as usize
            };
            if configuration.bytes_per_line > i32::MAX as u32
                || row_stride < minimum_stride
                || (planar && (width | height) & 1 != 0)
                || (!is_rgb && width & 1 != 0)
            {
                return Err("Invalid raw frame dimensions or stride.".into());
            }
            let first_plane_size = row_stride
                .checked_mul(height)
                .ok_or("Raw video length overflowed.")?;
            let required = first_plane_size
                .checked_add(if planar { row_stride * (height / 2) } else { 0 })
                .ok_or("Raw video length overflowed.")?;
            if used < required {
                return Ok(Conversion::Discarded("Truncated raw video frame.".into()));
            }
            let input = self.padded_source(source, required)?;
            planes[0] = input;
            strides[0] = row_stride as i32;
            if planar {
                planes[1] = unsafe { input.add(first_plane_size) };
                strides[1] = row_stride as i32;
            }
        }
        match configuration.color_range {
            1 => full_range = false,
            2 => full_range = true,
            _ => {}
        }
        let matrix = match encoding {
            ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_709
            | ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_XV709 => ffi::SWS_CS_ITU709,
            ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_BT2020 => ffi::SWS_CS_BT2020,
            ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_SMPTE240M => ffi::SWS_CS_SMPTE240M,
            ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_BT2020_CONST_LUM => {
                return Err("BT.2020 constant-luminance conversion is unsupported.".into());
            }
            _ => ffi::SWS_CS_ITU601,
        };
        self.scaler = unsafe {
            ffi::sws_getCachedContext(
                self.scaler,
                configuration.width as i32,
                configuration.height as i32,
                format,
                configuration.width as i32,
                configuration.height as i32,
                ffi::AVPixelFormat_AV_PIX_FMT_RGBA,
                1,
                ptr::null_mut(),
                ptr::null_mut(),
                ptr::null(),
            )
        };
        if self.scaler.is_null() {
            return Err("Unable to allocate the color converter.".into());
        }
        let coefficients = unsafe { ffi::sws_getCoefficients(matrix as i32) };
        if coefficients.is_null()
            || unsafe {
                ffi::sws_setColorspaceDetails(
                    self.scaler,
                    coefficients,
                    i32::from(full_range),
                    coefficients,
                    1,
                    0,
                    1 << 16,
                    1 << 16,
                )
            } < 0
        {
            return Err("Unable to configure color range conversion.".into());
        }
        let output_planes = [
            destination.as_mut_ptr(),
            ptr::null_mut(),
            ptr::null_mut(),
            ptr::null_mut(),
        ];
        let output_strides = [(width * 4) as i32, 0, 0, 0];
        let converted = unsafe {
            ffi::sws_scale(
                self.scaler,
                planes.as_ptr(),
                strides.as_ptr(),
                0,
                configuration.height as i32,
                output_planes.as_ptr(),
                output_strides.as_ptr(),
            )
        };
        if converted != configuration.height as i32 {
            return Err("Color conversion failed.".into());
        }
        if is_rgb && !full_range {
            for pixel in destination[..required_output].as_chunks_mut::<4>().0 {
                for channel in pixel.iter_mut().take(3) {
                    let scaled = ((i32::from(*channel) - 16) * 255 + 109) / 219;
                    *channel = scaled.clamp(0, 255) as u8;
                }
            }
        }
        Ok(Conversion::Converted)
    }
}

pub(crate) fn dma_reference_rgba(
    view: &ffi::mgfr_dma_frame_view,
    source: &[u8],
    destination: &mut [u8],
) -> Result<(), String> {
    let mut converter = PixelConverter::new(PixelConfiguration {
        fourcc: view.fourcc,
        width: view.width,
        height: view.height,
        bytes_per_line: view.stride,
        quantization: 0,
        encoding: view.encoding,
        colorspace: view.colorspace,
        color_range: view.color_range,
    })?;
    match converter.convert(source, view.byte_length, destination)? {
        Conversion::Converted => Ok(()),
        Conversion::Discarded(message) => Err(message),
    }
}

fn decoder_failure(operation: &str, code: i32) -> Result<Conversion, String> {
    let message = format!("{operation}: {}", ffmpeg_error(code));
    if code == AVERROR_INVALIDDATA || code == -EAGAIN || code == AVERROR_EOF {
        Ok(Conversion::Discarded(message))
    } else {
        Err(message)
    }
}

pub(crate) fn capture_buffer(index: u32) -> ffi::v4l2_buffer {
    let mut buffer: ffi::v4l2_buffer = unsafe { std::mem::zeroed() };
    buffer.index = index;
    buffer.type_ = CAPTURE_TYPE;
    buffer.memory = MMAP_MEMORY;
    buffer
}

struct FrameMetadata {
    timestamp: u64,
    dropped_frames: u64,
}

struct VideoCapture {
    file: VideoFile,
    buffers: Vec<MappedBuffer>,
    converter: PixelConverter,
    info: ffi::mgfr_video_info,
    dropped_frames: u64,
    consecutive_invalid_frames: u32,
    streaming: bool,
    recording: Arc<crate::media::infrastructure::packets::VideoTap>,
}

// Capture and its native allocations move to exactly one worker thread.
unsafe impl Send for VideoCapture {}

impl Drop for VideoCapture {
    fn drop(&mut self) {
        if self.streaming {
            let mut kind = CAPTURE_TYPE as i32;
            let _ = try_ioctl(self.file.descriptor(), STREAM_OFF, &mut kind);
        }
    }
}

impl VideoCapture {
    fn new(configuration: &ffi::mgfr_video_config) -> Result<Self, String> {
        if configuration.path.is_null()
            || configuration.interval_numerator == 0
            || configuration.interval_denominator == 0
            || !(0..=2).contains(&configuration.color_range)
            || !supports_format(configuration.fourcc)
        {
            return Err("Invalid capture configuration.".into());
        }
        if frame_layout(configuration.width, configuration.height).is_none() {
            return Err("Requested video dimensions exceed the frame pool limits.".into());
        }
        let path = unsafe { CStr::from_ptr(configuration.path) }.to_string_lossy();
        let file = VideoFile::open(&path)?;
        let descriptor = file.descriptor();
        let mut format: ffi::v4l2_format = unsafe { std::mem::zeroed() };
        format.type_ = CAPTURE_TYPE;
        let mut requested_pixels: ffi::v4l2_pix_format = unsafe { std::mem::zeroed() };
        requested_pixels.width = configuration.width;
        requested_pixels.height = configuration.height;
        requested_pixels.pixelformat = configuration.fourcc;
        requested_pixels.field = ffi::v4l2_field_V4L2_FIELD_NONE;
        format.fmt.pix = requested_pixels;
        checked_ioctl(descriptor, SET_FORMAT, &mut format, "Set video format")?;
        let pixels = unsafe { format.fmt.pix };
        if frame_layout(pixels.width, pixels.height).is_none() {
            return Err("Negotiated video dimensions exceed the frame pool limits.".into());
        }
        if pixels.field != ffi::v4l2_field_V4L2_FIELD_NONE {
            return Err(
                "Interlaced capture is unsupported; select a progressive input mode.".into(),
            );
        }
        let mut parameters: ffi::v4l2_streamparm = unsafe { std::mem::zeroed() };
        parameters.type_ = CAPTURE_TYPE;
        checked_ioctl(
            descriptor,
            GET_PARAMETERS,
            &mut parameters,
            "Read frame interval",
        )?;
        let mut capture_parameters = unsafe { parameters.parm.capture };
        if capture_parameters.capability & ffi::V4L2_CAP_TIMEPERFRAME != 0 {
            capture_parameters.timeperframe.numerator = configuration.interval_numerator;
            capture_parameters.timeperframe.denominator = configuration.interval_denominator;
            parameters.parm.capture = capture_parameters;
            checked_ioctl(
                descriptor,
                SET_PARAMETERS,
                &mut parameters,
                "Set frame interval",
            )?;
            capture_parameters = unsafe { parameters.parm.capture };
        }
        if capture_parameters.timeperframe.numerator == 0
            || capture_parameters.timeperframe.denominator == 0
        {
            return Err("The capture device did not report a valid frame interval.".into());
        }
        let converter = PixelConverter::new(PixelConfiguration {
            fourcc: pixels.pixelformat,
            width: pixels.width,
            height: pixels.height,
            bytes_per_line: pixels.bytesperline,
            quantization: pixels.quantization,
            encoding: unsafe { pixels.__bindgen_anon_1.ycbcr_enc },
            colorspace: pixels.colorspace,
            color_range: configuration.color_range,
        })?;
        let mut request: ffi::v4l2_requestbuffers = unsafe { std::mem::zeroed() };
        request.count = BUFFER_COUNT as u32;
        request.type_ = CAPTURE_TYPE;
        request.memory = MMAP_MEMORY;
        checked_ioctl(
            descriptor,
            REQUEST_BUFFERS,
            &mut request,
            "Allocate capture buffers",
        )?;
        if !(2..=64).contains(&request.count) {
            return Err(format!(
                "Unsupported capture buffer count: {}",
                request.count
            ));
        }
        let mut buffers = Vec::with_capacity(request.count as usize);
        for index in 0..request.count {
            let mut buffer = capture_buffer(index);
            checked_ioctl(
                descriptor,
                QUERY_BUFFER,
                &mut buffer,
                "Query capture buffer",
            )?;
            if buffer.length == 0 {
                return Err("The driver returned a zero-length capture buffer.".into());
            }
            let offset = unsafe { buffer.m.offset } as i64;
            let address = unsafe {
                ffi::mmap(
                    ptr::null_mut(),
                    buffer.length as usize,
                    PROT_READ_WRITE,
                    MAP_SHARED,
                    descriptor,
                    offset,
                )
            };
            if address as isize == -1 || address.is_null() {
                return Err(last_os_error("Map capture buffer"));
            }
            buffers.push(MappedBuffer {
                pointer: address.cast(),
                length: buffer.length as usize,
            });
            checked_ioctl(
                descriptor,
                QUEUE_BUFFER,
                &mut buffer,
                "Queue capture buffer",
            )?;
        }
        let mut kind = CAPTURE_TYPE as i32;
        checked_ioctl(descriptor, STREAM_ON, &mut kind, "Start capture")?;
        let info = ffi::mgfr_video_info {
            fourcc: pixels.pixelformat,
            width: pixels.width,
            height: pixels.height,
            stride: pixels.width.checked_mul(4).ok_or("Invalid RGBA stride.")?,
            interval_numerator: capture_parameters.timeperframe.numerator,
            interval_denominator: capture_parameters.timeperframe.denominator,
            bytes_per_line: pixels.bytesperline,
            quantization: pixels.quantization,
            encoding: unsafe { pixels.__bindgen_anon_1.ycbcr_enc },
            colorspace: pixels.colorspace,
            transfer_function: pixels.xfer_func,
            capture_buffers: request.count,
        };
        Ok(Self {
            file,
            buffers,
            converter,
            info,
            dropped_frames: 0,
            consecutive_invalid_frames: 0,
            streaming: true,
            recording: Arc::new(crate::media::infrastructure::packets::VideoTap::default()),
        })
    }

    fn requeue(&self, index: u32) -> Result<(), String> {
        let mut buffer = capture_buffer(index);
        checked_ioctl(
            self.file.descriptor(),
            QUEUE_BUFFER,
            &mut buffer,
            "Queue capture buffer",
        )
    }

    fn read_into(&mut self, destination: &mut [u8]) -> Result<Option<FrameMetadata>, String> {
        let mut descriptor = ffi::pollfd {
            fd: self.file.descriptor(),
            events: POLLIN,
            revents: 0,
        };
        let ready = unsafe { ffi::poll(&mut descriptor, 1, 50) };
        if ready < 0 {
            if std::io::Error::last_os_error().raw_os_error() == Some(EINTR) {
                return Ok(None);
            }
            return Err(last_os_error("Wait for video frame"));
        }
        if descriptor.revents & POLL_FAILURE != 0 {
            return Err("Capture device disconnected or reported a stream error.".into());
        }
        if ready == 0 || descriptor.revents & POLLIN == 0 {
            return Ok(None);
        }
        let mut latest: Option<ffi::v4l2_buffer> = None;
        let result = (|| {
            for _ in 0..self.buffers.len() {
                let mut buffer = capture_buffer(0);
                match try_ioctl(self.file.descriptor(), DEQUEUE_BUFFER, &mut buffer) {
                    Ok(()) => {}
                    Err(EAGAIN) => break,
                    Err(_) => return Err(last_os_error("Dequeue capture buffer")),
                }
                if buffer.index as usize >= self.buffers.len()
                    || buffer.bytesused as usize > self.buffers[buffer.index as usize].length
                {
                    return Err("The driver returned invalid capture buffer bounds.".into());
                }
                if buffer.flags & ffi::V4L2_BUF_FLAG_ERROR != 0 {
                    self.requeue(buffer.index)?;
                    self.dropped_frames = self.dropped_frames.saturating_add(1);
                    continue;
                }
                let mapping = &self.buffers[buffer.index as usize];
                let bytes = unsafe {
                    std::slice::from_raw_parts(mapping.pointer, buffer.bytesused as usize)
                };
                self.recording.submit(
                    bytes,
                    crate::media::infrastructure::packets::video_timestamp(&buffer),
                );
                let previous = latest.replace(buffer);
                if let Some(previous) = previous {
                    self.requeue(previous.index)?;
                    self.dropped_frames = self.dropped_frames.saturating_add(1);
                }
            }
            let Some(buffer) = latest else {
                return Ok(None);
            };
            let mapping = &self.buffers[buffer.index as usize];
            // The driver has dequeued this mapping, so it cannot write it until requeue.
            let source = unsafe { std::slice::from_raw_parts(mapping.pointer, mapping.length) };
            if destination.is_empty() {
                return Ok(None);
            }
            let conversion =
                self.converter
                    .convert(source, buffer.bytesused as usize, destination)?;
            match conversion {
                Conversion::Discarded(reason) => {
                    self.dropped_frames = self.dropped_frames.saturating_add(1);
                    self.consecutive_invalid_frames += 1;
                    if self.consecutive_invalid_frames >= 120 {
                        return Err(format!(
                            "Capture returned 120 consecutive invalid frames: {reason}"
                        ));
                    }
                    Ok(None)
                }
                Conversion::Converted => {
                    let dropped_frames = self.dropped_frames;
                    self.dropped_frames = 0;
                    self.consecutive_invalid_frames = 0;
                    let timestamp = if buffer.flags & ffi::V4L2_BUF_FLAG_TIMESTAMP_MASK
                        == ffi::V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC
                        && buffer.timestamp.tv_sec >= 0
                        && (0..1_000_000).contains(&buffer.timestamp.tv_usec)
                    {
                        (buffer.timestamp.tv_sec as u64) * 1_000_000_000
                            + (buffer.timestamp.tv_usec as u64) * 1000
                    } else {
                        let mut current: ffi::timespec = unsafe { std::mem::zeroed() };
                        if unsafe { ffi::clock_gettime(1, &mut current) } != 0 {
                            return Err(last_os_error("Read monotonic timestamp"));
                        }
                        (current.tv_sec as u64) * 1_000_000_000 + current.tv_nsec as u64
                    };
                    Ok(Some(FrameMetadata {
                        timestamp,
                        dropped_frames,
                    }))
                }
            }
        })();
        if let Some(buffer) = latest {
            let requeued = self.requeue(buffer.index);
            if result.is_ok() {
                requeued?;
            }
        }
        result
    }
}

fn capture_worker(mut capture: VideoCapture, pool: Arc<FramePool>, status: Arc<VideoStatus>) {
    let mut sequence = 0_u64;
    while !status.stop.load(Ordering::Acquire) {
        let Some(mut reservation) = pool.reserve() else {
            if status.recording.is_active() {
                if let Err(error) = capture.read_into(&mut []) {
                    *status.error.lock().unwrap() = error;
                    break;
                }
            } else {
                thread::sleep(Duration::from_millis(1));
            }
            continue;
        };
        let result = capture.read_into(reservation.pixels());
        let dropped_frames = match &result {
            Ok(Some(metadata)) => metadata.dropped_frames,
            _ => std::mem::take(&mut capture.dropped_frames),
        };
        status
            .dropped_frames
            .fetch_add(dropped_frames, Ordering::Relaxed);
        match result {
            Ok(Some(metadata)) => {
                sequence = sequence.wrapping_add(1);
                status.frames.fetch_add(1, Ordering::Relaxed);
                reservation.publish(sequence, metadata.timestamp);
                status.notification.notify();
            }
            Ok(None) => {}
            Err(error) => {
                *status.error.lock().unwrap() = error;
                break;
            }
        }
    }
    status.running.store(false, Ordering::Release);
    status.notification.notify();
}

fn json_string(value: &str) -> String {
    let mut encoded = String::from("\"");
    for character in value.chars() {
        match character {
            '"' => encoded.push_str("\\\""),
            '\\' => encoded.push_str("\\\\"),
            '\n' => encoded.push_str("\\n"),
            '\r' => encoded.push_str("\\r"),
            '\t' => encoded.push_str("\\t"),
            character if character <= '\u{001f}' => {
                encoded.push_str(&format!("\\u{:04x}", character as u32))
            }
            character => encoded.push(character),
        }
    }
    encoded.push('"');
    encoded
}

fn json_error(error: &str) -> String {
    format!("{{\"error\":{}}}", json_string(error))
}

fn format_name(fourcc: u32) -> String {
    String::from_utf8_lossy(&fourcc.to_le_bytes()).into_owned()
}

fn video_devices_json() -> Result<String, String> {
    let mut paths = Vec::new();
    for entry in read_dir("/dev").map_err(|error| format!("List video devices: {error}"))? {
        let entry = entry.map_err(|error| format!("List video devices: {error}"))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if name.strip_prefix("video").is_some_and(|number| {
            !number.is_empty() && number.chars().all(|digit| digit.is_ascii_digit())
        }) {
            paths.push(format!("/dev/{name}"));
        }
    }
    paths.sort_by_key(|path| {
        path.trim_start_matches("/dev/video")
            .parse::<u64>()
            .unwrap_or(u64::MAX)
    });
    let mut devices = Vec::new();
    for path in paths {
        if let Ok(file) = VideoFile::open(&path) {
            devices.push(format!(
                "{{\"path\":{},\"name\":{}}}",
                json_string(&path),
                json_string(&file.name)
            ));
        }
    }
    Ok(format!("[{}]", devices.join(",")))
}

fn candidate_sizes(size: &ffi::v4l2_frmsizeenum) -> Vec<(u32, u32)> {
    if size.type_ == ffi::v4l2_frmsizetypes_V4L2_FRMSIZE_TYPE_DISCRETE {
        let discrete = unsafe { size.__bindgen_anon_1.discrete };
        return vec![(discrete.width, discrete.height)];
    }
    let stepwise = unsafe { size.__bindgen_anon_1.stepwise };
    let common = [
        (640, 480),
        (720, 480),
        (720, 576),
        (1280, 720),
        (1280, 800),
        (1920, 1080),
        (1920, 1200),
        (2560, 1080),
        (2560, 1440),
        (3440, 1440),
        (3840, 2160),
        (7680, 4320),
        (stepwise.min_width, stepwise.min_height),
        (stepwise.max_width, stepwise.max_height),
    ];
    let mut seen = HashSet::new();
    common
        .into_iter()
        .filter(|&(width, height)| {
            width > 0
                && height > 0
                && width <= MAX_DIMENSION
                && height <= MAX_DIMENSION
                && width >= stepwise.min_width
                && width <= stepwise.max_width
                && height >= stepwise.min_height
                && height <= stepwise.max_height
                && (size.type_ != ffi::v4l2_frmsizetypes_V4L2_FRMSIZE_TYPE_STEPWISE
                    || ((width - stepwise.min_width) % stepwise.step_width.max(1) == 0
                        && (height - stepwise.min_height) % stepwise.step_height.max(1) == 0))
                && seen.insert((width, height))
        })
        .collect()
}

fn candidate_intervals(interval: &ffi::v4l2_frmivalenum) -> Vec<(u32, u32)> {
    if interval.type_ == ffi::v4l2_frmivaltypes_V4L2_FRMIVAL_TYPE_DISCRETE {
        let discrete = unsafe { interval.__bindgen_anon_1.discrete };
        return if discrete.numerator > 0 && discrete.denominator > 0 {
            vec![(discrete.numerator, discrete.denominator)]
        } else {
            Vec::new()
        };
    }
    let stepwise = unsafe { interval.__bindgen_anon_1.stepwise };
    if stepwise.min.denominator == 0 || stepwise.max.denominator == 0 {
        return Vec::new();
    }
    let minimum = f64::from(stepwise.min.numerator) / f64::from(stepwise.min.denominator);
    let maximum = f64::from(stepwise.max.numerator) / f64::from(stepwise.max.denominator);
    let step = if stepwise.step.denominator == 0 {
        0.0
    } else {
        f64::from(stepwise.step.numerator) / f64::from(stepwise.step.denominator)
    };
    let common = [
        (1, 240),
        (1, 144),
        (1, 120),
        (1, 100),
        (1, 60),
        (1001, 60000),
        (1, 50),
        (1, 30),
        (1001, 30000),
        (1, 25),
        (1, 24),
        (1001, 24000),
        (stepwise.min.numerator, stepwise.min.denominator),
        (stepwise.max.numerator, stepwise.max.denominator),
    ];
    let mut seen = Vec::<f64>::new();
    common
        .into_iter()
        .filter(|&(numerator, denominator)| {
            if numerator == 0 || denominator == 0 {
                return false;
            }
            let value = f64::from(numerator) / f64::from(denominator);
            if value < minimum - 1e-10 || value > maximum + 1e-10 {
                return false;
            }
            if interval.type_ == ffi::v4l2_frmivaltypes_V4L2_FRMIVAL_TYPE_STEPWISE && step > 0.0 {
                let steps = (value - minimum) / step;
                if (steps - steps.round()).abs() >= 1e-7 {
                    return false;
                }
            }
            if seen.iter().any(|previous| (previous - value).abs() < 1e-10) {
                return false;
            }
            seen.push(value);
            true
        })
        .collect()
}

fn video_modes_json(path: &str) -> Result<String, String> {
    let file = VideoFile::open(path)?;
    let descriptor = file.descriptor();
    let mut modes = Vec::new();
    for format_index in 0..1024 {
        let mut format: ffi::v4l2_fmtdesc = unsafe { std::mem::zeroed() };
        format.index = format_index;
        format.type_ = CAPTURE_TYPE;
        match try_ioctl(descriptor, ENUMERATE_FORMAT, &mut format) {
            Ok(()) => {}
            Err(EINVAL) => break,
            Err(_) => return Err(last_os_error("Enumerate video formats")),
        }
        if !supports_format(format.pixelformat) {
            continue;
        }
        for size_index in 0..4096 {
            let mut size: ffi::v4l2_frmsizeenum = unsafe { std::mem::zeroed() };
            size.index = size_index;
            size.pixel_format = format.pixelformat;
            match try_ioctl(descriptor, ENUMERATE_SIZES, &mut size) {
                Ok(()) => {}
                Err(EINVAL) => break,
                Err(_) => return Err(last_os_error("Enumerate video sizes")),
            }
            for (width, height) in candidate_sizes(&size) {
                for interval_index in 0..4096 {
                    let mut interval: ffi::v4l2_frmivalenum = unsafe { std::mem::zeroed() };
                    interval.index = interval_index;
                    interval.pixel_format = format.pixelformat;
                    interval.width = width;
                    interval.height = height;
                    match try_ioctl(descriptor, ENUMERATE_INTERVALS, &mut interval) {
                        Ok(()) => {}
                        Err(EINVAL) => break,
                        Err(_) => return Err(last_os_error("Enumerate frame intervals")),
                    }
                    for (numerator, denominator) in candidate_intervals(&interval) {
                        modes.push(format!(
                            "{{\"fourcc\":{},\"format\":{},\"width\":{},\"height\":{},\"intervalNumerator\":{},\"intervalDenominator\":{}}}",
                            format.pixelformat,
                            json_string(&format_name(format.pixelformat)),
                            width,
                            height,
                            numerator,
                            denominator,
                        ));
                    }
                    if interval.type_ != ffi::v4l2_frmivaltypes_V4L2_FRMIVAL_TYPE_DISCRETE {
                        break;
                    }
                }
            }
            if size.type_ != ffi::v4l2_frmsizetypes_V4L2_FRMSIZE_TYPE_DISCRETE {
                break;
            }
        }
    }
    Ok(format!("[{}]", modes.join(",")))
}

fn write_error(pointer: *mut *mut c_char, message: String) {
    if !pointer.is_null() {
        unsafe { *pointer = make_c_string(message) };
    }
}

/// # Safety
/// `configuration` and `info` must point to valid C ABI structures. `error`, when non-null,
/// must point to writable pointer storage. The returned handle must be destroyed exactly once.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_video_create(
    configuration: *const ffi::mgfr_video_config,
    info: *mut ffi::mgfr_video_info,
    error: *mut *mut c_char,
) -> *mut c_void {
    if !error.is_null() {
        unsafe { *error = ptr::null_mut() };
    }
    if configuration.is_null() || info.is_null() {
        write_error(error, "Video configuration and output are required.".into());
        return ptr::null_mut();
    }
    let mut capture = match VideoCapture::new(unsafe { &*configuration }) {
        Ok(capture) => capture,
        Err(message) => {
            write_error(error, message);
            return ptr::null_mut();
        }
    };
    let pool = match FramePool::new(capture.info.width, capture.info.height) {
        Ok(pool) => Arc::new(pool),
        Err(message) => {
            write_error(error, message);
            return ptr::null_mut();
        }
    };
    let details = capture.info;
    let status = Arc::new(VideoStatus {
        recording: Arc::new(crate::media::infrastructure::packets::VideoTap::default()),
        notification: NotificationSlot::default(),
        running: AtomicBool::new(true),
        stop: AtomicBool::new(false),
        frames: AtomicU64::new(0),
        dropped_frames: AtomicU64::new(0),
        error: Mutex::new(String::new()),
    });
    capture.recording = Arc::clone(&status.recording);
    let worker_pool = Arc::clone(&pool);
    let worker_status = Arc::clone(&status);
    let worker = match thread::Builder::new()
        .name("mygoframe-video".into())
        .spawn(move || {
            capture_worker(capture, worker_pool, worker_status);
        }) {
        Ok(worker) => worker,
        Err(error_value) => {
            write_error(error, format!("Start capture thread: {error_value}"));
            return ptr::null_mut();
        }
    };
    unsafe { *info = details };
    Box::into_raw(Box::new(VideoEngine {
        pool,
        status,
        worker: Some(worker),
    }))
    .cast()
}

/// # Safety
/// `video` must be a live handle returned by `mgfr_video_create`, or null. A live handle may be
/// destroyed only once and must not be used concurrently with this call.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_video_destroy(video: *mut c_void) {
    if !video.is_null() {
        unsafe { drop(Box::from_raw(video.cast::<VideoEngine>())) };
    }
}

/// # Safety
/// `video` must be a live video handle and `frame` must point to writable frame storage. The
/// handle must remain live for the duration of the call. Each returned token must be released once.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_video_acquire(
    video: *mut c_void,
    after_sequence: u64,
    frame: *mut ffi::mgfr_frame_view,
) -> i32 {
    if video.is_null() || frame.is_null() {
        return 0;
    }
    let engine = unsafe { &*(video.cast::<VideoEngine>()) };
    let Some((slot, sequence, timestamp)) = engine.pool.acquire(after_sequence) else {
        return 0;
    };
    let pixels = slot.address();
    let token = Box::into_raw(Box::new(slot)).cast::<c_void>();
    unsafe {
        *frame = ffi::mgfr_frame_view {
            token,
            pixels,
            byte_length: engine.pool.byte_length,
            width: engine.pool.width,
            height: engine.pool.height,
            stride: engine.pool.stride,
            sequence,
            timestamp_ns: timestamp,
        };
    }
    1
}

/// # Safety
/// `token` must be an outstanding frame token returned by `mgfr_video_acquire`, or null. A token
/// must be released exactly once after its pixel pointer is no longer in use.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_video_release(token: *mut c_void) {
    if !token.is_null() {
        unsafe { drop(Box::from_raw(token.cast::<Arc<FrameSlot>>())) };
    }
}

/// # Safety
/// `video` must be a live video handle and `result` must point to writable status storage. The
/// handle must remain live for the duration of the call.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_video_status(
    video: *mut c_void,
    result: *mut ffi::mgfr_video_status_data,
) -> i32 {
    if video.is_null() || result.is_null() {
        return 0;
    }
    let engine = unsafe { &*(video.cast::<VideoEngine>()) };
    let mut status: ffi::mgfr_video_status_data = unsafe { std::mem::zeroed() };
    status.running = i32::from(engine.status.running.load(Ordering::Acquire));
    status.frames = engine.status.frames.load(Ordering::Relaxed);
    status.dropped_frames = engine.status.dropped_frames.load(Ordering::Relaxed);
    let error = engine.status.error.lock().unwrap();
    for (index, byte) in error
        .as_bytes()
        .iter()
        .copied()
        .take(status.error.len() - 1)
        .enumerate()
    {
        status.error[index] = byte as c_char;
    }
    unsafe { *result = status };
    1
}

#[unsafe(no_mangle)]
pub extern "C" fn mgfr_video_devices_json() -> *mut c_char {
    make_c_string(video_devices_json().unwrap_or_else(|message| json_error(&message)))
}

/// # Safety
/// `path` must point to a valid null-terminated string, or be null. The returned string must be
/// freed with `mgfr_string_free`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_video_modes_json(path: *const c_char) -> *mut c_char {
    if path.is_null() {
        return make_c_string(json_error("A video device path is required."));
    }
    let path = unsafe { CStr::from_ptr(path) }.to_string_lossy();
    make_c_string(video_modes_json(&path).unwrap_or_else(|message| json_error(&message)))
}

/// # Safety
/// Both handles must be live. The caller serializes control against destruction.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_video_set_notification(
    video: *mut c_void,
    notification: *mut c_void,
) {
    if video.is_null() || notification.is_null() {
        return;
    }
    let engine = unsafe { &*video.cast::<VideoEngine>() };
    engine
        .status
        .notification
        .attach(unsafe { notification::retain(notification) });
}

/// # Safety
/// The serialized control caller keeps the capture handle alive for this lookup.
pub(crate) unsafe fn recording_tap(
    handle: *mut c_void,
) -> Arc<crate::media::infrastructure::packets::VideoTap> {
    Arc::clone(&unsafe { &*handle.cast::<VideoEngine>() }.status.recording)
}

#[cfg(test)]
mod tests {
    use super::*;

    unsafe extern "C" {
        fn mprotect(address: *mut c_void, length: usize, protection: i32) -> i32;
        fn sysconf(name: i32) -> isize;
    }

    struct GuardedSource {
        mapping: *mut u8,
        length: usize,
        data: *const u8,
        data_length: usize,
    }

    impl GuardedSource {
        fn new(data_length: usize, tail: usize) -> Self {
            let page = unsafe { sysconf(30) } as usize;
            assert!(page > 0 && page.is_power_of_two());
            let allocated_length = data_length + tail;
            let pages = allocated_length.div_ceil(page);
            let length = (pages + 1) * page;
            let mapping =
                unsafe { ffi::mmap(ptr::null_mut(), length, PROT_READ_WRITE, 2 | 0x20, -1, 0) }
                    .cast::<u8>();
            assert_ne!(mapping as isize, -1);
            let guard = unsafe { mapping.add(pages * page) };
            assert_eq!(unsafe { mprotect(guard.cast(), page, 0) }, 0);
            let data = unsafe { guard.sub(allocated_length) };
            unsafe { ptr::write_bytes(data, 128, allocated_length) };
            Self {
                mapping,
                length,
                data,
                data_length: allocated_length,
            }
        }

        fn bytes(&self) -> &[u8] {
            unsafe { std::slice::from_raw_parts(self.data, self.data_length) }
        }
    }

    impl Drop for GuardedSource {
        fn drop(&mut self) {
            unsafe { ffi::munmap(self.mapping.cast(), self.length) };
        }
    }

    #[test]
    fn raw_conversion_stages_guarded_input_and_matches_direct_path() {
        for (fourcc, width, height, row_stride) in [
            (RGB24, 640_u32, 480_u32, 640_usize * 3),
            (YUYV, 18, 6, 18 * 2),
            (NV12, 18, 6, 18),
        ] {
            let required = row_stride * height as usize
                + if fourcc == NV12 {
                    row_stride * (height as usize / 2)
                } else {
                    0
                };
            let configuration = PixelConfiguration {
                fourcc,
                width,
                height,
                bytes_per_line: row_stride as u32,
                quantization: 0,
                encoding: 0,
                colorspace: 0,
                color_range: 2,
            };
            let exact = GuardedSource::new(required, 0);
            let padded = GuardedSource::new(required, SOURCE_PADDING);
            let mut converter = PixelConverter::new(configuration).unwrap();
            let mut staged_output = vec![0_u8; width as usize * height as usize * 4];
            let mut direct_output = staged_output.clone();
            assert!(matches!(
                converter
                    .convert(exact.bytes(), required, &mut staged_output)
                    .unwrap(),
                Conversion::Converted
            ));
            assert!(converter.padded_input.is_some());
            assert!(matches!(
                converter
                    .convert(padded.bytes(), required, &mut direct_output)
                    .unwrap(),
                Conversion::Converted
            ));
            assert_eq!(
                staged_output,
                direct_output,
                "conversion changed for {}",
                format_name(fourcc)
            );
        }
    }

    #[test]
    fn frame_memory_budget_rejects_oversized_requests_before_open() {
        assert_eq!(frame_layout(3840, 2160), Some((15_360, 33_177_600)));
        assert_eq!(frame_layout(7680, 4320), Some((30_720, 132_710_400)));
        assert_eq!(frame_layout(16_384, 2048), Some((65_536, MAX_FRAME_BYTES)));
        assert_eq!(frame_layout(16_384, 2049), None);
        assert_eq!(frame_layout(0, 2048), None);
        assert!(FramePool::new(16_384, 2049).is_err());

        let configuration = ffi::mgfr_video_config {
            path: c"/dev/mygoframe-nonexistent-memory-budget".as_ptr(),
            fourcc: NV12,
            width: 16_384,
            height: 2049,
            interval_numerator: 1,
            interval_denominator: 60,
            color_range: 0,
        };
        let error = VideoCapture::new(&configuration)
            .err()
            .expect("An oversized request must fail before opening the device.");
        assert_eq!(
            error,
            "Requested video dimensions exceed the frame pool limits."
        );
    }

    #[test]
    fn rejects_invalid_dimensions_and_truncated_raw_frames() {
        assert!(FramePool::new(16_385, 2).is_err());
        assert!(FramePool::new(16_384, 16_384).is_err());
        let configuration = PixelConfiguration {
            fourcc: NV12,
            width: 4,
            height: 2,
            bytes_per_line: 4,
            quantization: 0,
            encoding: 0,
            colorspace: 0,
            color_range: 2,
        };
        let mut converter = PixelConverter::new(configuration).unwrap();
        let source = vec![0_u8; 12];
        let mut output = vec![0_u8; 32];
        assert!(matches!(
            converter.convert(&source, 11, &mut output).unwrap(),
            Conversion::Discarded(_)
        ));
    }

    #[test]
    fn converts_supported_raw_formats_to_expected_rgba_channels() {
        let p010_words: [u16; 12] = [
            324 << 6,
            324 << 6,
            324 << 6,
            324 << 6,
            324 << 6,
            324 << 6,
            324 << 6,
            324 << 6,
            360 << 6,
            960 << 6,
            360 << 6,
            960 << 6,
        ];
        let p010: Vec<u8> = p010_words
            .iter()
            .flat_map(|sample| sample.to_le_bytes())
            .collect();
        let sources = [
            (YUYV, [81_u8, 90, 81, 240].repeat(4), 1),
            (UYVY, [90_u8, 81, 240, 81].repeat(4), 1),
            (
                NV12,
                [vec![81_u8; 8], [90_u8, 240, 90, 240].to_vec()].concat(),
                1,
            ),
            (P010, p010, 1),
            (RGB24, [255_u8, 0, 0].repeat(8), 2),
            (BGR24, [0_u8, 0, 255].repeat(8), 2),
        ];
        for (fourcc, source, range) in sources {
            let mut converter = PixelConverter::new(PixelConfiguration {
                fourcc,
                width: 4,
                height: 2,
                bytes_per_line: 0,
                quantization: 0,
                encoding: ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_601,
                colorspace: 0,
                color_range: range,
            })
            .unwrap();
            let mut output = [0_u8; 32];
            assert!(matches!(
                converter
                    .convert(&source, source.len(), &mut output)
                    .unwrap(),
                Conversion::Converted
            ));
            for pixel in output.as_chunks::<4>().0 {
                assert!(pixel[0] >= 250, "red channel in {}", format_name(fourcc));
                assert!(pixel[1] <= 4, "green channel in {}", format_name(fourcc));
                assert!(pixel[2] <= 4, "blue channel in {}", format_name(fourcc));
                assert_eq!(pixel[3], 255, "alpha channel in {}", format_name(fourcc));
            }
        }
    }

    #[test]
    fn malformed_mjpeg_is_discarded_without_persistent_decoder_failure() {
        let mut converter = PixelConverter::new(PixelConfiguration {
            fourcc: MJPEG,
            width: 32,
            height: 16,
            bytes_per_line: 0,
            quantization: 0,
            encoding: 0,
            colorspace: 0,
            color_range: 0,
        })
        .unwrap();
        let mut output = vec![0_u8; 32 * 16 * 4];
        assert!(matches!(
            converter.convert(&[1, 2, 3, 4], 4, &mut output).unwrap(),
            Conversion::Discarded(_)
        ));
        assert!(matches!(
            converter
                .convert(&[0xff, 0xd8, 0xff, 0xd9], 4, &mut output)
                .unwrap(),
            Conversion::Discarded(_)
        ));
    }
}
