use super::{Configuration, devices::Device};
use crate::{capture::domain::format::DmaFormat, ffi};
use std::ffi::{CStr, CString};
use std::ptr;

// Capture timestamps need sub-frame precision independently of the nominal encoder rate.
pub(super) const VIDEO_TIME_BASE: ffi::AVRational = ffi::AVRational {
    num: 1,
    den: 1_000_000,
};

pub(crate) fn checked(code: i32, operation: &str) -> Result<(), String> {
    if code >= 0 {
        return Ok(());
    }
    let mut bytes = [0_i8; 256];
    unsafe {
        ffi::av_strerror(code, bytes.as_mut_ptr(), bytes.len());
    }
    Err(format!(
        "{operation}: {}",
        unsafe { CStr::from_ptr(bytes.as_ptr()) }.to_string_lossy()
    ))
}

pub(crate) struct Codec(pub *mut ffi::AVCodecContext);
impl Drop for Codec {
    fn drop(&mut self) {
        unsafe {
            ffi::avcodec_free_context(&mut self.0);
        }
    }
}
pub(crate) struct Frame(pub *mut ffi::AVFrame);
impl Frame {
    pub fn new() -> Result<Self, String> {
        let value = unsafe { ffi::av_frame_alloc() };
        if value.is_null() {
            Err("Unable to allocate a recording frame.".into())
        } else {
            Ok(Self(value))
        }
    }
}
impl Drop for Frame {
    fn drop(&mut self) {
        unsafe {
            ffi::av_frame_free(&mut self.0);
        }
    }
}
pub(crate) struct Packet(pub *mut ffi::AVPacket);
impl Packet {
    pub fn new() -> Result<Self, String> {
        let value = unsafe { ffi::av_packet_alloc() };
        if value.is_null() {
            Err("Unable to allocate a recording packet.".into())
        } else {
            Ok(Self(value))
        }
    }
}
impl Drop for Packet {
    fn drop(&mut self) {
        unsafe {
            ffi::av_packet_free(&mut self.0);
        }
    }
}
pub(super) struct Buffer(pub(super) *mut ffi::AVBufferRef);
impl Drop for Buffer {
    fn drop(&mut self) {
        unsafe {
            ffi::av_buffer_unref(&mut self.0);
        }
    }
}

fn option(context: &Codec, key: &CStr, value: &str) -> Result<(), String> {
    let text = CString::new(value).map_err(|_| "Invalid recording option.")?;
    checked(
        unsafe { ffi::av_opt_set((*context.0).priv_data, key.as_ptr(), text.as_ptr(), 0) },
        "Configure recording encoder",
    )
}

pub(crate) struct VideoEncoder {
    pub context: Codec,
    pub frame: Frame,
    hardware: Option<Frame>,
    cuda_experiment: bool,
    experiment_mode: String,
    direct_import: bool,
    vulkan: Option<super::vulkan_upload::VulkanUpload>,
    transfer_name: String,
    vulkan_disabled: bool,
    scaler: *mut ffi::SwsContext,
    source_format: ffi::AVPixelFormat,
    source_stride: i32,
    required_bytes: usize,
    uv_offset: usize,
    height: i32,
    pub last_pts: i64,
    decoder: Option<super::decoder::Decoder>,
    source_full: bool,
    rec601: bool,
    normalized_rgb: Vec<u8>,
}
impl Drop for VideoEncoder {
    fn drop(&mut self) {
        unsafe {
            ffi::sws_freeContext(self.scaler);
        }
    }
}

impl VideoEncoder {
    pub fn new(config: &Configuration, device: &Device) -> Result<Self, String> {
        let experiment_mode = std::env::var("MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER")
            .unwrap_or_else(|_| {
                if std::env::var("MYGOFRAME_EXPERIMENTAL_CUDA_RECORDING").as_deref() == Ok("1") {
                    "cuda".into()
                } else {
                    "off".into()
                }
            });
        Self::with_transfer_mode(config, device, experiment_mode)
    }

    pub fn probe(config: &Configuration, device: &Device) -> Result<Self, String> {
        // Codec discovery must not create transfer devices whose teardown stalls active recording.
        Self::with_transfer_mode(config, device, "off".into())
    }

    fn with_transfer_mode(
        config: &Configuration,
        device: &Device,
        experiment_mode: String,
    ) -> Result<Self, String> {
        super::runtime::prepare(&device.backend)?;
        if !["off", "auto", "cuda", "vulkan"].contains(&experiment_mode.as_str()) {
            return Err(
                "Experimental recording transfer must be off, auto, cuda, or vulkan.".into(),
            );
        }
        let cuda_experiment = experiment_mode != "off";
        if cuda_experiment
            && (device.backend != "nvenc" || config.video.fourcc != u32::from_le_bytes(*b"NV12"))
        {
            return Err("Experimental CUDA recording requires NVIDIA NVENC and NV12 input.".into());
        }
        let compressed = config.video.fourcc == u32::from_le_bytes(*b"MJPG");
        let source = if compressed {
            DmaFormat::Nv12
        } else {
            DmaFormat::from_fourcc(config.video.fourcc)?
        };
        let stride = if compressed {
            config.video.width
        } else {
            config.video.bytes_per_line
        };
        let layout = source.layout(config.video.width, config.video.height, stride)?;
        let source_format = match source {
            DmaFormat::Nv12 => ffi::AVPixelFormat_AV_PIX_FMT_NV12,
            DmaFormat::P010 => ffi::AVPixelFormat_AV_PIX_FMT_P010LE,
            DmaFormat::Yuyv => ffi::AVPixelFormat_AV_PIX_FMT_YUYV422,
            DmaFormat::Uyvy => ffi::AVPixelFormat_AV_PIX_FMT_UYVY422,
            DmaFormat::Bgr24 => ffi::AVPixelFormat_AV_PIX_FMT_BGR24,
            DmaFormat::Rgb24 => ffi::AVPixelFormat_AV_PIX_FMT_RGB24,
        };
        let pixel_format = if source == DmaFormat::P010 && config.codec != 0 {
            ffi::AVPixelFormat_AV_PIX_FMT_P010LE
        } else {
            ffi::AVPixelFormat_AV_PIX_FMT_NV12
        };
        let codec_name = CString::new(format!(
            "{}_{}",
            ["h264", "hevc", "av1"][config.codec as usize],
            device.backend
        ))
        .unwrap();
        let codec = unsafe { ffi::avcodec_find_encoder_by_name(codec_name.as_ptr()) };
        if codec.is_null() {
            return Err("The selected hardware encoder is not present in FFmpeg.".into());
        }
        let context = Codec(unsafe { ffi::avcodec_alloc_context3(codec) });
        if context.0.is_null() {
            return Err("Unable to allocate a video encoder.".into());
        }
        let width = config.video.width as i32;
        let height = config.video.height as i32;
        let mut pixels: ffi::v4l2_pix_format = unsafe { std::mem::zeroed() };
        pixels.pixelformat = config.video.fourcc;
        pixels.colorspace = config.video.colorspace;
        pixels.quantization = config.video.quantization;
        pixels.__bindgen_anon_1.ycbcr_enc = config.video.encoding;
        pixels.xfer_func = config.video.transfer_function;
        let (range, matrix, transfer) =
            crate::capture::infrastructure::dmabuf::resolve_color(&pixels, config.color_range)
                .map_err(|error| error.replace("DMA-BUF capture", "Recording"))?;
        let source_full = range == 2;
        let destination_full = source_full && !source.is_rgb();
        let rec601 = matrix == 1;
        unsafe {
            (*context.0).sample_aspect_ratio = ffi::AVRational { num: 1, den: 1 };
            (*context.0).width = width;
            (*context.0).height = height;
            (*context.0).time_base = VIDEO_TIME_BASE;
            (*context.0).framerate = ffi::AVRational {
                num: config.video.interval_denominator as i32,
                den: config.video.interval_numerator as i32,
            };
            (*context.0).pix_fmt = pixel_format;
            (*context.0).bit_rate = config.bitrate as i64;
            (*context.0).rc_max_rate = if config.constant_rate {
                config.bitrate as i64
            } else {
                config.bitrate.saturating_mul(2) as i64
            };
            (*context.0).rc_buffer_size = config.bitrate.min(i32::MAX as u64) as i32;
            (*context.0).gop_size = (2 * config.video.interval_denominator
                / config.video.interval_numerator)
                .clamp(1, 1000) as i32;
            (*context.0).max_b_frames = 0;
            (*context.0).flags |= ffi::AV_CODEC_FLAG_GLOBAL_HEADER as i32;
            (*context.0).color_range = if destination_full {
                ffi::AVColorRange_AVCOL_RANGE_JPEG
            } else {
                ffi::AVColorRange_AVCOL_RANGE_MPEG
            };
            (*context.0).colorspace = if rec601 {
                ffi::AVColorSpace_AVCOL_SPC_SMPTE170M
            } else {
                ffi::AVColorSpace_AVCOL_SPC_BT709
            };
            (*context.0).color_primaries = if rec601 {
                ffi::AVColorPrimaries_AVCOL_PRI_SMPTE170M
            } else {
                ffi::AVColorPrimaries_AVCOL_PRI_BT709
            };
            (*context.0).color_trc = if transfer == 2 {
                ffi::AVColorTransferCharacteristic_AVCOL_TRC_IEC61966_2_1
            } else {
                ffi::AVColorTransferCharacteristic_AVCOL_TRC_BT709
            };
            (*context.0).log_level_offset = 16;
        }
        let mut hardware = None;
        if device.backend == "nvenc" {
            option(&context, c"gpu", &device.device)?;
            option(&context, c"preset", "p4")?;
            option(&context, c"tune", "ll")?;
            option(
                &context,
                c"rc",
                if config.constant_rate { "cbr" } else { "vbr" },
            )?;
            option(&context, c"rc-lookahead", "0")?;
            option(&context, c"zerolatency", "1")?;
        }
        if device.backend != "nvenc" || cuda_experiment {
            let kind = if cuda_experiment {
                ffi::AVHWDeviceType_AV_HWDEVICE_TYPE_CUDA
            } else if device.backend == "qsv" {
                ffi::AVHWDeviceType_AV_HWDEVICE_TYPE_QSV
            } else {
                ffi::AVHWDeviceType_AV_HWDEVICE_TYPE_VAAPI
            };
            let mut reference = Buffer(ptr::null_mut());
            let mut options = ptr::null_mut();
            let target = CString::new(device.device.as_str()).unwrap();
            if device.backend == "qsv" {
                unsafe {
                    ffi::av_dict_set(&mut options, c"child_device".as_ptr(), target.as_ptr(), 0);
                }
            }
            let initialized = unsafe {
                ffi::av_hwdevice_ctx_create(
                    &mut reference.0,
                    kind,
                    if device.backend == "qsv" {
                        ptr::null()
                    } else {
                        target.as_ptr()
                    },
                    options,
                    0,
                )
            };
            unsafe {
                ffi::av_dict_free(&mut options);
            }
            checked(initialized, "Initialize the selected recording GPU")?;
            let frames = Buffer(unsafe { ffi::av_hwframe_ctx_alloc(reference.0) });
            if frames.0.is_null() {
                return Err("Unable to allocate hardware recording surfaces.".into());
            }
            let format = if cuda_experiment {
                ffi::AVPixelFormat_AV_PIX_FMT_CUDA
            } else if device.backend == "qsv" {
                ffi::AVPixelFormat_AV_PIX_FMT_QSV
            } else {
                ffi::AVPixelFormat_AV_PIX_FMT_VAAPI
            };
            unsafe {
                let storage = (*frames.0).data.cast::<ffi::AVHWFramesContext>();
                (*storage).format = format;
                (*storage).sw_format = pixel_format;
                (*storage).width = width;
                (*storage).height = height;
                (*storage).initial_pool_size = 8;
                checked(
                    ffi::av_hwframe_ctx_init(frames.0),
                    "Initialize encoder surface pool",
                )?;
                (*context.0).pix_fmt = format;
                (*context.0).hw_frames_ctx = ffi::av_buffer_ref(frames.0);
                if (*context.0).hw_frames_ctx.is_null() {
                    return Err("Unable to retain encoder surfaces.".into());
                }
            }
            if device.backend == "vaapi" {
                option(
                    &context,
                    c"rc_mode",
                    if config.constant_rate { "CBR" } else { "VBR" },
                )?;
            }
            hardware = Some(Frame::new()?);
        }
        checked(
            unsafe { ffi::avcodec_open2(context.0, codec, ptr::null_mut()) },
            "Open hardware video encoder",
        )?;
        let frame = Frame::new()?;
        unsafe {
            (*frame.0).format = pixel_format;
            (*frame.0).width = width;
            (*frame.0).height = height;
            checked(
                ffi::av_frame_get_buffer(frame.0, 64),
                "Allocate recording conversion surfaces",
            )?;
        }
        let decoder = if compressed {
            Some(super::decoder::Decoder::new(
                device,
                config.video.width,
                config.video.height,
            )?)
        } else {
            None
        };
        let mut normalized_rgb = Vec::new();
        if source.is_rgb() && !source_full {
            normalized_rgb
                .try_reserve_exact(layout.byte_length + 64)
                .map_err(|_| "Unable to reserve recording RGB normalization storage.")?;
            normalized_rgb.resize(layout.byte_length + 64, 0);
        }
        let scaler = unsafe {
            ffi::sws_getContext(
                width,
                height,
                source_format,
                width,
                height,
                pixel_format,
                ffi::MGF_SWS_FAST_BILINEAR,
                ptr::null_mut(),
                ptr::null_mut(),
                ptr::null(),
            )
        };
        if scaler.is_null() {
            return Err("Unable to create the recording color converter.".into());
        }
        let mut value = Self {
            context,
            frame,
            hardware,
            cuda_experiment,
            experiment_mode,
            direct_import: true,
            vulkan: None,
            transfer_name: String::new(),
            vulkan_disabled: false,
            scaler,
            source_format,
            source_stride: config.video.bytes_per_line as i32,
            required_bytes: layout.byte_length,
            uv_offset: layout.uv_offset as usize,
            height,
            last_pts: -1,
            decoder,
            source_full,
            rec601,
            normalized_rgb,
        };
        let coefficients = unsafe {
            ffi::sws_getCoefficients(if rec601 {
                ffi::SWS_CS_ITU601 as i32
            } else {
                ffi::SWS_CS_ITU709 as i32
            })
        };
        checked(
            unsafe {
                ffi::sws_setColorspaceDetails(
                    value.scaler,
                    coefficients,
                    i32::from(source_full),
                    coefficients,
                    i32::from(destination_full),
                    0,
                    1 << 16,
                    1 << 16,
                )
            },
            "Set recording color range",
        )?;
        if value.cuda_experiment {
            let hardware = value
                .hardware
                .as_ref()
                .ok_or("Missing CUDA recording surface.")?;
            unsafe {
                checked(
                    ffi::av_hwframe_get_buffer((*value.context.0).hw_frames_ctx, hardware.0, 0),
                    "Warm recording CUDA surface pool",
                )?;
            }
            if value.experiment_mode != "cuda" {
                match super::vulkan_upload::VulkanUpload::new(hardware, width, height) {
                    Ok(mut vulkan) => {
                        // Prime the driver transfer path before capture can fill the bounded queue.
                        unsafe {
                            for (plane, rows, byte) in [
                                (0, height as usize, 16),
                                (1, (height as usize).div_ceil(2), 128),
                            ] {
                                std::ptr::write_bytes(
                                    (*value.frame.0).data[plane],
                                    byte,
                                    (*value.frame.0).linesize[plane] as usize * rows,
                                );
                            }
                        }
                        if let Err(error) = vulkan.transfer(
                            &value.frame,
                            None,
                            hardware,
                            stride as usize,
                            layout.uv_offset as usize,
                            true,
                        ) {
                            eprintln!("Experimental recording Vulkan warmup failed: {error}");
                        }
                        value.vulkan = Some(vulkan);
                    }
                    Err(error) => eprintln!("Experimental recording Vulkan unavailable: {error}"),
                }
            }
        }
        Ok(value)
    }

    pub fn transfer_name(&self) -> &str {
        if self.cuda_experiment {
            if self.transfer_name.is_empty() {
                "experimental-pending"
            } else {
                &self.transfer_name
            }
        } else {
            "CPU staging to hardware encoder"
        }
    }

    pub fn experimental(&self) -> bool {
        self.cuda_experiment
    }

    pub fn submit_shared(
        &mut self,
        shared: &std::sync::Arc<dyn crate::media::infrastructure::packets::SharedVideoFrame>,
        pts: i64,
    ) -> Result<bool, String> {
        if !self.cuda_experiment {
            return Err("Shared recording input requires experimental transfer.".into());
        }
        self.submit_experimental(shared.bytes(), Some(shared), pts)
    }

    fn submit_experimental(
        &mut self,
        bytes: &[u8],
        shared: Option<
            &std::sync::Arc<dyn crate::media::infrastructure::packets::SharedVideoFrame>,
        >,
        pts: i64,
    ) -> Result<bool, String> {
        if pts <= self.last_pts {
            return Ok(false);
        }
        if bytes.len() < self.required_bytes {
            return Err("Truncated experimental NV12 recording input.".into());
        }
        let source = Frame::new()?;
        let hardware = self
            .hardware
            .as_ref()
            .ok_or("Missing CUDA recording surface.")?;
        unsafe {
            (*source.0).format = self.source_format;
            (*source.0).width = (*self.context.0).width;
            (*source.0).height = self.height;
            (*source.0).data[0] = bytes.as_ptr().cast_mut();
            (*source.0).data[1] = bytes.as_ptr().add(self.uv_offset).cast_mut();
            (*source.0).linesize[0] = self.source_stride;
            (*source.0).linesize[1] = self.source_stride;
            ffi::av_frame_unref(hardware.0);
            checked(
                ffi::av_hwframe_get_buffer((*self.context.0).hw_frames_ctx, hardware.0, 0),
                "Acquire experimental CUDA surface",
            )?;
        }
        let width = unsafe { (*self.context.0).width as usize };
        let mut transfer = None;
        if self.direct_import && self.experiment_mode != "vulkan" {
            if let Some(shared) = shared {
                let context = super::cuda_host::Context::enter(hardware)?;
                match shared.prepare_gpu_read().and_then(|_| {
                    context.import(
                        shared.as_ref(),
                        hardware,
                        self.source_stride as usize,
                        self.uv_offset,
                        width,
                        self.height as usize,
                    )
                }) {
                    Ok(()) => transfer = Some("cuda-dmabuf"),
                    Err(error) => {
                        self.direct_import = false;
                        eprintln!("Experimental recording direct DMA-BUF disabled: {error}");
                    }
                }
            }
        }
        if transfer.is_none() && self.experiment_mode != "cuda" && !self.vulkan_disabled {
            if let Some(vulkan) = &mut self.vulkan {
                match vulkan.transfer(
                    &source,
                    shared,
                    hardware,
                    self.source_stride as usize,
                    self.uv_offset,
                    self.experiment_mode == "vulkan",
                ) {
                    Ok(name) => transfer = Some(name),
                    Err(error) => {
                        eprintln!("Experimental recording Vulkan fallback: {error}");
                        // Retire the failed device after recording rather than stalling capture on driver teardown.
                        self.vulkan_disabled = true;
                    }
                }
            }
        }
        let transfer = if let Some(name) = transfer {
            name
        } else {
            let context = super::cuda_host::Context::enter(hardware)?;
            context.upload(
                bytes,
                hardware,
                self.source_stride as usize,
                self.uv_offset,
                width,
                self.height as usize,
            )?;
            "cuda-host-upload"
        };
        if self.transfer_name != transfer {
            self.transfer_name = transfer.into();
            eprintln!("Experimental recording transfer active: {transfer}");
        }
        unsafe {
            (*hardware.0).pts = pts;
            (*hardware.0).color_range = (*self.context.0).color_range;
            (*hardware.0).colorspace = (*self.context.0).colorspace;
            (*hardware.0).color_primaries = (*self.context.0).color_primaries;
            (*hardware.0).color_trc = (*self.context.0).color_trc;
            checked(
                ffi::avcodec_send_frame(self.context.0, hardware.0),
                "Submit experimental GPU frame",
            )?;
        }
        self.last_pts = pts;
        Ok(true)
    }

    pub fn decoder_name(&self) -> &str {
        self.decoder
            .as_ref()
            .map_or("Not required (raw input)", |decoder| decoder.name.as_str())
    }

    pub fn submit(&mut self, bytes: &[u8], pts: i64) -> Result<bool, String> {
        if pts <= self.last_pts {
            return Ok(false);
        }
        if self.decoder.is_none()
            && bytes.len() < self.required_bytes + ffi::AV_INPUT_BUFFER_PADDING_SIZE as usize
        {
            return Err("Truncated or unpadded recording input.".into());
        }
        if self.cuda_experiment {
            return self.submit_experimental(bytes, None, pts);
        }
        unsafe {
            checked(
                ffi::av_frame_make_writable(self.frame.0),
                "Acquire writable recording frame",
            )?;
            let converted = if let Some(decoder) = &mut self.decoder {
                let Some(decoded) = decoder.decode(bytes)? else {
                    return Ok(false);
                };
                self.scaler = ffi::sws_getCachedContext(
                    self.scaler,
                    (*decoded).width,
                    (*decoded).height,
                    (*decoded).format as ffi::AVPixelFormat,
                    (*self.frame.0).width,
                    self.height,
                    (*self.frame.0).format as ffi::AVPixelFormat,
                    ffi::MGF_SWS_FAST_BILINEAR,
                    ptr::null_mut(),
                    ptr::null_mut(),
                    ptr::null(),
                );
                if self.scaler.is_null() {
                    return Err("Unable to convert decoded recording frames.".into());
                }
                let coefficients = ffi::sws_getCoefficients(if self.rec601 {
                    ffi::SWS_CS_ITU601
                } else {
                    ffi::SWS_CS_ITU709
                } as i32);
                checked(
                    ffi::sws_setColorspaceDetails(
                        self.scaler,
                        coefficients,
                        i32::from(self.source_full),
                        coefficients,
                        i32::from(
                            (*self.context.0).color_range == ffi::AVColorRange_AVCOL_RANGE_JPEG,
                        ),
                        0,
                        1 << 16,
                        1 << 16,
                    ),
                    "Set decoded recording color range",
                )?;
                ffi::sws_scale(
                    self.scaler,
                    (*decoded).data.as_ptr().cast(),
                    (*decoded).linesize.as_ptr(),
                    0,
                    self.height,
                    (*self.frame.0).data.as_ptr(),
                    (*self.frame.0).linesize.as_ptr(),
                )
            } else {
                let bytes = if self.normalized_rgb.is_empty() {
                    bytes
                } else {
                    for (destination, source) in self.normalized_rgb[..self.required_bytes]
                        .iter_mut()
                        .zip(bytes)
                    {
                        *destination = expand_limited_rgb(*source);
                    }
                    &self.normalized_rgb
                };
                let mut planes = [bytes.as_ptr(), ptr::null(), ptr::null(), ptr::null()];
                let mut strides = [self.source_stride, 0, 0, 0];
                if matches!(
                    self.source_format,
                    ffi::AVPixelFormat_AV_PIX_FMT_NV12 | ffi::AVPixelFormat_AV_PIX_FMT_P010LE
                ) {
                    planes[1] = bytes.as_ptr().add(self.uv_offset);
                    strides[1] = self.source_stride;
                }
                ffi::sws_scale(
                    self.scaler,
                    planes.as_ptr(),
                    strides.as_ptr(),
                    0,
                    self.height,
                    (*self.frame.0).data.as_ptr(),
                    (*self.frame.0).linesize.as_ptr(),
                )
            };
            if converted != self.height {
                return Err("Recording color conversion returned an incomplete frame.".into());
            }
            (*self.frame.0).pts = pts;
            (*self.frame.0).color_range = (*self.context.0).color_range;
            (*self.frame.0).colorspace = (*self.context.0).colorspace;
            (*self.frame.0).color_primaries = (*self.context.0).color_primaries;
            (*self.frame.0).color_trc = (*self.context.0).color_trc;
            let send = if let Some(hardware) = &self.hardware {
                ffi::av_frame_unref(hardware.0);
                checked(
                    ffi::av_hwframe_get_buffer((*self.context.0).hw_frames_ctx, hardware.0, 0),
                    "Acquire encoder hardware surface",
                )?;
                checked(
                    ffi::av_hwframe_transfer_data(hardware.0, self.frame.0, 0),
                    "Upload recording frame",
                )?;
                checked(
                    ffi::av_frame_copy_props(hardware.0, self.frame.0),
                    "Copy encoder surface metadata",
                )?;
                (*hardware.0).pts = pts;
                hardware.0
            } else {
                self.frame.0
            };
            checked(
                ffi::avcodec_send_frame(self.context.0, send),
                "Submit recording video frame",
            )?;
        }
        self.last_pts = pts;
        Ok(true)
    }
}

pub(crate) struct AudioEncoder {
    pub context: Codec,
    pub frame: Frame,
    converted: Frame,
    resampler: *mut ffi::SwrContext,
    fifo: *mut ffi::AVAudioFifo,
    first_pts: Option<i64>,
    emitted: i64,
    last_compensation: u64,
}
impl Drop for AudioEncoder {
    fn drop(&mut self) {
        unsafe {
            ffi::swr_free(&mut self.resampler);
            ffi::av_audio_fifo_free(self.fifo);
        }
    }
}
impl AudioEncoder {
    pub fn new() -> Result<Self, String> {
        let codec = unsafe { ffi::avcodec_find_encoder_by_name(c"aac".as_ptr()) };
        if codec.is_null() {
            return Err("FFmpeg's AAC encoder is unavailable.".into());
        }
        let context = Codec(unsafe { ffi::avcodec_alloc_context3(codec) });
        if context.0.is_null() {
            return Err("Unable to allocate an audio encoder.".into());
        }
        unsafe {
            ffi::av_channel_layout_default(&mut (*context.0).ch_layout, 2);
            (*context.0).sample_rate = 48000;
            (*context.0).sample_fmt = ffi::AVSampleFormat_AV_SAMPLE_FMT_FLTP;
            (*context.0).bit_rate = 256000;
            (*context.0).time_base = ffi::AVRational { num: 1, den: 48000 };
            (*context.0).flags |= ffi::AV_CODEC_FLAG_GLOBAL_HEADER as i32;
            checked(
                ffi::avcodec_open2(context.0, codec, ptr::null_mut()),
                "Open AAC encoder",
            )?;
        }
        let frame = Frame::new()?;
        let converted = Frame::new()?;
        let mut value = Self {
            context,
            frame,
            converted,
            resampler: ptr::null_mut(),
            fifo: ptr::null_mut(),
            first_pts: None,
            emitted: 0,
            last_compensation: 0,
        };
        unsafe {
            for (frame, samples) in [
                (&value.frame, (*value.context.0).frame_size),
                (&value.converted, 4096),
            ] {
                (*frame.0).format = ffi::AVSampleFormat_AV_SAMPLE_FMT_FLTP;
                (*frame.0).sample_rate = 48000;
                ffi::av_channel_layout_default(&mut (*frame.0).ch_layout, 2);
                (*frame.0).nb_samples = samples;
                checked(
                    ffi::av_frame_get_buffer(frame.0, 0),
                    "Allocate audio recording buffer",
                )?;
            }
            checked(
                ffi::swr_alloc_set_opts2(
                    &mut value.resampler,
                    &(*value.context.0).ch_layout,
                    ffi::AVSampleFormat_AV_SAMPLE_FMT_FLTP,
                    48000,
                    &(*value.context.0).ch_layout,
                    ffi::AVSampleFormat_AV_SAMPLE_FMT_FLT,
                    48000,
                    0,
                    ptr::null_mut(),
                ),
                "Allocate recording resampler",
            )?;
            checked(
                ffi::swr_init(value.resampler),
                "Initialize recording resampler",
            )?;
            value.fifo = ffi::av_audio_fifo_alloc(ffi::AVSampleFormat_AV_SAMPLE_FMT_FLTP, 2, 8192);
            if value.fifo.is_null() {
                return Err("Unable to allocate the bounded audio FIFO.".into());
            }
        }
        Ok(value)
    }

    pub fn push(&mut self, block: &super::input::AudioBlock, origin: u64) -> Result<(), String> {
        let end = block
            .timestamp
            .saturating_add(block.frames as u64 * 1_000_000_000 / 48000);
        if end <= origin {
            return Ok(());
        }
        let skip = if block.timestamp < origin {
            ((origin - block.timestamp) as u128 * 48000 / 1_000_000_000).min(block.frames as u128)
                as usize
        } else {
            0
        };
        let count = block.frames - skip;
        if count == 0 {
            return Ok(());
        }
        let timestamp = block
            .timestamp
            .saturating_add(skip as u64 * 1_000_000_000 / 48000);
        let target = ((timestamp.saturating_sub(origin) as u128 * 48000) / 1_000_000_000) as i64;
        if self.first_pts.is_none() {
            self.first_pts = Some(target);
        }
        unsafe {
            let expected =
                self.first_pts.unwrap() + self.emitted + ffi::av_audio_fifo_size(self.fifo) as i64;
            let error = target - expected;
            if error.abs() > 4800 {
                return Err(
                    "The recording audio clock has a discontinuity exceeding 100 ms.".into(),
                );
            }
            if timestamp.saturating_sub(self.last_compensation) >= 500_000_000 {
                checked(
                    ffi::swr_set_compensation(self.resampler, error.clamp(-48, 48) as i32, 48000),
                    "Correct recording audio clock drift",
                )?;
                self.last_compensation = timestamp;
            }
            checked(
                ffi::av_frame_make_writable(self.converted.0),
                "Acquire audio conversion buffer",
            )?;
            let input = [block.samples.as_ptr().add(skip * 2).cast::<u8>()];
            let samples = ffi::swr_convert(
                self.resampler,
                (*self.converted.0).extended_data,
                4096,
                input.as_ptr(),
                count as i32,
            );
            checked(samples, "Convert recording audio")?;
            if ffi::av_audio_fifo_size(self.fifo) + samples > 8192 {
                return Err("The bounded recording audio FIFO is full.".into());
            }
            if ffi::av_audio_fifo_write(
                self.fifo,
                (*self.converted.0).extended_data.cast(),
                samples,
            ) != samples
            {
                return Err("Unable to enqueue converted recording audio.".into());
            }
        }
        Ok(())
    }

    #[cfg(test)]
    pub fn emitted_samples(&self) -> i64 {
        self.emitted
    }

    pub fn flush_resampler(&mut self) -> Result<(), String> {
        unsafe {
            checked(
                ffi::av_frame_make_writable(self.converted.0),
                "Acquire final audio conversion buffer",
            )?;
            let count = ffi::swr_convert(
                self.resampler,
                (*self.converted.0).extended_data,
                4096,
                ptr::null(),
                0,
            );
            checked(count, "Drain recording resampler")?;
            if ffi::av_audio_fifo_size(self.fifo) + count > 8192 {
                return Err("The final recording audio FIFO is full.".into());
            }
            if count > 0
                && ffi::av_audio_fifo_write(
                    self.fifo,
                    (*self.converted.0).extended_data.cast(),
                    count,
                ) != count
            {
                return Err("Unable to drain final recording audio samples.".into());
            }
        }
        Ok(())
    }

    pub fn submit(&mut self, final_block: bool) -> Result<bool, String> {
        unsafe {
            let size = ffi::av_audio_fifo_size(self.fifo);
            let required = (*self.context.0).frame_size;
            if size == 0 || (!final_block && size < required) {
                return Ok(false);
            }
            let count = size.min(required);
            checked(
                ffi::av_frame_make_writable(self.frame.0),
                "Acquire writable audio frame",
            )?;
            (*self.frame.0).nb_samples = count;
            if ffi::av_audio_fifo_read(self.fifo, (*self.frame.0).extended_data.cast(), count)
                != count
            {
                return Err("Unable to read recording audio FIFO.".into());
            }
            (*self.frame.0).pts = self.first_pts.unwrap_or(0) + self.emitted;
            checked(
                ffi::avcodec_send_frame(self.context.0, self.frame.0),
                "Submit recording audio frame",
            )?;
            self.emitted += count as i64;
        }
        Ok(true)
    }
}

fn expand_limited_rgb(value: u8) -> u8 {
    ((u32::from(value).saturating_sub(16) * 255 + 109) / 219).min(255) as u8
}

#[cfg(test)]
mod tests;
