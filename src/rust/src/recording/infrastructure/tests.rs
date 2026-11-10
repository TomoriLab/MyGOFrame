use super::*;

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
fn recording_configuration_rejects_overflow_and_invalid_layout() {
    let valid = configuration(b"NV12", 0);
    assert!(valid.validate().is_ok());
    for invalid in [
        Configuration {
            bitrate: 0,
            ..valid.clone()
        },
        Configuration {
            codec: 3,
            ..valid.clone()
        },
        Configuration {
            path: "relative.mkv".into(),
            ..valid.clone()
        },
        Configuration {
            path: "/tmp/output.webm".into(),
            ..valid.clone()
        },
    ] {
        assert!(invalid.validate().is_err());
    }
    let mut invalid = valid.clone();
    invalid.video.interval_denominator = u32::MAX;
    assert!(invalid.validate().is_err());
    invalid = valid;
    invalid.video.width = u32::MAX;
    assert!(invalid.validate().is_err());
}

#[test]
#[ignore = "Requires a hardware encoder and an exclusive test output directory."]
fn hardware_raw_and_mjpeg_recording() {
    let output = std::path::PathBuf::from(
        std::env::var("RECORDING_TEST_OUTPUT").expect("Set RECORDING_TEST_OUTPUT"),
    );
    std::fs::create_dir_all(&output).unwrap();
    let device =
        devices::resolve(&std::env::var("RECORDING_TEST_GPU").unwrap_or("auto".into())).unwrap();
    for codec in 0..3 {
        for format in [
            b"NV12", b"P010", b"YUYV", b"UYVY", b"BGR3", b"RGB3", b"MJPG",
        ] {
            if std::env::var("RECORDING_TEST_FORMAT")
                .is_ok_and(|selected| selected.as_bytes() != format)
            {
                continue;
            }
            if std::env::var("RECORDING_TEST_CODEC")
                .is_ok_and(|selected| selected != codec.to_string())
            {
                continue;
            }
            let mut config = configuration(format, codec);
            config.path = output
                .join(format!("{}-{}.mkv", String::from_utf8_lossy(format), codec))
                .to_string_lossy()
                .into_owned();
            let capacity = config.validate().unwrap();
            let mut input = vec![0_u8; capacity];
            if format == b"MJPG" {
                input = std::fs::read(
                    std::env::var("RECORDING_TEST_JPEG").expect("Set RECORDING_TEST_JPEG"),
                )
                .unwrap();
                input.resize(input.len() + 64, 0);
            } else if format == b"P010" {
                for pixel in input[..capacity - 64].as_chunks_mut::<2>().0 {
                    pixel.copy_from_slice(&(512_u16 << 6).to_le_bytes());
                }
            } else {
                input[..capacity - 64].fill(128);
            }
            let mut encoder = VideoEncoder::new(&config, &device).unwrap();
            let mut muxer = muxer::Muxer::new(&config.path, &encoder.context, None).unwrap();
            let mut packet = Packet::new().unwrap();
            for index in 0..12 {
                assert!(
                    encoder.submit(&input, index).unwrap(),
                    "No frame from {}",
                    encoder.decoder_name()
                );
                muxer
                    .drain(&encoder.context, muxer.video_stream, &mut packet)
                    .unwrap();
            }
            checked(
                unsafe { ffi::avcodec_send_frame(encoder.context.0, ptr::null()) },
                "Flush test encoder",
            )
            .unwrap();
            muxer
                .drain(&encoder.context, muxer.video_stream, &mut packet)
                .unwrap();
            muxer.finish().unwrap();
            eprintln!(
                "RECORDING_HARDWARE {} codec={} decoder={} bytes={}",
                String::from_utf8_lossy(format),
                codec,
                encoder.decoder_name(),
                muxer.bytes()
            );
        }
    }
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
