mod codec;
mod cuda_host;
mod decoder;
mod devices;
mod vulkan_upload;
use crate::media::infrastructure::packets as input;
mod muxer;
mod runtime;

use crate::{ffi, make_c_string};
use codec::{AudioEncoder, Packet, VideoEncoder, checked};
use devices::json_string;
use std::ffi::{CStr, c_char, c_void};
use std::ptr;
use std::sync::atomic::{AtomicBool, AtomicI32, AtomicU64, Ordering};
use std::sync::{Arc, Mutex, Weak};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use crate::recording::domain::configuration::{Configuration, VideoFormat};

struct Status {
    phase: AtomicI32,
    stop: AtomicBool,
    frames: AtomicU64,
    dropped: AtomicU64,
    bytes: AtomicU64,
    duration: AtomicU64,
    error: Mutex<String>,
    adapter: Mutex<String>,
    decoder: Mutex<String>,
    transfer: Mutex<String>,
}
struct Recording {
    status: Arc<Status>,
    input: Weak<input::VideoInput>,
    worker: Option<JoinHandle<()>>,
    config: Configuration,
}
impl Recording {
    fn request_stop(&self) {
        self.status.stop.store(true, Ordering::Release);
        if self.status.phase.load(Ordering::Acquire) < 2 {
            self.status.phase.store(2, Ordering::Release);
        }
        if let Some(input) = self.input.upgrade() {
            input.active.store(false, Ordering::Release);
            input.signal.notify();
        }
    }
}
impl Drop for Recording {
    fn drop(&mut self) {
        self.request_stop();
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

fn run(
    config: Configuration,
    video: Arc<input::VideoInput>,
    tap: Arc<input::VideoTap>,
    audio: Option<Arc<input::AudioInput>>,
    status: &Status,
) -> Result<(), String> {
    let (device, mut encoder) = if config.gpu == "auto" || config.gpu.is_empty() {
        let mut chosen = None;
        let mut errors = Vec::new();
        for candidate in devices::discover()? {
            match VideoEncoder::new(&config, &candidate) {
                Ok(encoder) => {
                    chosen = Some((candidate, encoder));
                    break;
                }
                Err(error) => errors.push(error),
            }
        }
        chosen.ok_or_else(|| {
            if errors.is_empty() {
                "No supported hardware recording GPU is available.".into()
            } else {
                errors.join("; ")
            }
        })?
    } else {
        let device = devices::resolve(&config.gpu)?;
        let encoder = VideoEncoder::new(&config, &device)?;
        (device, encoder)
    };
    *status.adapter.lock().unwrap() = device.name.clone();
    *status.transfer.lock().unwrap() = encoder.transfer_name().to_owned();
    *status.decoder.lock().unwrap() = encoder.decoder_name().to_owned();
    let mut sound = if audio.is_some() {
        Some(AudioEncoder::new()?)
    } else {
        None
    };
    if status.stop.load(Ordering::Acquire) {
        status.phase.store(5, Ordering::Release);
        return Ok(());
    }
    let mut output = muxer::Muxer::new(
        &config.path,
        &encoder.context,
        sound.as_ref().map(|sound| &sound.context),
    )?;
    let mut packet = Packet::new()?;
    let origin = input::monotonic_ns();
    if let Some(audio) = &audio {
        audio.activate()?;
    }
    struct ActiveAudio(Option<Arc<input::AudioInput>>);
    impl Drop for ActiveAudio {
        fn drop(&mut self) {
            if let Some(audio) = &self.0 {
                audio.active.store(false, Ordering::Release);
            }
        }
    }
    let _audio_guard = ActiveAudio(audio.clone());
    video
        .shared_frames
        .store(encoder.experimental(), Ordering::Release);
    if let Err(error) = tap.attach(&video) {
        output.cancel()?;
        return Err(error);
    }
    let _ = status
        .phase
        .compare_exchange(0, 1, Ordering::AcqRel, Ordering::Acquire);
    let mut last_video = Instant::now();
    let mut last_audio = Instant::now();
    let mut overload_at = Instant::now();
    let mut previous_drops = 0;
    let result = (|| {
        loop {
            let stopping = status.stop.load(Ordering::Acquire);
            if stopping {
                video.active.store(false, Ordering::Release);
                if let Some(audio) = &audio {
                    audio.active.store(false, Ordering::Release);
                }
            }
            let mut worked = false;
            if let Some(block) = video.take() {
                worked = true;
                if block.timestamp >= origin {
                    let pts = unsafe {
                        ffi::av_rescale_q(
                            (block.timestamp - origin).min(i64::MAX as u64) as i64,
                            ffi::AVRational {
                                num: 1,
                                den: 1_000_000_000,
                            },
                            (*encoder.context.0).time_base,
                        )
                    };
                    let submitted = if let Some(shared) = &block.shared {
                        encoder.submit_shared(shared, pts)
                    } else {
                        encoder.submit(&block.bytes, pts)
                    };
                    *status.decoder.lock().unwrap() = encoder.decoder_name().to_owned();
                    *status.transfer.lock().unwrap() = encoder.transfer_name().to_owned();
                    video.recycle(block);
                    if submitted? {
                        status.frames.fetch_add(1, Ordering::Relaxed);
                        output.drain(&encoder.context, output.video_stream, &mut packet)?;
                        let ns = unsafe {
                            ffi::av_rescale_q(
                                pts,
                                (*encoder.context.0).time_base,
                                ffi::AVRational {
                                    num: 1,
                                    den: 1_000_000_000,
                                },
                            )
                        };
                        status.duration.store(ns.max(0) as u64, Ordering::Relaxed);
                        last_video = Instant::now();
                    }
                } else {
                    video.recycle(block);
                }
            }
            if let (Some(audio), Some(sound)) = (&audio, &mut sound) {
                for _ in 0..64 {
                    let Some(block) = audio.take() else {
                        break;
                    };
                    worked = true;
                    last_audio = Instant::now();
                    sound.push(&block, origin)?;
                    while sound.submit(false)? {
                        output.drain(&sound.context, output.audio_stream, &mut packet)?;
                    }
                }
                if audio.overflow.load(Ordering::Acquire) {
                    return Err("The bounded recording audio queue overflowed.".into());
                }
            }
            let dropped = video.dropped.load(Ordering::Relaxed);
            status.dropped.store(dropped, Ordering::Relaxed);
            status.bytes.store(output.bytes(), Ordering::Relaxed);
            if video.oversized.load(Ordering::Acquire) {
                return Err("Recording input exceeded its reserved capacity.".into());
            }
            if stopping && !worked {
                break;
            }
            if !stopping && last_video.elapsed() > Duration::from_secs(3) {
                return Err("Video capture stopped delivering recording frames.".into());
            }
            if !stopping && audio.is_some() && last_audio.elapsed() > Duration::from_secs(3) {
                return Err("Audio capture stopped delivering recording samples.".into());
            }
            if overload_at.elapsed() > Duration::from_secs(2) {
                if dropped.saturating_sub(previous_drops)
                    > u64::from(config.video.interval_denominator / config.video.interval_numerator)
                        .max(1)
                {
                    return Err(
                        "The recording encoder cannot sustain the selected capture rate.".into(),
                    );
                }
                previous_drops = dropped;
                overload_at = Instant::now();
            }
            if !worked {
                let mut descriptors = [
                    ffi::pollfd {
                        fd: video.signal.descriptor(),
                        events: 1,
                        revents: 0,
                    },
                    ffi::pollfd {
                        fd: -1,
                        events: 1,
                        revents: 0,
                    },
                ];
                if let Some(audio) = &audio
                    && let Some(signal) = &audio.signal
                {
                    descriptors[1].fd = signal.descriptor();
                }
                let code =
                    unsafe { ffi::poll(descriptors.as_mut_ptr(), descriptors.len() as _, 100) };
                if code < 0
                    && std::io::Error::last_os_error().kind() != std::io::ErrorKind::Interrupted
                {
                    return Err("Wait for recording input failed.".into());
                }
                video.signal.clear();
                if let Some(audio) = &audio
                    && let Some(signal) = &audio.signal
                {
                    signal.clear();
                }
            }
        }
        Ok(())
    })();
    video.active.store(false, Ordering::Release);
    if let Some(audio) = &audio {
        audio.active.store(false, Ordering::Release);
    }
    if status.frames.load(Ordering::Relaxed) == 0 {
        if result.is_ok() {
            output.cancel()?;
            status.phase.store(5, Ordering::Release);
        }
        return result;
    }
    // Delayed encoder packets are drained even after an input failure when the output still works.
    let finish = (|| {
        if let Some(sound) = &mut sound {
            sound.flush_resampler()?;
            while sound.submit(true)? {
                output.drain(&sound.context, output.audio_stream, &mut packet)?;
            }
            checked(
                unsafe { ffi::avcodec_send_frame(sound.context.0, ptr::null()) },
                "Flush recording audio",
            )?;
            output.drain(&sound.context, output.audio_stream, &mut packet)?;
        }
        checked(
            unsafe { ffi::avcodec_send_frame(encoder.context.0, ptr::null()) },
            "Flush recording video",
        )?;
        output.drain(&encoder.context, output.video_stream, &mut packet)?;
        output.finish()?;
        status.bytes.store(output.bytes(), Ordering::Relaxed);
        Ok(())
    })();
    result.and(finish)
}

/// # Safety
/// Configuration strings are NUL-terminated and valid for this synchronous call. Capture handles
/// remain live throughout this call; serialized control prevents concurrent destruction.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_recording_create(
    video: *mut c_void,
    dma: i32,
    audio: *mut c_void,
    config: *const ffi::mgfr_recording_config,
    error: *mut *mut c_char,
) -> *mut c_void {
    if !error.is_null() {
        unsafe {
            *error = ptr::null_mut();
        }
    }
    let result = (|| {
        if config.is_null() || video.is_null() {
            return Err("A running video capture and recording configuration are required.".into());
        }
        let config = unsafe { &*config };
        if config.abi_version != 1
            || config.path.is_null()
            || config.gpu_id.is_null()
            || ![0, 1].contains(&config.rate_control)
        {
            return Err("The recording ABI or configuration is invalid.".into());
        }
        let configuration = Configuration {
            path: unsafe { CStr::from_ptr(config.path) }
                .to_string_lossy()
                .into_owned(),
            gpu: unsafe { CStr::from_ptr(config.gpu_id) }
                .to_string_lossy()
                .into_owned(),
            codec: config.codec,
            constant_rate: config.rate_control == 1,
            bitrate: config.bitrate,
            video: config.video.into(),
            color_range: config.color_range,
        };
        let input = input::VideoInput::new(configuration.validate()?)?;
        let tap = if dma != 0 {
            unsafe { crate::capture::infrastructure::dmabuf::recording_tap(video) }
        } else {
            unsafe { crate::capture::infrastructure::v4l2::recording_tap(video) }
        };
        let audio_input = if config.record_audio != 0 {
            if audio.is_null() {
                return Err(
                    "Select and start the capture-card audio input before recording audio.".into(),
                );
            }
            Some(unsafe { crate::audio::infrastructure::pipewire::recording_input(audio) })
        } else {
            None
        };
        let status = Arc::new(Status {
            phase: AtomicI32::new(0),
            stop: AtomicBool::new(false),
            frames: AtomicU64::new(0),
            dropped: AtomicU64::new(0),
            bytes: AtomicU64::new(0),
            duration: AtomicU64::new(0),
            error: Mutex::new(String::new()),
            adapter: Mutex::new(String::new()),
            decoder: Mutex::new(String::new()),
            transfer: Mutex::new(String::new()),
        });
        let worker_status = Arc::clone(&status);
        let worker_input = Arc::clone(&input);
        let worker_config = configuration.clone();
        let worker = thread::Builder::new()
            .name("mygoframe-record".into())
            .spawn(move || {
                let outcome = run(
                    worker_config,
                    Arc::clone(&worker_input),
                    tap,
                    audio_input,
                    &worker_status,
                );
                worker_input.active.store(false, Ordering::Release);
                if let Err(error) = outcome {
                    *worker_status.error.lock().unwrap() = error;
                    worker_status.phase.store(4, Ordering::Release);
                } else if worker_status.phase.load(Ordering::Acquire) != 5 {
                    worker_status.phase.store(3, Ordering::Release);
                }
            })
            .map_err(|error| format!("Start recording worker: {error}"))?;
        Ok(Recording {
            status,
            input: Arc::downgrade(&input),
            worker: Some(worker),
            config: configuration,
        })
    })();
    match result {
        Ok(recording) => Box::into_raw(Box::new(recording)).cast(),
        Err(message) => {
            if !error.is_null() {
                unsafe {
                    *error = make_c_string(message);
                }
            }
            ptr::null_mut()
        }
    }
}

/// # Safety
/// The recording handle is live and serialized with its other control calls.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_recording_stop(recording: *mut c_void) {
    if !recording.is_null() {
        unsafe { &*recording.cast::<Recording>() }.request_stop();
    }
}
/// # Safety
/// The handle is destroyed exactly once after all control callers stop using it.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_recording_destroy(recording: *mut c_void) {
    if !recording.is_null() {
        unsafe {
            drop(Box::from_raw(recording.cast::<Recording>()));
        }
    }
}
/// # Safety
/// The handle remains live throughout this synchronous snapshot.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_recording_status_json(recording: *mut c_void) -> *mut c_char {
    if recording.is_null() {
        return make_c_string("{\"phase\":\"idle\",\"frames\":0,\"droppedFrames\":0,\"bytes\":0,\"durationNanoseconds\":0,\"error\":\"\"}".into());
    }
    let value = unsafe { &*recording.cast::<Recording>() };
    let status = &value.status;
    let phase = [
        "starting",
        "recording",
        "stopping",
        "completed",
        "failed",
        "cancelled",
    ][status.phase.load(Ordering::Acquire).clamp(0, 5) as usize];
    make_c_string(format!(
        "{{\"phase\":{},\"frames\":{},\"droppedFrames\":{},\"bytes\":{},\"durationNanoseconds\":{},\"error\":{},\"path\":{},\"gpu\":{},\"codec\":{},\"decoder\":{},\"transfer\":{}}}",
        json_string(phase),
        status.frames.load(Ordering::Relaxed),
        status.dropped.load(Ordering::Relaxed),
        status.bytes.load(Ordering::Relaxed),
        status.duration.load(Ordering::Relaxed),
        json_string(&status.error.lock().unwrap()),
        json_string(&value.config.path),
        json_string(&status.adapter.lock().unwrap()),
        json_string(["h264", "hevc", "av1"][value.config.codec as usize]),
        json_string(&status.decoder.lock().unwrap()),
        json_string(&status.transfer.lock().unwrap())
    ))
}

#[unsafe(no_mangle)]
pub extern "C" fn mgfr_recording_devices_json() -> *mut c_char {
    let mut devices = Vec::new();
    let discovered = match devices::discover() {
        Ok(devices) => devices,
        Err(error) => return make_c_string(format!("{{\"error\":{}}}", json_string(&error))),
    };
    for device in discovered {
        let mut codecs = Vec::new();
        let mut failures = Vec::new();
        for codec in 0..=2 {
            let config = Configuration {
                path: "/unused.mkv".into(),
                gpu: device.id.clone(),
                codec,
                constant_rate: false,
                bitrate: 8_000_000,
                video: ffi::mgfr_video_info {
                    fourcc: u32::from_le_bytes(*b"NV12"),
                    width: 1280,
                    height: 720,
                    stride: 5120,
                    interval_numerator: 1,
                    interval_denominator: 60,
                    bytes_per_line: 1280,
                    quantization: 2,
                    encoding: 2,
                    colorspace: 3,
                    transfer_function: 1,
                    capture_buffers: 0,
                }
                .into(),
                color_range: 0,
            };
            match VideoEncoder::probe(&config, &device) {
                Ok(_) => codecs.push(json_string(["h264", "hevc", "av1"][codec as usize])),
                Err(error) => failures.push(format!(
                    "{}: {error}",
                    ["h264", "hevc", "av1"][codec as usize]
                )),
            }
        }
        devices.push(format!(
            "{{\"id\":{},\"name\":{},\"backend\":{},\"pci\":{},\"codecs\":[{}],\"error\":{}}}",
            json_string(&device.id),
            json_string(&device.name),
            json_string(&device.backend),
            json_string(&device.pci),
            codecs.join(","),
            json_string(&failures.join("; "))
        ));
    }
    make_c_string(format!("[{}]", devices.join(",")))
}

impl From<ffi::mgfr_video_info> for VideoFormat {
    fn from(value: ffi::mgfr_video_info) -> Self {
        Self {
            fourcc: value.fourcc,
            width: value.width,
            height: value.height,
            bytes_per_line: value.bytes_per_line,
            interval_numerator: value.interval_numerator,
            interval_denominator: value.interval_denominator,
            quantization: value.quantization,
            encoding: value.encoding,
            colorspace: value.colorspace,
            transfer_function: value.transfer_function,
        }
    }
}
