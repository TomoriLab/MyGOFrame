pub(crate) const BUFFER_COUNT: usize = 3;
pub(crate) const MAX_DIMENSION: u32 = 16_384;
pub(crate) const MAX_FRAME_BYTES: usize = 128 * 1024 * 1024;

pub(crate) fn frame_layout(width: u32, height: u32) -> Option<(u32, usize)> {
    if width == 0 || height == 0 || width > MAX_DIMENSION || height > MAX_DIMENSION {
        return None;
    }
    let stride = width.checked_mul(4)?;
    let byte_length = (stride as usize).checked_mul(height as usize)?;
    (byte_length <= MAX_FRAME_BYTES).then_some((stride, byte_length))
}
