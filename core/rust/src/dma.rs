use crate::video::{
    MappedBuffer, VideoFile, capture_buffer, checked_ioctl, frame_layout, last_os_error,
    read_write_ioctl, try_ioctl, write_ioctl,
};
use crate::{ffi, make_c_string};
use std::ffi::{CStr, c_char, c_void};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::ptr;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, Receiver, SyncSender, TryRecvError, TrySendError};
use std::sync::{Arc, Mutex, OnceLock, Weak};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

const ABI_VERSION: u32 = 1;
const NV12: u32 = u32::from_le_bytes(*b"NV12");
const CAPTURE_TYPE: u32 = ffi::v4l2_buf_type_V4L2_BUF_TYPE_VIDEO_CAPTURE;
const MMAP_MEMORY: u32 = ffi::v4l2_memory_V4L2_MEMORY_MMAP;
const BUFFER_COUNT: u32 = 6;
const MAX_BUFFER_BYTES: usize = 128 * 1024 * 1024;
const MAX_POOL_BYTES: usize = 512 * 1024 * 1024;
const MAX_PENDING_READS: usize = 64;
const O_CLOEXEC: u32 = 0o2000000;
const O_RDWR: u32 = 2;
const POLLIN: i16 = 1;
const POLL_ERROR_OR_HANGUP: i16 = 0x18;
const POLL_FAILURE: i16 = 0x38;
const EAGAIN: i32 = 11;
const EINTR: i32 = 4;
const SET_FORMAT: u64 = read_write_ioctl::<ffi::v4l2_format>(5);
const REQUEST_BUFFERS: u64 = read_write_ioctl::<ffi::v4l2_requestbuffers>(8);
const QUERY_BUFFER: u64 = read_write_ioctl::<ffi::v4l2_buffer>(9);
const QUEUE_BUFFER: u64 = read_write_ioctl::<ffi::v4l2_buffer>(15);
const EXPORT_BUFFER: u64 = read_write_ioctl::<ffi::v4l2_exportbuffer>(16);
const DEQUEUE_BUFFER: u64 = read_write_ioctl::<ffi::v4l2_buffer>(17);
const STREAM_ON: u64 = write_ioctl::<i32>(18);
const STREAM_OFF: u64 = write_ioctl::<i32>(19);
const GET_PARAMETERS: u64 = read_write_ioctl::<ffi::v4l2_streamparm>(21);
const SET_PARAMETERS: u64 = read_write_ioctl::<ffi::v4l2_streamparm>(22);
const DMA_BUF_IOCTL_SYNC: u64 = (1 << 30) | (8 << 16) | ((b'b' as u64) << 8);
static NEXT_GENERATION: AtomicU64 = AtomicU64::new(1);
const VMALLOC_EXPORTER: &str = "videobuf2_vmalloc";

fn parse_exporter_name(metadata: &str) -> Result<&str, String> {
    let mut exporter = None;
    for line in metadata.lines() {
        if let Some((key, value)) = line.split_once(':')
            && key.trim() == "exp_name"
        {
            let name = value.trim();
            if exporter.is_some()
                || name.is_empty()
                || name.len() > 256
                || name.chars().any(char::is_control)
            {
                return Err("DMA-BUF exporter metadata is invalid.".into());
            }
            exporter = Some(name);
        }
    }
    exporter.ok_or_else(|| "DMA-BUF exporter metadata is unavailable.".into())
}

fn read_exporter_name(descriptor: RawFd) -> Result<String, String> {
    let metadata = std::fs::read_to_string(format!("/proc/self/fdinfo/{descriptor}"))
        .map_err(|error| format!("Read DMA-BUF exporter metadata: {error}"))?;
    parse_exporter_name(&metadata).map(str::to_owned)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CacheFlushMethod {
    Optimized,
    Legacy,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct CacheFlushPlan {
    method: CacheFlushMethod,
    cache_line_bytes: usize,
}

impl CacheFlushPlan {
    fn from_features(
        supported: bool,
        optimized: bool,
        cache_line_bytes: usize,
    ) -> Result<Self, String> {
        if !supported {
            return Err("DMA-BUF source visibility requires CPU cache-flush support.".into());
        }
        if !(8..=4096).contains(&cache_line_bytes) || !cache_line_bytes.is_power_of_two() {
            return Err("The CPU reported an unsupported cache-line size.".into());
        }
        Ok(Self {
            method: if optimized {
                CacheFlushMethod::Optimized
            } else {
                CacheFlushMethod::Legacy
            },
            cache_line_bytes,
        })
    }

    #[cfg(target_arch = "x86_64")]
    fn detect() -> Result<Self, String> {
        use std::arch::x86_64::{__cpuid, __cpuid_count};
        let maximum = __cpuid(0).eax;
        if maximum < 1 {
            return Err("CPU cache-flush capabilities are unavailable.".into());
        }
        let features = __cpuid(1);
        let optimized = maximum >= 7 && __cpuid_count(7, 0).ebx & (1 << 23) != 0;
        Self::from_features(
            features.edx & (1 << 19) != 0,
            optimized,
            (((features.ebx >> 8) & 0xff) * 8) as usize,
        )
    }

    #[cfg(not(target_arch = "x86_64"))]
    fn detect() -> Result<Self, String> {
        Err("DMA-BUF source visibility is unsupported on this CPU architecture.".into())
    }

    fn validate_bounds(&self, mapping: &MappedBuffer, byte_length: usize) -> Result<(), String> {
        if mapping.pointer.is_null()
            || byte_length == 0
            || byte_length > mapping.length
            || mapping.length > isize::MAX as usize
            || mapping.pointer.addr().checked_add(mapping.length).is_none()
            || !mapping.pointer.addr().is_multiple_of(self.cache_line_bytes)
        {
            return Err("DMA-BUF source visibility bounds are invalid.".into());
        }
        Ok(())
    }

    #[cfg(target_arch = "x86_64")]
    fn flush(&self, mapping: &MappedBuffer, byte_length: usize) -> Result<&'static str, String> {
        self.validate_bounds(mapping, byte_length)?;
        // Safety: The frame lease pins this mapping and prevents capture writes throughout the
        // flush. The validated CPU supports these instructions and every operand remains inside
        // the MMAP allocation. The assembly retains its default compiler memory barriers.
        unsafe {
            if self.method == CacheFlushMethod::Legacy {
                std::arch::asm!("mfence", options(nostack, preserves_flags));
            }
            for offset in (0..byte_length).step_by(self.cache_line_bytes) {
                let address = mapping.pointer.add(offset);
                match self.method {
                    CacheFlushMethod::Optimized => {
                        std::arch::asm!("clflushopt [{address}]", address = in(reg) address,
                            options(nostack, preserves_flags));
                    }
                    CacheFlushMethod::Legacy => {
                        std::arch::asm!("clflush [{address}]", address = in(reg) address,
                            options(nostack, preserves_flags));
                    }
                }
            }
            // The fence completes writeback before EGL can submit a read of the exported pages.
            match self.method {
                CacheFlushMethod::Optimized => {
                    std::arch::asm!("sfence", options(nostack, preserves_flags));
                }
                CacheFlushMethod::Legacy => {
                    std::arch::asm!("mfence", options(nostack, preserves_flags));
                }
            }
        }
        Ok(match self.method {
            CacheFlushMethod::Optimized => "clflushopt-sfence",
            CacheFlushMethod::Legacy => "clflush-mfence",
        })
    }

    #[cfg(not(target_arch = "x86_64"))]
    fn flush(&self, _mapping: &MappedBuffer, _byte_length: usize) -> Result<&'static str, String> {
        Err("DMA-BUF source visibility is unsupported on this CPU architecture.".into())
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct GpuVisibility {
    pub(crate) method: &'static str,
    pub(crate) bytes: usize,
    pub(crate) elapsed_ns: u64,
}

struct DmaStorage {
    _mapping: MappedBuffer,
    descriptor: OwnedFd,
    exporter: String,
}

// The CPU mapping is never accessed while a queued slot can be written by the device.
unsafe impl Send for DmaStorage {}
unsafe impl Sync for DmaStorage {}

struct ReturnQueue {
    running: AtomicBool,
    returned: Mutex<Vec<u32>>,
}

impl ReturnQueue {
    fn new() -> Self {
        Self {
            running: AtomicBool::new(true),
            returned: Mutex::new(Vec::with_capacity(BUFFER_COUNT as usize)),
        }
    }

    fn complete(&self, index: u32) {
        if self.running.load(Ordering::Acquire) {
            self.returned.lock().unwrap().push(index);
        }
    }
}

pub(crate) struct DmaFrame {
    storage: Arc<DmaStorage>,
    returns: Weak<ReturnQueue>,
    reaper: SyncSender<PendingRead>,
    gpu_visibility: OnceLock<Result<GpuVisibility, String>>,
    pub(crate) index: u32,
    pub(crate) width: u32,
    pub(crate) height: u32,
    pub(crate) stride: u32,
    pub(crate) byte_length: usize,
    pub(crate) uv_offset: u64,
    pub(crate) fourcc: u32,
    pub(crate) modifier: u64,
    pub(crate) generation: u64,
    pub(crate) sequence: u64,
    pub(crate) timestamp_ns: u64,
    pub(crate) color_range: i32,
    pub(crate) encoding: u32,
    pub(crate) colorspace: u32,
    pub(crate) transfer_function: u32,
}

impl DmaFrame {
    pub(crate) fn descriptor(&self) -> RawFd {
        self.storage.descriptor.as_raw_fd()
    }

    fn cached_gpu_visibility(
        &self,
        prepare: impl FnOnce() -> Result<GpuVisibility, String>,
    ) -> Result<GpuVisibility, String> {
        self.gpu_visibility
            .get_or_init(prepare)
            .as_ref()
            .copied()
            .map_err(Clone::clone)
    }

    // The tested NVIDIA/vmalloc pairing requires explicit writeback of cached source pages.
    // The renderer invokes this before import only for that verified driver/exporter pairing.
    pub(crate) fn ensure_gpu_visibility(&self) -> Result<GpuVisibility, String> {
        self.cached_gpu_visibility(|| {
            if self.storage.exporter != VMALLOC_EXPORTER {
                return Ok(GpuVisibility {
                    method: "implicit",
                    bytes: 0,
                    elapsed_ns: 0,
                });
            }
            static PLAN: OnceLock<Result<CacheFlushPlan, String>> = OnceLock::new();
            let plan = PLAN
                .get_or_init(CacheFlushPlan::detect)
                .as_ref()
                .map_err(Clone::clone)?;
            let started = Instant::now();
            let method = plan.flush(&self.storage._mapping, self.byte_length)?;
            Ok(GpuVisibility {
                method,
                bytes: self.byte_length,
                elapsed_ns: started.elapsed().as_nanos().min(u64::MAX as u128) as u64,
            })
        })
    }

    fn view(&self, token: *mut c_void) -> ffi::mgfr_dma_frame_view {
        ffi::mgfr_dma_frame_view {
            abi_version: ABI_VERSION,
            struct_size: std::mem::size_of::<ffi::mgfr_dma_frame_view>() as u32,
            token,
            fd: self.descriptor(),
            index: self.index,
            width: self.width,
            height: self.height,
            stride: self.stride,
            fourcc: self.fourcc,
            byte_length: self.byte_length,
            uv_offset: self.uv_offset,
            modifier: self.modifier,
            generation: self.generation,
            sequence: self.sequence,
            timestamp_ns: self.timestamp_ns,
            color_range: self.color_range,
            encoding: self.encoding,
            colorspace: self.colorspace,
            transfer_function: self.transfer_function,
        }
    }
}

impl Drop for DmaFrame {
    fn drop(&mut self) {
        if let Some(queue) = self.returns.upgrade() {
            queue.complete(self.index);
        }
    }
}

struct PendingRead {
    _frame: Arc<DmaFrame>,
    fence: OwnedFd,
}

fn start_reaper() -> Result<SyncSender<PendingRead>, String> {
    let (sender, receiver) = mpsc::sync_channel(MAX_PENDING_READS);
    thread::Builder::new()
        .name("mygoframe-fence".into())
        .spawn(move || reap_reads(receiver))
        .map_err(|error| format!("Start GPU fence worker: {error}"))?;
    Ok(sender)
}

fn reap_reads(receiver: Receiver<PendingRead>) {
    let mut pending: Vec<PendingRead> = Vec::with_capacity(MAX_PENDING_READS);
    let mut descriptors = Vec::with_capacity(MAX_PENDING_READS);
    loop {
        if pending.is_empty() {
            match receiver.recv() {
                Ok(read) => pending.push(read),
                Err(_) => break,
            }
        }
        while pending.len() < MAX_PENDING_READS {
            match receiver.try_recv() {
                Ok(read) => pending.push(read),
                Err(TryRecvError::Empty | TryRecvError::Disconnected) => break,
            }
        }
        descriptors.clear();
        descriptors.extend(pending.iter().map(|read| ffi::pollfd {
            fd: read.fence.as_raw_fd(),
            events: POLLIN,
            revents: 0,
        }));
        let result = unsafe { ffi::poll(descriptors.as_mut_ptr(), descriptors.len() as _, 2) };
        if result < 0 {
            // A failed wait never authorizes storage reuse; the owned descriptors remain live.
            thread::sleep(Duration::from_millis(1));
            continue;
        }
        for index in (0..pending.len()).rev() {
            if descriptors[index].revents & (POLLIN | POLL_ERROR_OR_HANGUP) != 0 {
                // A sync-file error denotes a completed fence whose GPU job failed.
                drop(pending.swap_remove(index));
            }
        }
    }
}

/// Keeps the captured allocation pinned until the exported native GPU fence completes.
/// The caller must retain its own frame reference until this returns successfully or GPU work
/// has been completed explicitly after a submission error.
pub(crate) fn submit_gpu_read(frame: &Arc<DmaFrame>, fence: OwnedFd) -> Result<(), String> {
    let read = PendingRead {
        _frame: Arc::clone(frame),
        fence,
    };
    frame.reaper.try_send(read).map_err(|error| match error {
        TrySendError::Full(_) => "The bounded GPU completion queue is full.".into(),
        TrySendError::Disconnected(_) => "The GPU completion worker is unavailable.".into(),
    })
}

/// # Safety
/// `token` must remain an outstanding token from `mgfr_dma_video_acquire` during this call.
pub(crate) unsafe fn retain_frame(token: *mut c_void) -> Arc<DmaFrame> {
    unsafe { Arc::clone(&*(token.cast::<Arc<DmaFrame>>())) }
}

struct DmaPool {
    latest: Mutex<Option<Arc<DmaFrame>>>,
}

struct DmaStatus {
    stop: AtomicBool,
    running: AtomicBool,
    frames: AtomicU64,
    dropped_frames: AtomicU64,
    error: Mutex<String>,
}

struct DmaEngine {
    pool: Arc<DmaPool>,
    status: Arc<DmaStatus>,
    worker: Option<JoinHandle<()>>,
}

impl Drop for DmaEngine {
    fn drop(&mut self) {
        self.status.stop.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
        self.pool.latest.lock().unwrap().take();
    }
}

struct DmaCapture {
    file: VideoFile,
    slots: Vec<Arc<DmaStorage>>,
    queued: Vec<bool>,
    returns: Arc<ReturnQueue>,
    reaper: SyncSender<PendingRead>,
    info: ffi::mgfr_video_info,
    required_bytes: usize,
    uv_offset: u64,
    color_range: i32,
    encoding: u32,
    generation: u64,
    streaming: bool,
    consecutive_invalid_frames: u32,
}

impl Drop for DmaCapture {
    fn drop(&mut self) {
        self.returns.running.store(false, Ordering::Release);
        if self.streaming {
            let mut kind = CAPTURE_TYPE as i32;
            let _ = try_ioctl(self.file.descriptor(), STREAM_OFF, &mut kind);
        }
    }
}

fn resolve_color(pixels: &ffi::v4l2_pix_format, selection: i32) -> Result<(i32, u32, u32), String> {
    let colorspace = pixels.colorspace;
    if colorspace == ffi::v4l2_colorspace_V4L2_COLORSPACE_BT2020
        || colorspace == ffi::v4l2_colorspace_V4L2_COLORSPACE_DCI_P3
    {
        return Err("DMA-BUF capture requires an SDR Rec.601 or Rec.709 signal.".into());
    }
    let reported = unsafe { pixels.__bindgen_anon_1.ycbcr_enc };
    let encoding = if reported == 0 {
        // These defaults match V4L2_MAP_YCBCR_ENC_DEFAULT in linux/videodev2.h.
        match colorspace {
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
    } else {
        reported
    };
    if !matches!(encoding, 1 | 2) {
        return Err("DMA-BUF capture supports only Rec.601 and Rec.709 YCbCr matrices.".into());
    }
    let transfer_function = if pixels.xfer_func == ffi::v4l2_xfer_func_V4L2_XFER_FUNC_DEFAULT {
        // These defaults match V4L2_MAP_XFER_FUNC_DEFAULT in linux/videodev2.h.
        match colorspace {
            ffi::v4l2_colorspace_V4L2_COLORSPACE_OPRGB => ffi::v4l2_xfer_func_V4L2_XFER_FUNC_OPRGB,
            ffi::v4l2_colorspace_V4L2_COLORSPACE_SMPTE240M => {
                ffi::v4l2_xfer_func_V4L2_XFER_FUNC_SMPTE240M
            }
            ffi::v4l2_colorspace_V4L2_COLORSPACE_DCI_P3 => {
                ffi::v4l2_xfer_func_V4L2_XFER_FUNC_DCI_P3
            }
            ffi::v4l2_colorspace_V4L2_COLORSPACE_RAW => ffi::v4l2_xfer_func_V4L2_XFER_FUNC_NONE,
            ffi::v4l2_colorspace_V4L2_COLORSPACE_SRGB
            | ffi::v4l2_colorspace_V4L2_COLORSPACE_JPEG => ffi::v4l2_xfer_func_V4L2_XFER_FUNC_SRGB,
            _ => ffi::v4l2_xfer_func_V4L2_XFER_FUNC_709,
        }
    } else {
        pixels.xfer_func
    };
    if !matches!(transfer_function, 1 | 2) {
        return Err("DMA-BUF capture supports only Rec.709 and sRGB transfer functions.".into());
    }
    let full = match selection {
        1 => false,
        2 => true,
        _ => {
            pixels.quantization == ffi::v4l2_quantization_V4L2_QUANTIZATION_FULL_RANGE
                || (pixels.quantization == 0
                    && colorspace == ffi::v4l2_colorspace_V4L2_COLORSPACE_JPEG)
        }
    };
    Ok((if full { 2 } else { 1 }, encoding, transfer_function))
}

impl DmaCapture {
    fn new(configuration: &ffi::mgfr_video_config) -> Result<Self, String> {
        if configuration.path.is_null()
            || configuration.fourcc != NV12
            || configuration.interval_numerator == 0
            || configuration.interval_denominator == 0
            || !(0..=2).contains(&configuration.color_range)
            || configuration.width & 1 != 0
            || configuration.height & 1 != 0
            || frame_layout(configuration.width, configuration.height).is_none()
        {
            return Err("DMA-BUF capture requires a valid progressive NV12 configuration.".into());
        }
        let path = unsafe { CStr::from_ptr(configuration.path) }.to_string_lossy();
        let file = VideoFile::open(&path)?;
        let descriptor = file.descriptor();
        let mut format: ffi::v4l2_format = unsafe { std::mem::zeroed() };
        format.type_ = CAPTURE_TYPE;
        let mut pixels: ffi::v4l2_pix_format = unsafe { std::mem::zeroed() };
        pixels.width = configuration.width;
        pixels.height = configuration.height;
        pixels.pixelformat = NV12;
        pixels.field = ffi::v4l2_field_V4L2_FIELD_NONE;
        format.fmt.pix = pixels;
        checked_ioctl(descriptor, SET_FORMAT, &mut format, "Set DMA video format")?;
        let pixels = unsafe { format.fmt.pix };
        if pixels.pixelformat != NV12
            || pixels.field != ffi::v4l2_field_V4L2_FIELD_NONE
            || pixels.width & 1 != 0
            || pixels.height & 1 != 0
            || pixels.bytesperline < pixels.width
            || frame_layout(pixels.width, pixels.height).is_none()
        {
            return Err("The driver did not negotiate a supported progressive NV12 layout.".into());
        }
        let (color_range, encoding, transfer_function) =
            resolve_color(&pixels, configuration.color_range)?;
        let uv_offset = u64::from(pixels.bytesperline)
            .checked_mul(u64::from(pixels.height))
            .ok_or("NV12 plane offset overflowed.")?;
        let required_bytes = uv_offset
            .checked_add(uv_offset / 2)
            .filter(|length| *length <= MAX_BUFFER_BYTES as u64)
            .ok_or("NV12 allocation exceeds the DMA buffer budget.")?
            as usize;
        if (pixels.sizeimage as usize) < required_bytes {
            return Err("The negotiated NV12 allocation is too short.".into());
        }
        if pixels.sizeimage as usize > MAX_BUFFER_BYTES
            || (pixels.sizeimage as usize)
                .checked_mul(BUFFER_COUNT as usize)
                .is_none_or(|length| length > MAX_POOL_BYTES)
        {
            return Err("The negotiated DMA allocation exceeds the pool memory budget.".into());
        }
        let mut parameters: ffi::v4l2_streamparm = unsafe { std::mem::zeroed() };
        parameters.type_ = CAPTURE_TYPE;
        checked_ioctl(
            descriptor,
            GET_PARAMETERS,
            &mut parameters,
            "Read DMA frame interval",
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
                "Set DMA frame interval",
            )?;
            capture_parameters = unsafe { parameters.parm.capture };
        }
        if capture_parameters.timeperframe.numerator == 0
            || capture_parameters.timeperframe.denominator == 0
        {
            return Err("The capture device did not report a valid frame interval.".into());
        }
        let mut request: ffi::v4l2_requestbuffers = unsafe { std::mem::zeroed() };
        request.count = BUFFER_COUNT;
        request.type_ = CAPTURE_TYPE;
        request.memory = MMAP_MEMORY;
        checked_ioctl(
            descriptor,
            REQUEST_BUFFERS,
            &mut request,
            "Allocate DMA capture buffers",
        )?;
        if !(3..=16).contains(&request.count) || request.capabilities & 0x10 == 0 {
            return Err(
                "DMA-BUF capture requires bounded buffers with orphaned-buffer support.".into(),
            );
        }
        let mut slots = Vec::with_capacity(request.count as usize);
        let mut allocation_bytes = 0_usize;
        for index in 0..request.count {
            let mut buffer = capture_buffer(index);
            checked_ioctl(
                descriptor,
                QUERY_BUFFER,
                &mut buffer,
                "Query DMA capture buffer",
            )?;
            let length = buffer.length as usize;
            allocation_bytes = allocation_bytes
                .checked_add(length)
                .ok_or("DMA pool size overflowed.")?;
            if length < required_bytes
                || length > MAX_BUFFER_BYTES
                || allocation_bytes > MAX_POOL_BYTES
            {
                return Err("The driver DMA allocation exceeds the frame memory budget.".into());
            }
            let address = unsafe {
                ffi::mmap(
                    ptr::null_mut(),
                    length,
                    3,
                    1,
                    descriptor,
                    buffer.m.offset as i64,
                )
            };
            if address.is_null() || address as isize == -1 {
                return Err(last_os_error("Map DMA capture buffer"));
            }
            let mapping = MappedBuffer {
                pointer: address.cast(),
                length,
            };
            let mut export: ffi::v4l2_exportbuffer = unsafe { std::mem::zeroed() };
            export.type_ = CAPTURE_TYPE;
            export.index = index;
            export.flags = O_CLOEXEC | O_RDWR;
            checked_ioctl(
                descriptor,
                EXPORT_BUFFER,
                &mut export,
                "Export DMA capture buffer",
            )?;
            if export.fd < 0 {
                return Err("The driver returned an invalid DMA-BUF descriptor.".into());
            }
            // The export ioctl transfers one new descriptor to this allocation owner.
            let exported = unsafe { OwnedFd::from_raw_fd(export.fd) };
            let exporter = read_exporter_name(exported.as_raw_fd())?;
            slots.push(Arc::new(DmaStorage {
                _mapping: mapping,
                descriptor: exported,
                exporter,
            }));
            checked_ioctl(
                descriptor,
                QUEUE_BUFFER,
                &mut buffer,
                "Queue DMA capture buffer",
            )?;
        }
        let reaper = start_reaper()?;
        let mut kind = CAPTURE_TYPE as i32;
        checked_ioctl(descriptor, STREAM_ON, &mut kind, "Start DMA capture")?;
        Ok(Self {
            file,
            slots,
            queued: vec![true; request.count as usize],
            returns: Arc::new(ReturnQueue::new()),
            reaper,
            info: ffi::mgfr_video_info {
                fourcc: NV12,
                width: pixels.width,
                height: pixels.height,
                stride: pixels.width * 4,
                interval_numerator: capture_parameters.timeperframe.numerator,
                interval_denominator: capture_parameters.timeperframe.denominator,
                bytes_per_line: pixels.bytesperline,
                quantization: pixels.quantization,
                encoding,
                colorspace: pixels.colorspace,
                transfer_function,
                capture_buffers: request.count,
            },
            required_bytes,
            uv_offset,
            color_range,
            encoding,
            generation: NEXT_GENERATION.fetch_add(1, Ordering::Relaxed),
            streaming: true,
            consecutive_invalid_frames: 0,
        })
    }

    fn requeue(&mut self, index: u32) -> Result<(), String> {
        let queued = self
            .queued
            .get_mut(index as usize)
            .ok_or("Invalid returned DMA slot.")?;
        if *queued {
            return Err("A DMA slot was returned more than once.".into());
        }
        let mut buffer = capture_buffer(index);
        checked_ioctl(
            self.file.descriptor(),
            QUEUE_BUFFER,
            &mut buffer,
            "Return DMA capture buffer",
        )?;
        *queued = true;
        Ok(())
    }

    fn reclaim(&mut self) -> Result<(), String> {
        let completed: Vec<_> = self.returns.returned.lock().unwrap().drain(..).collect();
        for index in completed {
            self.requeue(index)?;
        }
        Ok(())
    }

    fn read_latest(&mut self, status: &DmaStatus) -> Result<Option<ffi::v4l2_buffer>, String> {
        self.reclaim()?;
        let mut descriptor = ffi::pollfd {
            fd: self.file.descriptor(),
            events: POLLIN,
            revents: 0,
        };
        let result = unsafe { ffi::poll(&mut descriptor, 1, 2) };
        if result < 0 {
            if std::io::Error::last_os_error().raw_os_error() == Some(EINTR) {
                return Ok(None);
            }
            return Err(last_os_error("Wait for DMA capture"));
        }
        if descriptor.revents & POLL_FAILURE != 0 {
            return Err("DMA capture device disconnected or reported a stream error.".into());
        }
        if descriptor.revents & POLLIN == 0 {
            return Ok(None);
        }
        let mut latest: Option<ffi::v4l2_buffer> = None;
        for _ in 0..self.slots.len() {
            let mut buffer = capture_buffer(0);
            match try_ioctl(self.file.descriptor(), DEQUEUE_BUFFER, &mut buffer) {
                Ok(()) => {}
                Err(EAGAIN) => break,
                Err(_) => return Err(last_os_error("Dequeue DMA capture buffer")),
            }
            let queued = self
                .queued
                .get_mut(buffer.index as usize)
                .ok_or("The driver returned an invalid DMA slot.")?;
            if !*queued {
                return Err("The driver dequeued a slot still leased to the GPU.".into());
            }
            *queued = false;
            if buffer.bytesused as usize > self.slots[buffer.index as usize]._mapping.length {
                return Err("The driver returned invalid DMA capture bounds.".into());
            }
            if (buffer.bytesused as usize) < self.required_bytes
                || buffer.flags & ffi::V4L2_BUF_FLAG_ERROR != 0
            {
                self.requeue(buffer.index)?;
                status.dropped_frames.fetch_add(1, Ordering::Relaxed);
                self.consecutive_invalid_frames += 1;
                if self.consecutive_invalid_frames >= 120 {
                    return Err("DMA capture returned 120 consecutive invalid frames.".into());
                }
                continue;
            }
            self.consecutive_invalid_frames = 0;
            if let Some(previous) = latest.replace(buffer) {
                self.requeue(previous.index)?;
                status.dropped_frames.fetch_add(1, Ordering::Relaxed);
            }
        }
        Ok(latest)
    }

    fn publish(&self, buffer: ffi::v4l2_buffer, sequence: u64) -> Arc<DmaFrame> {
        let timestamp_ns = if buffer.flags & ffi::V4L2_BUF_FLAG_TIMESTAMP_MASK
            == ffi::V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC
            && buffer.timestamp.tv_sec >= 0
            && (0..1_000_000).contains(&buffer.timestamp.tv_usec)
        {
            buffer.timestamp.tv_sec as u64 * 1_000_000_000 + buffer.timestamp.tv_usec as u64 * 1000
        } else {
            let mut timestamp: ffi::timespec = unsafe { std::mem::zeroed() };
            if unsafe { ffi::clock_gettime(1, &mut timestamp) } == 0 {
                timestamp.tv_sec as u64 * 1_000_000_000 + timestamp.tv_nsec as u64
            } else {
                0
            }
        };
        Arc::new(DmaFrame {
            storage: Arc::clone(&self.slots[buffer.index as usize]),
            returns: Arc::downgrade(&self.returns),
            reaper: self.reaper.clone(),
            gpu_visibility: OnceLock::new(),
            index: buffer.index,
            width: self.info.width,
            height: self.info.height,
            stride: self.info.bytes_per_line,
            byte_length: self.required_bytes,
            uv_offset: self.uv_offset,
            fourcc: NV12,
            modifier: 0,
            generation: self.generation,
            sequence,
            timestamp_ns,
            color_range: self.color_range,
            encoding: self.encoding,
            colorspace: self.info.colorspace,
            transfer_function: self.info.transfer_function,
        })
    }
}

fn capture_worker(mut capture: DmaCapture, pool: Arc<DmaPool>, status: Arc<DmaStatus>) {
    let mut sequence = 0_u64;
    while !status.stop.load(Ordering::Acquire) {
        match capture.read_latest(&status) {
            Ok(Some(buffer)) => {
                sequence = sequence.wrapping_add(1);
                let frame = capture.publish(buffer, sequence);
                let previous = pool.latest.lock().unwrap().replace(frame);
                drop(previous);
                status.frames.fetch_add(1, Ordering::Relaxed);
            }
            Ok(None) => {}
            Err(error) => {
                *status.error.lock().unwrap() = error;
                break;
            }
        }
    }
    capture.returns.running.store(false, Ordering::Release);
    status.running.store(false, Ordering::Release);
}

fn write_error(pointer: *mut *mut c_char, message: String) {
    if !pointer.is_null() {
        unsafe { *pointer = make_c_string(message) };
    }
}

/// # Safety
/// Configuration and output pointers must address valid ABI structures. The returned session is
/// controlled serially and destroyed once. Exported frame tokens have independent lifetimes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_dma_video_create(
    configuration: *const ffi::mgfr_video_config,
    info: *mut ffi::mgfr_video_info,
    error: *mut *mut c_char,
) -> *mut c_void {
    if !error.is_null() {
        unsafe { *error = ptr::null_mut() };
    }
    if configuration.is_null() || info.is_null() {
        write_error(
            error,
            "DMA video configuration and output are required.".into(),
        );
        return ptr::null_mut();
    }
    let capture = match DmaCapture::new(unsafe { &*configuration }) {
        Ok(capture) => capture,
        Err(message) => {
            write_error(error, message);
            return ptr::null_mut();
        }
    };
    let details = capture.info;
    let pool = Arc::new(DmaPool {
        latest: Mutex::new(None),
    });
    let status = Arc::new(DmaStatus {
        stop: AtomicBool::new(false),
        running: AtomicBool::new(true),
        frames: AtomicU64::new(0),
        dropped_frames: AtomicU64::new(0),
        error: Mutex::new(String::new()),
    });
    let worker_pool = Arc::clone(&pool);
    let worker_status = Arc::clone(&status);
    let worker = match thread::Builder::new()
        .name("mygoframe-dma".into())
        .spawn(move || capture_worker(capture, worker_pool, worker_status))
    {
        Ok(worker) => worker,
        Err(error_value) => {
            write_error(error, format!("Start DMA capture worker: {error_value}"));
            return ptr::null_mut();
        }
    };
    unsafe { *info = details };
    Box::into_raw(Box::new(DmaEngine {
        pool,
        status,
        worker: Some(worker),
    }))
    .cast()
}

/// # Safety
/// `video` must be a live DMA session returned by `mgfr_dma_video_create`, or null.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_dma_video_destroy(video: *mut c_void) {
    if !video.is_null() {
        unsafe { drop(Box::from_raw(video.cast::<DmaEngine>())) };
    }
}

/// # Safety
/// Session control must be serialized. `output` must address writable frame storage. Each returned
/// token is released exactly once after CPU callers and submitted GPU reads stop using it.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_dma_video_acquire(
    video: *mut c_void,
    after_sequence: u64,
    output: *mut ffi::mgfr_dma_frame_view,
) -> i32 {
    if video.is_null() || output.is_null() {
        return -1;
    }
    let engine = unsafe { &*video.cast::<DmaEngine>() };
    let latest = engine.pool.latest.lock().unwrap();
    let Some(frame) = latest
        .as_ref()
        .filter(|frame| frame.sequence > after_sequence)
    else {
        return 0;
    };
    let frame = Arc::clone(frame);
    let token = Box::into_raw(Box::new(Arc::clone(&frame))).cast();
    unsafe { *output = frame.view(token) };
    1
}

/// # Safety
/// `token` must be an outstanding DMA frame token, or null, and is released only once.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_dma_video_release(token: *mut c_void) {
    if !token.is_null() {
        unsafe { drop(Box::from_raw(token.cast::<Arc<DmaFrame>>())) };
    }
}

/// # Safety
/// `video` must remain live while this function writes the supplied status structure.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_dma_video_status(
    video: *mut c_void,
    output: *mut ffi::mgfr_video_status_data,
) -> i32 {
    if video.is_null() || output.is_null() {
        return 0;
    }
    let engine = unsafe { &*video.cast::<DmaEngine>() };
    let mut result: ffi::mgfr_video_status_data = unsafe { std::mem::zeroed() };
    result.running = i32::from(engine.status.running.load(Ordering::Acquire));
    result.frames = engine.status.frames.load(Ordering::Relaxed);
    result.dropped_frames = engine.status.dropped_frames.load(Ordering::Relaxed);
    let error = engine.status.error.lock().unwrap();
    for (destination, source) in result
        .error
        .iter_mut()
        .zip(error.as_bytes().iter())
        .take(511)
    {
        *destination = *source as c_char;
    }
    unsafe { *output = result };
    1
}

fn synchronize_cpu_read(frame: &DmaFrame, ending: bool) -> Result<(), String> {
    let mut flags = 1_u64 | if ending { 4 } else { 0 };
    loop {
        match try_ioctl(frame.descriptor(), DMA_BUF_IOCTL_SYNC, &mut flags) {
            Ok(()) => return Ok(()),
            Err(EAGAIN) => thread::yield_now(),
            Err(_) => return Err(last_os_error("Synchronize DMA reference read")),
        }
    }
}

/// # Safety
/// The token must remain acquired throughout this diagnostic call. `pixels` must address at least
/// `capacity` writable bytes and must not overlap the captured NV12 allocation.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_dma_copy_rgba(
    token: *mut c_void,
    pixels: *mut u8,
    capacity: usize,
    error: *mut *mut c_char,
) -> i32 {
    if !error.is_null() {
        unsafe { *error = ptr::null_mut() };
    }
    if token.is_null() || pixels.is_null() {
        write_error(
            error,
            "A leased DMA frame and output pixels are required.".into(),
        );
        return 0;
    }
    let frame = unsafe { retain_frame(token) };
    let Some((_, required)) = frame_layout(frame.width, frame.height) else {
        write_error(error, "Invalid DMA reference frame dimensions.".into());
        return 0;
    };
    if capacity < required || frame.storage._mapping.pointer.is_null() {
        write_error(
            error,
            "The DMA reference output is too short or storage is unavailable.".into(),
        );
        return 0;
    }
    if let Err(message) = synchronize_cpu_read(&frame, false) {
        write_error(error, message);
        return 0;
    }
    // The lease excludes device writes, and both this reference conversion and GPU sampling only read.
    let source = unsafe {
        std::slice::from_raw_parts(
            frame.storage._mapping.pointer,
            frame.storage._mapping.length,
        )
    };
    let output = unsafe { std::slice::from_raw_parts_mut(pixels, required) };
    let result = crate::video::dma_reference_rgba(&frame.view(token), source, output);
    let completed = synchronize_cpu_read(&frame, true);
    match result.and(completed) {
        Ok(()) => 1,
        Err(message) => {
            write_error(error, message);
            0
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs::File;
    use std::io::Write;
    use std::time::Instant;

    unsafe extern "C" {
        fn eventfd(initial: u32, flags: i32) -> i32;
        fn mprotect(address: *mut c_void, length: usize, protection: i32) -> i32;
        fn sysconf(name: i32) -> isize;
    }

    fn frame_with_sender(
        reaper: SyncSender<PendingRead>,
        queue: &Arc<ReturnQueue>,
    ) -> Arc<DmaFrame> {
        let descriptor: OwnedFd = File::open("/dev/null").unwrap().into();
        Arc::new(DmaFrame {
            storage: Arc::new(DmaStorage {
                _mapping: MappedBuffer {
                    pointer: ptr::null_mut(),
                    length: 0,
                },
                descriptor,
                exporter: "test".into(),
            }),
            returns: Arc::downgrade(queue),
            reaper,
            gpu_visibility: OnceLock::new(),
            index: 2,
            width: 4,
            height: 2,
            stride: 4,
            byte_length: 12,
            uv_offset: 8,
            fourcc: NV12,
            modifier: 0,
            generation: 1,
            sequence: 1,
            timestamp_ns: 1,
            color_range: 1,
            encoding: 2,
            colorspace: 8,
            transfer_function: 1,
        })
    }

    fn pending_fence() -> (OwnedFd, File) {
        let descriptor = unsafe { eventfd(0, (O_CLOEXEC | 0o4000) as i32) };
        assert!(descriptor >= 0);
        let fence = unsafe { OwnedFd::from_raw_fd(descriptor) };
        let signal: File = fence.try_clone().unwrap().into();
        (fence, signal)
    }

    fn wait_until(mut predicate: impl FnMut() -> bool) {
        let deadline = Instant::now() + Duration::from_secs(1);
        while !predicate() {
            assert!(
                Instant::now() < deadline,
                "GPU completion worker did not retire the lease"
            );
            thread::sleep(Duration::from_millis(1));
        }
    }

    #[test]
    fn exporter_metadata_requires_one_complete_exporter_name() {
        assert_eq!(
            parse_exporter_name("pos:\t0\nflags:\t02000002\nexp_name:\tvideobuf2_vmalloc\n")
                .unwrap(),
            VMALLOC_EXPORTER
        );
        assert_eq!(
            parse_exporter_name("exp_name: videobuf2_vmalloc_extra\n").unwrap(),
            "videobuf2_vmalloc_extra"
        );
        for metadata in [
            "pos: 0\n",
            "exp_name: \n",
            "exp_name: videobuf2_vmalloc\nexp_name: another\n",
            "exp_name: invalid\0name\n",
        ] {
            assert!(parse_exporter_name(metadata).is_err());
        }
        assert!(read_exporter_name(-1).is_err());
    }

    #[test]
    fn cache_flush_requires_supported_instructions_and_checked_lines() {
        assert!(CacheFlushPlan::from_features(false, false, 64).is_err());
        for cache_line_bytes in [0, 7, 63, 4097, usize::MAX] {
            assert!(CacheFlushPlan::from_features(true, true, cache_line_bytes).is_err());
        }
        assert_eq!(
            CacheFlushPlan::from_features(true, true, 64)
                .unwrap()
                .method,
            CacheFlushMethod::Optimized
        );
        let plan = CacheFlushPlan::from_features(true, false, 64).unwrap();
        assert_eq!(plan.method, CacheFlushMethod::Legacy);
        let mut mapping = std::mem::ManuallyDrop::new(MappedBuffer {
            pointer: 0x1000_usize as *mut u8,
            length: 128,
        });
        assert!(plan.validate_bounds(&mapping, 127).is_ok());
        assert!(plan.validate_bounds(&mapping, 0).is_err());
        assert!(plan.validate_bounds(&mapping, 129).is_err());
        mapping.pointer = 0x1001_usize as *mut u8;
        assert!(plan.validate_bounds(&mapping, 128).is_err());
        mapping.pointer = (usize::MAX - 63) as *mut u8;
        assert!(plan.validate_bounds(&mapping, 128).is_err());
        mapping.pointer = ptr::null_mut();
        assert!(plan.validate_bounds(&mapping, 128).is_err());
    }

    #[test]
    fn source_visibility_runs_once_across_shared_frame_consumers() {
        let queue = Arc::new(ReturnQueue::new());
        let frame = frame_with_sender(start_reaper().unwrap(), &queue);
        let executions = AtomicU64::new(0);
        let expected = GpuVisibility {
            method: "test",
            bytes: 12,
            elapsed_ns: 123,
        };
        thread::scope(|scope| {
            for _ in 0..16 {
                let frame = Arc::clone(&frame);
                let executions = &executions;
                scope.spawn(move || {
                    assert_eq!(
                        frame.cached_gpu_visibility(|| {
                            executions.fetch_add(1, Ordering::SeqCst);
                            thread::sleep(Duration::from_millis(2));
                            Ok(expected)
                        }),
                        Ok(expected)
                    );
                });
            }
        });
        assert_eq!(executions.load(Ordering::SeqCst), 1);
        assert_eq!(frame.ensure_gpu_visibility(), Ok(expected));
        assert!(queue.returned.lock().unwrap().is_empty());
        drop(frame);
        assert_eq!(*queue.returned.lock().unwrap(), [2]);
    }

    #[test]
    fn source_visibility_caches_failures_and_skips_other_exporters() {
        let queue = Arc::new(ReturnQueue::new());
        let frame = frame_with_sender(start_reaper().unwrap(), &queue);
        assert_eq!(
            frame.ensure_gpu_visibility(),
            Ok(GpuVisibility {
                method: "implicit",
                bytes: 0,
                elapsed_ns: 0,
            })
        );
        let another = frame_with_sender(start_reaper().unwrap(), &queue);
        assert!(
            another
                .cached_gpu_visibility(|| Err("unsupported".into()))
                .is_err()
        );
        assert_eq!(another.ensure_gpu_visibility().unwrap_err(), "unsupported");
    }

    #[cfg(target_arch = "x86_64")]
    #[test]
    fn cache_flush_preserves_guarded_source_and_caches_real_execution() {
        let Ok(plan) = CacheFlushPlan::detect() else {
            return;
        };
        let page = unsafe { sysconf(30) } as usize;
        assert!(page > 0 && page.is_power_of_two());
        let allocation =
            unsafe { ffi::mmap(ptr::null_mut(), page * 2, 3, 2 | 0x20, -1, 0) }.cast::<u8>();
        assert_ne!(allocation as isize, -1);
        assert!(!allocation.is_null());
        let mapping = MappedBuffer {
            pointer: allocation,
            length: page * 2,
        };
        unsafe { ptr::write_bytes(allocation, 0xa5, page) };
        assert_eq!(unsafe { mprotect(allocation.add(page).cast(), page, 0) }, 0);
        let queue = Arc::new(ReturnQueue::new());
        let mut frame = frame_with_sender(start_reaper().unwrap(), &queue);
        let owned = Arc::get_mut(&mut frame).unwrap();
        owned.byte_length = page;
        let storage = Arc::get_mut(&mut owned.storage).unwrap();
        storage._mapping = mapping;
        storage.exporter = VMALLOC_EXPORTER.into();
        let prepared = frame.ensure_gpu_visibility().unwrap();
        assert_eq!(prepared.bytes, page);
        assert_eq!(
            prepared.method,
            if plan.method == CacheFlushMethod::Optimized {
                "clflushopt-sfence"
            } else {
                "clflush-mfence"
            }
        );
        assert_eq!(frame.ensure_gpu_visibility(), Ok(prepared));
        assert!(
            unsafe { std::slice::from_raw_parts(allocation, page) }
                .iter()
                .all(|byte| *byte == 0xa5)
        );
        let source = std::mem::ManuallyDrop::new(MappedBuffer {
            pointer: allocation,
            length: page,
        });
        let legacy = CacheFlushPlan::from_features(true, false, plan.cache_line_bytes).unwrap();
        assert_eq!(legacy.flush(&source, page - 1).unwrap(), "clflush-mfence");
        assert!(
            unsafe { std::slice::from_raw_parts(allocation, page) }
                .iter()
                .all(|byte| *byte == 0xa5)
        );
    }

    #[test]
    fn copied_readers_return_a_capture_slot_exactly_once() {
        let queue = Arc::new(ReturnQueue::new());
        let frame = frame_with_sender(start_reaper().unwrap(), &queue);
        let another_reader = Arc::clone(&frame);
        drop(frame);
        assert!(queue.returned.lock().unwrap().is_empty());
        drop(another_reader);
        assert_eq!(*queue.returned.lock().unwrap(), [2]);
    }

    #[test]
    fn unsignalled_gpu_fence_prevents_capture_requeue() {
        let queue = Arc::new(ReturnQueue::new());
        let frame = frame_with_sender(start_reaper().unwrap(), &queue);
        let (fence, mut signal) = pending_fence();
        submit_gpu_read(&frame, fence).unwrap();
        drop(frame);
        thread::sleep(Duration::from_millis(10));
        assert!(queue.returned.lock().unwrap().is_empty());
        signal.write_all(&1_u64.to_ne_bytes()).unwrap();
        wait_until(|| !queue.returned.lock().unwrap().is_empty());
        assert_eq!(*queue.returned.lock().unwrap(), [2]);
    }

    #[test]
    fn gpu_read_pins_allocation_after_capture_session_stops() {
        let queue = Arc::new(ReturnQueue::new());
        let frame = frame_with_sender(start_reaper().unwrap(), &queue);
        let storage = Arc::downgrade(&frame.storage);
        let (fence, mut signal) = pending_fence();
        submit_gpu_read(&frame, fence).unwrap();
        queue.running.store(false, Ordering::Release);
        drop(queue);
        drop(frame);
        assert!(storage.upgrade().is_some());
        signal.write_all(&1_u64.to_ne_bytes()).unwrap();
        wait_until(|| storage.upgrade().is_none());
    }

    #[test]
    fn failed_gpu_submission_preserves_the_callers_frame_lease() {
        let queue = Arc::new(ReturnQueue::new());
        let (sender, receiver) = mpsc::sync_channel(1);
        drop(receiver);
        let frame = frame_with_sender(sender, &queue);
        let (fence, _) = pending_fence();
        assert!(submit_gpu_read(&frame, fence).is_err());
        assert!(queue.returned.lock().unwrap().is_empty());
        assert_eq!(Arc::strong_count(&frame), 1);
        drop(frame);
        assert_eq!(*queue.returned.lock().unwrap(), [2]);
    }

    #[test]
    fn color_selection_rejects_hdr_and_resolves_range_without_guessing() {
        let mut pixels: ffi::v4l2_pix_format = unsafe { std::mem::zeroed() };
        pixels.colorspace = ffi::v4l2_colorspace_V4L2_COLORSPACE_REC709;
        assert_eq!(resolve_color(&pixels, 0).unwrap(), (1, 2, 1));
        assert_eq!(resolve_color(&pixels, 2).unwrap(), (2, 2, 1));
        pixels.quantization = ffi::v4l2_quantization_V4L2_QUANTIZATION_FULL_RANGE;
        assert_eq!(resolve_color(&pixels, 0).unwrap(), (2, 2, 1));
        assert_eq!(resolve_color(&pixels, 1).unwrap(), (1, 2, 1));
        pixels.xfer_func = ffi::v4l2_xfer_func_V4L2_XFER_FUNC_SMPTE2084;
        assert!(resolve_color(&pixels, 0).is_err());
        pixels.xfer_func = 1;
        pixels.colorspace = ffi::v4l2_colorspace_V4L2_COLORSPACE_BT2020;
        assert!(resolve_color(&pixels, 0).is_err());
    }

    #[test]
    fn color_defaults_reject_unsupported_matrices_and_transfers() {
        let mut pixels: ffi::v4l2_pix_format = unsafe { std::mem::zeroed() };
        pixels.colorspace = ffi::v4l2_colorspace_V4L2_COLORSPACE_SMPTE240M;
        assert!(resolve_color(&pixels, 0).unwrap_err().contains("matrices"));
        pixels.__bindgen_anon_1.ycbcr_enc = ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_709;
        assert!(
            resolve_color(&pixels, 0)
                .unwrap_err()
                .contains("transfer functions")
        );
        pixels.__bindgen_anon_1.ycbcr_enc = ffi::v4l2_ycbcr_encoding_V4L2_YCBCR_ENC_DEFAULT;
        for colorspace in [
            ffi::v4l2_colorspace_V4L2_COLORSPACE_OPRGB,
            ffi::v4l2_colorspace_V4L2_COLORSPACE_RAW,
        ] {
            pixels.colorspace = colorspace;
            assert!(
                resolve_color(&pixels, 0)
                    .unwrap_err()
                    .contains("transfer functions")
            );
        }
        pixels.colorspace = ffi::v4l2_colorspace_V4L2_COLORSPACE_SRGB;
        assert_eq!(resolve_color(&pixels, 0).unwrap(), (1, 1, 2));
        pixels.colorspace = ffi::v4l2_colorspace_V4L2_COLORSPACE_JPEG;
        assert_eq!(resolve_color(&pixels, 0).unwrap(), (2, 1, 2));
        pixels.colorspace = ffi::v4l2_colorspace_V4L2_COLORSPACE_SMPTE170M;
        assert_eq!(resolve_color(&pixels, 0).unwrap(), (1, 1, 1));
    }

    #[test]
    fn unsupported_dma_formats_fail_before_opening_a_device() {
        let config = ffi::mgfr_video_config {
            path: c"/dev/mygoframe-nonexistent-dma-test".as_ptr(),
            fourcc: u32::from_le_bytes(*b"MJPG"),
            width: 640,
            height: 480,
            interval_numerator: 1,
            interval_denominator: 60,
            color_range: 0,
        };
        let failure = DmaCapture::new(&config).err().unwrap();
        assert!(failure.contains("NV12 configuration"));
    }
}
