use super::codec::{Buffer, Frame, checked};
use crate::{ffi, media::infrastructure::packets::SharedVideoFrame};
use std::{os::fd::AsRawFd, ptr, sync::Arc};

pub(super) struct VulkanUpload {
    frames: Buffer,
    drm_frames: Buffer,
    import_enabled: bool,
}
struct Source {
    descriptor: ffi::AVDRMFrameDescriptor,
    _lease: Arc<dyn SharedVideoFrame>,
}
unsafe extern "C" fn release_source(opaque: *mut std::ffi::c_void, _: *mut u8) {
    unsafe {
        drop(Box::from_raw(opaque.cast::<Source>()));
    }
}
impl VulkanUpload {
    pub fn new(cuda: &Frame, width: i32, height: i32) -> Result<Self, String> {
        unsafe {
            let cuda_frames = (*(*cuda.0).hw_frames_ctx)
                .data
                .cast::<ffi::AVHWFramesContext>();
            let mut device = Buffer(ptr::null_mut());
            // Derivation matches the physical GPU UUID and enables separate plane images.
            checked(
                ffi::av_hwdevice_ctx_create_derived(
                    &mut device.0,
                    ffi::AVHWDeviceType_AV_HWDEVICE_TYPE_VULKAN,
                    (*cuda_frames).device_ref,
                    0,
                ),
                "Derive Vulkan recording GPU from CUDA",
            )?;
            let frames = Buffer(ffi::av_hwframe_ctx_alloc(device.0));
            if frames.0.is_null() {
                return Err("Allocate Vulkan recording pool.".into());
            }
            let storage = (*frames.0).data.cast::<ffi::AVHWFramesContext>();
            (*storage).format = ffi::AVPixelFormat_AV_PIX_FMT_VULKAN;
            (*storage).sw_format = ffi::AVPixelFormat_AV_PIX_FMT_NV12;
            (*storage).width = width;
            (*storage).height = height;
            (*storage).initial_pool_size = 3;
            checked(
                ffi::av_hwframe_ctx_init(frames.0),
                "Initialize Vulkan recording pool",
            )?;
            let device = Buffer(ffi::av_hwdevice_ctx_alloc(
                ffi::AVHWDeviceType_AV_HWDEVICE_TYPE_DRM,
            ));
            if device.0.is_null() {
                return Err("Allocate DRM recording context.".into());
            }
            checked(
                ffi::av_hwdevice_ctx_init(device.0),
                "Initialize DRM recording context",
            )?;
            let drm_frames = Buffer(ffi::av_hwframe_ctx_alloc(device.0));
            if drm_frames.0.is_null() {
                return Err("Allocate DRM recording frame context.".into());
            }
            let storage = (*drm_frames.0).data.cast::<ffi::AVHWFramesContext>();
            (*storage).format = ffi::AVPixelFormat_AV_PIX_FMT_DRM_PRIME;
            (*storage).sw_format = ffi::AVPixelFormat_AV_PIX_FMT_NV12;
            (*storage).width = width;
            (*storage).height = height;
            checked(
                ffi::av_hwframe_ctx_init(drm_frames.0),
                "Initialize DRM recording frames",
            )?;
            Ok(Self {
                frames,
                drm_frames,
                import_enabled: true,
            })
        }
    }
    pub fn transfer(
        &mut self,
        source: &Frame,
        shared: Option<&Arc<dyn SharedVideoFrame>>,
        target: &Frame,
        stride: usize,
        uv_offset: usize,
        allow_upload: bool,
    ) -> Result<&'static str, String> {
        if self.import_enabled {
            if let Some(shared) = shared {
                match self.import(source, shared, target, stride, uv_offset) {
                    Ok(()) => return Ok("vulkan-dmabuf-cuda"),
                    Err(error) => {
                        self.import_enabled = false;
                        eprintln!("Experimental recording Vulkan DMA-BUF disabled: {error}");
                    }
                }
            }
        }
        if !allow_upload {
            return Err(
                "Vulkan DMA-BUF interop is unavailable for this capture allocation.".into(),
            );
        }
        let frame = Frame::new()?;
        unsafe {
            checked(
                ffi::av_hwframe_get_buffer(self.frames.0, frame.0, 0),
                "Acquire Vulkan recording surface",
            )?;
            checked(
                ffi::av_hwframe_transfer_data(frame.0, source.0, 0),
                "Upload recording frame to Vulkan",
            )?;
            let context = super::cuda_host::Context::enter(target)?;
            let result = ffi::av_hwframe_transfer_data(target.0, frame.0, 0);
            context.finish();
            checked(result, "Transfer Vulkan recording surface to CUDA")?;
        }
        Ok("vulkan-upload-cuda")
    }
    fn import(
        &self,
        source: &Frame,
        shared: &Arc<dyn SharedVideoFrame>,
        target: &Frame,
        stride: usize,
        uv_offset: usize,
    ) -> Result<(), String> {
        shared.prepare_gpu_read()?;
        let mut owner = Box::new(Source {
            descriptor: unsafe { std::mem::zeroed() },
            _lease: Arc::clone(shared),
        });
        owner.descriptor.nb_objects = 1;
        owner.descriptor.objects[0].fd = shared.descriptor().as_raw_fd();
        owner.descriptor.objects[0].size = shared.allocation_size();
        owner.descriptor.objects[0].format_modifier = 0;
        // Separate R8 and GR88 layers keep CUDA-compatible single-plane Vulkan images.
        owner.descriptor.nb_layers = 2;
        for (index, format, offset) in [
            (0, u32::from_le_bytes(*b"R8  "), 0),
            (1, u32::from_le_bytes(*b"GR88"), uv_offset),
        ] {
            let layer = &mut owner.descriptor.layers[index];
            layer.format = format;
            layer.nb_planes = 1;
            layer.planes[0].object_index = 0;
            layer.planes[0].offset = offset as isize;
            layer.planes[0].pitch = stride as isize;
        }
        let drm = Frame::new()?;
        let mapped = Frame::new()?;
        unsafe {
            let pointer = (&mut owner.descriptor as *mut ffi::AVDRMFrameDescriptor).cast::<u8>();
            let opaque = Box::into_raw(owner);
            let buffer = ffi::av_buffer_create(
                pointer,
                std::mem::size_of::<ffi::AVDRMFrameDescriptor>(),
                Some(release_source),
                opaque.cast(),
                ffi::AV_BUFFER_FLAG_READONLY as i32,
            );
            if buffer.is_null() {
                drop(Box::from_raw(opaque));
                return Err("Retain DMA-BUF recording descriptor.".into());
            }
            (*drm.0).buf[0] = buffer;
            (*drm.0).data[0] = pointer;
            (*drm.0).format = ffi::AVPixelFormat_AV_PIX_FMT_DRM_PRIME;
            (*drm.0).width = (*source.0).width;
            (*drm.0).height = (*source.0).height;
            (*drm.0).hw_frames_ctx = ffi::av_buffer_ref(self.drm_frames.0);
            (*mapped.0).hw_frames_ctx = ffi::av_buffer_ref(self.frames.0);
            if (*drm.0).hw_frames_ctx.is_null() || (*mapped.0).hw_frames_ctx.is_null() {
                return Err("Retain DMA-BUF recording contexts.".into());
            }
            (*mapped.0).format = ffi::AVPixelFormat_AV_PIX_FMT_VULKAN;
            checked(
                ffi::av_hwframe_map(
                    mapped.0,
                    drm.0,
                    (ffi::AV_HWFRAME_MAP_READ | ffi::AV_HWFRAME_MAP_DIRECT) as i32,
                ),
                "Import recording DMA-BUF into Vulkan",
            )?;
            let context = super::cuda_host::Context::enter(target)?;
            let result = ffi::av_hwframe_transfer_data(target.0, mapped.0, 0);
            context.finish();
            checked(result, "Transfer imported Vulkan frame to CUDA")?;
        }
        Ok(())
    }
}
