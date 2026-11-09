use crate::frame_pool::frame_layout;

pub(crate) const MAX_BUFFER_BYTES: usize = 128 * 1024 * 1024;

// These discriminants select the matching byte unpacking branch in the GLES shader.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(i32)]
pub(crate) enum DmaFormat {
    Nv12 = 0,
    P010 = 1,
    Yuyv = 2,
    Uyvy = 3,
    Bgr24 = 4,
    Rgb24 = 5,
}

impl DmaFormat {
    pub(crate) fn from_fourcc(fourcc: u32) -> Result<Self, String> {
        match fourcc.to_le_bytes() {
            [b'N', b'V', b'1', b'2'] => Ok(Self::Nv12),
            [b'P', b'0', b'1', b'0'] => Ok(Self::P010),
            [b'Y', b'U', b'Y', b'V'] => Ok(Self::Yuyv),
            [b'U', b'Y', b'V', b'Y'] => Ok(Self::Uyvy),
            [b'B', b'G', b'R', b'3'] => Ok(Self::Bgr24),
            [b'R', b'G', b'B', b'3'] => Ok(Self::Rgb24),
            [b'M', b'J', b'P', b'G'] => {
                Err("MJPEG requires decoding and uses the CPU video backend.".into())
            }
            _ => Err("This pixel format has no DMA-BUF conversion path.".into()),
        }
    }

    pub(crate) fn is_rgb(self) -> bool {
        matches!(self, Self::Bgr24 | Self::Rgb24)
    }

    pub(crate) fn is_planar(self) -> bool {
        matches!(self, Self::Nv12 | Self::P010)
    }

    pub(crate) fn row_bytes(self, width: u32, height: u32) -> Result<u32, String> {
        if frame_layout(width, height).is_none()
            || (!self.is_rgb() && width & 1 != 0)
            || (self.is_planar() && height & 1 != 0)
        {
            return Err("Invalid DMA-BUF dimensions for the selected pixel format.".into());
        }
        let bytes_per_pixel = match self {
            Self::Nv12 => 1,
            Self::Bgr24 | Self::Rgb24 => 3,
            _ => 2,
        };
        width
            .checked_mul(bytes_per_pixel)
            .ok_or_else(|| "DMA row size overflowed.".into())
    }

    pub(crate) fn layout(self, width: u32, height: u32, stride: u32) -> Result<DmaLayout, String> {
        let row_bytes = self.row_bytes(width, height)?;
        if stride < row_bytes || stride > i32::MAX as u32 || (self == Self::P010 && stride & 1 != 0)
        {
            return Err("Invalid DMA-BUF stride for the selected pixel format.".into());
        }
        let first_plane = u64::from(stride) * u64::from(height);
        let uv_offset = if self.is_planar() { first_plane } else { 0 };
        let required = first_plane + if self.is_planar() { first_plane / 2 } else { 0 };
        if required > MAX_BUFFER_BYTES as u64 {
            return Err("The source layout exceeds the DMA buffer memory budget.".into());
        }
        Ok(DmaLayout {
            row_bytes,
            uv_offset,
            byte_length: required as usize,
        })
    }
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct DmaLayout {
    pub(crate) row_bytes: u32,
    pub(crate) uv_offset: u64,
    pub(crate) byte_length: usize,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn raw_layouts_account_for_padding_and_chroma() {
        for (format, width, height, stride, offset, length) in [
            (DmaFormat::Nv12, 4, 2, 8, 16, 24),
            (DmaFormat::P010, 4, 2, 12, 24, 36),
            (DmaFormat::Yuyv, 4, 3, 12, 0, 36),
            (DmaFormat::Uyvy, 4, 3, 12, 0, 36),
            (DmaFormat::Bgr24, 3, 3, 11, 0, 33),
            (DmaFormat::Rgb24, 3, 3, 11, 0, 33),
        ] {
            let layout = format.layout(width, height, stride).unwrap();
            assert_eq!((layout.uv_offset, layout.byte_length), (offset, length));
            assert!(format.layout(width, height, layout.row_bytes - 1).is_err());
            assert!(format.layout(width, height, u32::MAX).is_err());
            assert!(format.layout(0, height, stride).is_err());
        }
    }

    #[test]
    fn raw_layouts_reject_subsampling_and_allocation_overflow() {
        for format in [
            DmaFormat::Nv12,
            DmaFormat::P010,
            DmaFormat::Yuyv,
            DmaFormat::Uyvy,
        ] {
            assert!(format.layout(3, 2, 16).is_err());
        }
        for format in [DmaFormat::Nv12, DmaFormat::P010] {
            assert!(format.layout(4, 3, 16).is_err());
        }
        assert!(DmaFormat::P010.layout(4, 2, 9).is_err());
        assert!(DmaFormat::Nv12.layout(4, 2, 100_000_000).is_err());
        assert!(
            DmaFormat::Rgb24
                .layout(u32::MAX, u32::MAX, u32::MAX)
                .is_err()
        );
        assert!(
            DmaFormat::from_fourcc(u32::from_le_bytes(*b"MJPG"))
                .unwrap_err()
                .contains("decoding")
        );
        assert!(DmaFormat::from_fourcc(u32::from_le_bytes(*b"NM12")).is_err());
    }
}
