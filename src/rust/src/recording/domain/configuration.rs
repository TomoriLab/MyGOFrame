#[derive(Clone, Copy, Default)]
pub(crate) struct VideoFormat {
    pub fourcc: u32,
    pub width: u32,
    pub height: u32,
    pub bytes_per_line: u32,
    pub interval_numerator: u32,
    pub interval_denominator: u32,
    pub quantization: u32,
    pub encoding: u32,
    pub colorspace: u32,
    pub transfer_function: u32,
}
#[derive(Clone)]
pub(crate) struct Configuration {
    pub path: String,
    pub gpu: String,
    pub codec: i32,
    pub constant_rate: bool,
    pub bitrate: u64,
    pub video: VideoFormat,
    pub color_range: i32,
}
impl Configuration {
    pub(crate) fn validate(&self) -> Result<usize, String> {
        if !(0..=2).contains(&self.codec)
            || !(100_000..=1_000_000_000).contains(&self.bitrate)
            || !(0..=2).contains(&self.color_range)
            || self.path.len() > 4096
            || !std::path::Path::new(&self.path).is_absolute()
            || self.video.width == 0
            || self.video.height == 0
            || self.video.width & 1 != 0
            || self.video.height & 1 != 0
            || self.video.interval_numerator == 0
            || self.video.interval_denominator == 0
            || self.video.interval_numerator > i32::MAX as u32
            || self.video.interval_denominator > i32::MAX as u32
        {
            return Err(
                "Invalid recording codec, bitrate, path, dimensions, or rational frame interval."
                    .into(),
            );
        }
        let extension = std::path::Path::new(&self.path)
            .extension()
            .and_then(|value| value.to_str())
            .unwrap_or("");
        if !extension.eq_ignore_ascii_case("mkv") && !extension.eq_ignore_ascii_case("mp4") {
            return Err("Recording output must use .mkv or .mp4.".into());
        }
        if self.video.fourcc == u32::from_le_bytes(*b"MJPG") {
            return crate::capture::domain::layout::frame_layout(
                self.video.width,
                self.video.height,
            )
            .map(|layout| layout.1 + 64)
            .ok_or_else(|| "Recording JPEG dimensions exceed the memory budget.".into());
        }
        let format = crate::capture::domain::format::DmaFormat::from_fourcc(self.video.fourcc)?;
        let layout = format.layout(
            self.video.width,
            self.video.height,
            self.video.bytes_per_line,
        )?;
        layout
            .byte_length
            .checked_add(64)
            .ok_or_else(|| "Recording capacity overflowed.".into())
    }
}
