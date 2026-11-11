//! Black-box recording checks use only the shipped C ABI and completed output files.

use std::ffi::{CStr, CString, c_char, c_void};
use std::io::Write;
use std::path::Path;
use std::process::{Command, Stdio};
use std::ptr::{self, NonNull};
use std::thread;
use std::time::{Duration, Instant};

#[allow(
    non_camel_case_types,
    non_snake_case,
    non_upper_case_globals,
    dead_code
)]
mod api {
    include!(concat!(env!("OUT_DIR"), "/public_api.rs"));
}

fn owned_string(pointer: *mut c_char) -> String {
    assert!(!pointer.is_null(), "The public API returned a null string");
    // The public API transfers this allocation, and its matching release is called exactly once.
    unsafe {
        let value = CStr::from_ptr(pointer).to_string_lossy().into_owned();
        mygoframe_media::mgfr_string_free(pointer);
        value
    }
}

fn handle(pointer: *mut c_void, error: *mut c_char) -> NonNull<c_void> {
    let message = if error.is_null() {
        String::new()
    } else {
        owned_string(error)
    };
    NonNull::new(pointer).unwrap_or_else(|| panic!("Public API creation failed: {message}"))
}

struct Capture {
    pointer: NonNull<c_void>,
    dma: bool,
    info: api::mgfr_video_info,
}

impl Capture {
    fn open(device: &str, format: &str, width: u32, height: u32, dma: bool) -> Self {
        let device = CString::new(device).unwrap();
        let fourcc: [u8; 4] = format
            .as_bytes()
            .try_into()
            .expect("A format must have four bytes");
        let config = api::mgfr_video_config {
            path: device.as_ptr(),
            fourcc: u32::from_le_bytes(fourcc),
            width,
            height,
            interval_numerator: 1,
            interval_denominator: 60,
            color_range: 0,
        };
        let mut error = ptr::null_mut();
        // The ABI structure contains only integer fields and accepts a writable output buffer.
        let mut info = unsafe { std::mem::zeroed() };
        let pointer = unsafe {
            if dma {
                api::mgfr_dma_video_create(&config, &mut info, &mut error)
            } else {
                api::mgfr_video_create(&config, &mut info, &mut error)
            }
        };
        let capture = Self {
            pointer: handle(pointer, error),
            dma,
            info,
        };
        assert_eq!(
            (capture.info.width, capture.info.height, capture.info.fourcc),
            (width, height, config.fourcc),
            "Capture negotiated a different mode"
        );
        assert_eq!(
            u64::from(capture.info.interval_denominator),
            60 * u64::from(capture.info.interval_numerator)
        );
        capture
    }
}

impl Drop for Capture {
    fn drop(&mut self) {
        // Recording borrows this owner, so its worker has stopped before capture is destroyed.
        unsafe {
            if self.dma {
                api::mgfr_dma_video_destroy(self.pointer.as_ptr());
            } else {
                api::mgfr_video_destroy(self.pointer.as_ptr());
            }
        }
    }
}

struct Recording<'capture> {
    pointer: NonNull<c_void>,
    _capture: &'capture Capture,
}

impl<'capture> Recording<'capture> {
    fn start(capture: &'capture Capture, path: &Path, gpu: &str, codec: i32) -> Self {
        let path = CString::new(path.to_str().expect("UTF-8 test path")).unwrap();
        let gpu = CString::new(gpu).unwrap();
        let config = api::mgfr_recording_config {
            abi_version: 1,
            path: path.as_ptr(),
            gpu_id: gpu.as_ptr(),
            codec,
            rate_control: 0,
            bitrate: 40_000_000,
            video: capture.info,
            color_range: 0,
            record_audio: 0,
        };
        let mut error = ptr::null_mut();
        // Creation copies configuration strings; the borrowed capture outlives the recording owner.
        let pointer = unsafe {
            api::mgfr_recording_create(
                capture.pointer.as_ptr(),
                i32::from(capture.dma),
                ptr::null_mut(),
                &config,
                &mut error,
            )
        };
        Self {
            pointer: handle(pointer, error),
            _capture: capture,
        }
    }

    fn status(&self) -> (String, u64) {
        let json = owned_string(unsafe { api::mgfr_recording_status_json(self.pointer.as_ptr()) });
        let decoded = python(&["status"], &json);
        let mut fields = decoded.lines();
        let phase = fields.next().expect("Status phase").to_owned();
        let frames = fields.next().expect("Status frames").parse().unwrap();
        (phase, frames)
    }

    fn wait(&self, completed: bool) {
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            let (phase, frames) = self.status();
            if (completed && phase == "completed")
                || (!completed && phase == "recording" && frames >= 120)
            {
                return;
            }
            assert!(
                !matches!(phase.as_str(), "failed" | "cancelled"),
                "Recording ended unexpectedly: {phase}"
            );
            assert!(
                Instant::now() < deadline,
                "Recording timed out in {phase} after {frames} frames"
            );
            thread::sleep(Duration::from_millis(100));
        }
    }
}

impl Drop for Recording<'_> {
    fn drop(&mut self) {
        // Destruction joins the worker on success, assertion failure, and early return.
        unsafe {
            api::mgfr_recording_destroy(self.pointer.as_ptr());
        }
    }
}

fn python(arguments: &[&str], input: &str) -> String {
    // The existing Python test dependency supplies JSON parsing without new Cargo dependencies.
    let mut child = Command::new("python3")
        .arg("-c")
        .arg(include_str!("support/recording_check.py"))
        .args(arguments)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let written = child.stdin.take().unwrap().write_all(input.as_bytes());
    let output = child.wait_with_output().unwrap();
    written.unwrap();
    assert!(
        output.status.success(),
        "Public recording validation failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    String::from_utf8(output.stdout).unwrap()
}

#[test]
fn recording_rejects_missing_capture_through_public_api() {
    let mut error = ptr::null_mut();
    let pointer = unsafe {
        api::mgfr_recording_create(ptr::null_mut(), 0, ptr::null_mut(), ptr::null(), &mut error)
    };
    assert!(pointer.is_null());
    assert!(!owned_string(error).is_empty());
}

#[test]
#[ignore = "Requires an exclusive capture device, hardware encoder, Python, ffprobe, and ffmpeg."]
fn hardware_recording_through_public_api() {
    let device = std::env::var("RECORDING_TEST_DEVICE").expect("Set RECORDING_TEST_DEVICE");
    let output = std::env::var("RECORDING_TEST_OUTPUT").expect("Set RECORDING_TEST_OUTPUT");
    let output = Path::new(&output);
    assert!(output.is_absolute());
    std::fs::create_dir_all(output).unwrap();
    let format = std::env::var("RECORDING_TEST_FORMAT").unwrap_or("NV12".into());
    let width: u32 = std::env::var("RECORDING_TEST_WIDTH")
        .unwrap_or("1280".into())
        .parse()
        .unwrap();
    let height: u32 = std::env::var("RECORDING_TEST_HEIGHT")
        .unwrap_or("720".into())
        .parse()
        .unwrap();
    let backend = std::env::var("RECORDING_TEST_BACKEND").unwrap_or("cpu".into());
    assert!(matches!(backend.as_str(), "cpu" | "dma"));
    let gpu = std::env::var("RECORDING_TEST_GPU").unwrap_or("auto".into());
    let codecs = match std::env::var("RECORDING_TEST_CODEC") {
        Ok(codec) => {
            let codec: i32 = codec.parse().unwrap();
            assert!((0..=2).contains(&codec));
            vec![codec]
        }
        Err(std::env::VarError::NotPresent) => vec![0, 1, 2],
        Err(error) => panic!("Invalid codec selection: {error}"),
    };
    for codec in codecs {
        let path = output.join(format!("{format}-{codec}.mkv"));
        assert!(
            !path.exists() && !path.with_extension("mkv.part").exists(),
            "Use unused output paths"
        );
        let capture = Capture::open(&device, &format, width, height, backend == "dma");
        let recording = Recording::start(&capture, &path, &gpu, codec);
        recording.wait(false);
        unsafe {
            api::mgfr_recording_stop(recording.pointer.as_ptr());
        }
        recording.wait(true);
        let status =
            owned_string(unsafe { api::mgfr_recording_status_json(recording.pointer.as_ptr()) });
        drop(recording);
        drop(capture);
        let result = python(
            &[
                "file",
                path.to_str().unwrap(),
                &width.to_string(),
                &height.to_string(),
                &codec.to_string(),
            ],
            &status,
        );
        eprintln!("{}", result.trim());
    }
}
