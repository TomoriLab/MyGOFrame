use super::*;

fn configuration() -> Configuration {
    Configuration {
        path: "/tmp/recording-test.mkv".into(),
        gpu: "auto".into(),
        codec: 0,
        constant_rate: false,
        bitrate: 40_000_000,
        color_range: 1,
        video: VideoFormat {
            fourcc: u32::from_le_bytes(*b"NV12"),
            width: 1280,
            height: 720,
            bytes_per_line: 1280,
            interval_numerator: 1,
            interval_denominator: 60,
            ..VideoFormat::default()
        },
    }
}

#[test]
fn recording_configuration_rejects_overflow_and_invalid_layout() {
    let valid = configuration();
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
