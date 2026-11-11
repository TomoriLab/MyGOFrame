use super::*;
use crate::media::infrastructure::packets as input;

fn configuration(format: &[u8; 4], codec: i32) -> Configuration {
    let width = 1280;
    let mut video: ffi::mgfr_video_info = unsafe { std::mem::zeroed() };
    video.width = width;
    video.height = 720;
    video.fourcc = u32::from_le_bytes(*format);
    video.bytes_per_line = width
        * match format {
            b"P010" | b"YUYV" | b"UYVY" => 2,
            b"RGB3" | b"BGR3" => 3,
            _ => 1,
        };
    video.interval_numerator = 1;
    video.interval_denominator = 60;
    video.colorspace = ffi::v4l2_colorspace_V4L2_COLORSPACE_REC709;
    Configuration {
        path: "/tmp/recording-test.mkv".into(),
        gpu: "auto".into(),
        codec,
        constant_rate: false,
        bitrate: 40_000_000,
        video: video.into(),
        color_range: 1,
    }
}

#[test]
#[ignore = "Requires NVIDIA hardware encoding for all three codecs."]
fn hardware_codec_probe_has_no_transfer_resources() {
    let device = super::super::devices::resolve("auto").unwrap();
    assert_eq!(device.backend, "nvenc");
    for codec in 0..3 {
        let config = configuration(b"NV12", codec);
        let encoder = super::VideoEncoder::probe(&config, &device).unwrap();
        assert!(!encoder.experimental());
        assert!(encoder.vulkan.is_none());
        assert!(encoder.hardware.is_none());
        assert_eq!(encoder.experiment_mode, "off");
        assert_ne!(encoder.transfer_name(), "experimental-pending");
    }
}

#[test]
#[ignore = "Requires NVIDIA CUDA and optional Vulkan interop on the selected GPU."]
fn experimental_nv12_upload_roundtrip() {
    let mut config = configuration(b"NV12", 0);
    config.video.width = 3840;
    config.video.height = 2160;
    config.video.bytes_per_line = 4096;
    let device = super::super::devices::resolve("auto").unwrap();
    let mut encoder = super::VideoEncoder::new(&config, &device).unwrap();
    assert!(
        encoder.experimental(),
        "Set MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER"
    );
    let mut bytes = vec![0; config.validate().unwrap()];
    for (index, byte) in bytes.iter_mut().enumerate() {
        *byte = ((index * 37 + index / 4096 * 13) % 256) as u8;
    }
    assert!(encoder.submit(&bytes, 0).unwrap());
    if std::env::var("MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER").as_deref() == Ok("vulkan") {
        assert_eq!(
            encoder.transfer_name, "vulkan-upload-cuda",
            "Vulkan test must not pass through fallback"
        );
    }
    let downloaded = super::Frame::new().unwrap();
    unsafe {
        super::checked(
            crate::ffi::av_hwframe_transfer_data(
                downloaded.0,
                encoder.hardware.as_ref().unwrap().0,
                0,
            ),
            "Download test CUDA frame",
        )
        .unwrap();
        for (plane, offset, rows) in [(0, 0, 2160), (1, encoder.uv_offset, 1080)] {
            for row in 0..rows {
                let actual = std::slice::from_raw_parts(
                    (*downloaded.0).data[plane].add(row * (*downloaded.0).linesize[plane] as usize),
                    3840,
                );
                assert_eq!(
                    actual,
                    &bytes[offset + row * 4096..offset + row * 4096 + 3840],
                    "NV12 plane {plane} row {row}"
                );
            }
        }
    }
}

#[test]
fn limited_rgb_endpoints_and_midpoint() {
    assert_eq!(super::expand_limited_rgb(0), 0);
    assert_eq!(super::expand_limited_rgb(16), 0);
    assert_eq!(super::expand_limited_rgb(126), 128);
    assert_eq!(super::expand_limited_rgb(235), 255);
    assert_eq!(super::expand_limited_rgb(255), 255);
}

#[test]
fn audio_encoder_tracks_a_synthetic_hour_with_clock_drift() {
    for drift in [-100_i64, 100] {
        let mut encoder = AudioEncoder::new().unwrap();
        let packet = Packet::new().unwrap();
        let origin = 1_000_000_000_u64;
        let mut captured = 0_u64;
        // Accelerated silent input tests timestamp correction without requiring a live audio device.
        for _ in 0..(48000_u64 * 3600 / 2048) {
            let timestamp = origin
                + ((captured as u128 * 1_000_000_000 * (1_000_000 + drift) as u128)
                    / (48000 * 1_000_000)) as u64;
            let block = input::AudioBlock {
                timestamp,
                frames: 2048,
                samples: [0.0; 4096],
            };
            encoder.push(&block, origin).unwrap();
            while encoder.submit(false).unwrap() {
                loop {
                    let result =
                        unsafe { ffi::avcodec_receive_packet(encoder.context.0, packet.0) };
                    if result == -11 {
                        break;
                    }
                    checked(result, "Drain test audio").unwrap();
                    unsafe {
                        ffi::av_packet_unref(packet.0);
                    }
                }
            }
            captured += 2048;
        }
        encoder.flush_resampler().unwrap();
        while encoder.submit(true).unwrap() {
            while unsafe { ffi::avcodec_receive_packet(encoder.context.0, packet.0) } >= 0 {
                unsafe {
                    ffi::av_packet_unref(packet.0);
                }
            }
        }
        checked(
            unsafe { ffi::avcodec_send_frame(encoder.context.0, ptr::null()) },
            "Flush test audio",
        )
        .unwrap();
        loop {
            let code = unsafe { ffi::avcodec_receive_packet(encoder.context.0, packet.0) };
            if code == ffi::MGF_AVERROR_EOF {
                break;
            }
            checked(code, "Drain final test audio").unwrap();
            unsafe {
                ffi::av_packet_unref(packet.0);
            }
        }
        let expected = (captured as i128 * (1_000_000 + drift) as i128 / 1_000_000) as i64;
        let actual = encoder.emitted_samples();
        assert!(
            (actual - expected).abs() < 800,
            "Audio drift={drift}ppm actual={actual} expected={expected}"
        );
    }
}

#[test]
fn capture_timestamp_precision_preserves_frames_at_rounding_boundaries() {
    let nanoseconds = ffi::AVRational {
        num: 1,
        den: 1_000_000_000,
    };
    // These adjacent timestamps reproduce the collision from the 4K60 device trace.
    let input = [75_000_333, 91_665_333];
    let old: Vec<_> = input
        .iter()
        .map(|timestamp| unsafe {
            ffi::av_rescale_q(*timestamp, nanoseconds, ffi::AVRational { num: 1, den: 60 })
        })
        .collect();
    assert_eq!(old, [5, 5]);
    let precise: Vec<_> = input
        .iter()
        .map(|timestamp| unsafe { ffi::av_rescale_q(*timestamp, nanoseconds, VIDEO_TIME_BASE) })
        .collect();
    assert_eq!(precise, [75_000, 91_665]);

    for (numerator, denominator) in [(1_i64, 24_i64), (1, 60), (1001, 60000), (1, 240)] {
        let mut previous = -1;
        for index in 0..600 {
            let timestamp = 3_600_000_000_000
                + (2 * index + 1) * numerator * 1_000_000_000 / (2 * denominator)
                + if index % 2 == 0 { 1000 } else { -1000 };
            let pts = unsafe { ffi::av_rescale_q(timestamp, nanoseconds, VIDEO_TIME_BASE) };
            assert!(pts > previous);
            assert!((pts * 1000 - timestamp).abs() <= 500);
            previous = pts;
        }
    }
    // A missing input remains a gap rather than being concealed by sequential frame numbering.
    let before = unsafe { ffi::av_rescale_q(10_000_000, nanoseconds, VIDEO_TIME_BASE) };
    let after = unsafe { ffi::av_rescale_q(60_000_000, nanoseconds, VIDEO_TIME_BASE) };
    assert_eq!(after - before, 50_000);
}

#[test]
#[ignore = "Requires hardware encoding and a JPEG fixture for internal format conversion checks."]
fn hardware_encoder_converts_raw_and_mjpeg_inputs() {
    let device = super::super::devices::resolve(
        &std::env::var("RECORDING_TEST_GPU").unwrap_or("auto".into()),
    )
    .unwrap();
    for codec in 0..3 {
        for format in [
            b"NV12", b"P010", b"YUYV", b"UYVY", b"BGR3", b"RGB3", b"MJPG",
        ] {
            let config = configuration(format, codec);
            let capacity = config.validate().unwrap();
            let mut bytes = vec![128_u8; capacity];
            if format == b"MJPG" {
                bytes = std::fs::read(
                    std::env::var("ENCODER_TEST_JPEG")
                        .expect("Set ENCODER_TEST_JPEG to a 1280x720 JPEG"),
                )
                .unwrap();
                bytes.resize(bytes.len() + 64, 0);
            } else if format == b"P010" {
                for pixel in bytes[..capacity - 64].as_chunks_mut::<2>().0 {
                    pixel.copy_from_slice(&(512_u16 << 6).to_le_bytes());
                }
            }
            let mut encoder = VideoEncoder::new(&config, &device).unwrap();
            let packet = Packet::new().unwrap();
            let mut packets = 0;
            for index in 0..12 {
                let pts = unsafe {
                    ffi::av_rescale_q(
                        index,
                        ffi::AVRational { num: 1, den: 60 },
                        (*encoder.context.0).time_base,
                    )
                };
                assert!(encoder.submit(&bytes, pts).unwrap());
                loop {
                    let result =
                        unsafe { ffi::avcodec_receive_packet(encoder.context.0, packet.0) };
                    if result == -11 {
                        break;
                    }
                    checked(result, "Drain converted test input").unwrap();
                    packets += 1;
                    unsafe {
                        ffi::av_packet_unref(packet.0);
                    }
                }
            }
            checked(
                unsafe { ffi::avcodec_send_frame(encoder.context.0, ptr::null()) },
                "Flush test encoder",
            )
            .unwrap();
            loop {
                let result = unsafe { ffi::avcodec_receive_packet(encoder.context.0, packet.0) };
                if result == ffi::MGF_AVERROR_EOF {
                    break;
                }
                checked(result, "Drain final converted input").unwrap();
                packets += 1;
                unsafe {
                    ffi::av_packet_unref(packet.0);
                }
            }
            assert_eq!(packets, 12, "Format {:?}, codec {}", format, codec);
        }
    }
}
