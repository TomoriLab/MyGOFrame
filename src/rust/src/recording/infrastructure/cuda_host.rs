//! Driver ABI declarations avoid a build-time CUDA toolkit dependency.
use super::codec::Frame;
use crate::{ffi, media::infrastructure::packets::SharedVideoFrame};
use std::ffi::{c_char, c_void};
use std::os::fd::AsRawFd;
use std::ptr;

unsafe extern "C" {
    fn dlsym(handle: *mut c_void, name: *const c_char) -> *mut c_void;
}

// This is the public AVCUDADeviceContext prefix in libavutil/hwcontext_cuda.h.
#[repr(C)]
struct DeviceContext {
    context: *mut c_void,
    stream: *mut c_void,
}
#[repr(C)]
union Handle {
    fd: i32,
    alignment: [*mut c_void; 2],
}
#[repr(C)]
struct ExternalDescriptor {
    kind: i32,
    handle: Handle,
    size: u64,
    flags: u32,
    reserved: [u32; 16],
}
#[repr(C)]
struct BufferDescriptor {
    offset: u64,
    size: u64,
    flags: u32,
    reserved: [u32; 16],
}
#[repr(C)]
#[derive(Default)]
struct Copy2d {
    source_x: usize,
    source_y: usize,
    source_kind: u32,
    source_host: *const c_void,
    source_device: u64,
    source_array: *mut c_void,
    source_pitch: usize,
    target_x: usize,
    target_y: usize,
    target_kind: u32,
    target_host: *mut c_void,
    target_device: u64,
    target_array: *mut c_void,
    target_pitch: usize,
    width: usize,
    height: usize,
}

pub(super) struct Context<'a> {
    library: *mut c_void,
    pop: unsafe extern "C" fn(*mut *mut c_void) -> i32,
    synchronize: unsafe extern "C" fn() -> i32,
    _surface: &'a Frame,
}
impl<'a> Context<'a> {
    pub fn enter(surface: &'a Frame) -> Result<Self, String> {
        unsafe {
            if surface.0.is_null() || (*surface.0).hw_frames_ctx.is_null() {
                return Err("A CUDA hardware surface is required.".into());
            }
            let frames = (*(*surface.0).hw_frames_ctx)
                .data
                .cast::<ffi::AVHWFramesContext>();
            if frames.is_null()
                || (*frames).format != ffi::AVPixelFormat_AV_PIX_FMT_CUDA
                || (*frames).device_ctx.is_null()
                || (*(*frames).device_ctx).hwctx.is_null()
            {
                return Err("The recording hardware context is not CUDA.".into());
            }
            let library = ffi::dlopen(
                c"libcuda.so.1".as_ptr(),
                ffi::RTLD_NOW as i32 | ffi::RTLD_LOCAL as i32,
            );
            if library.is_null() {
                return Err("CUDA driver is unavailable.".into());
            }
            let push = dlsym(library, c"cuCtxPushCurrent_v2".as_ptr());
            let pop = dlsym(library, c"cuCtxPopCurrent_v2".as_ptr());
            let synchronize = dlsym(library, c"cuCtxSynchronize".as_ptr());
            if push.is_null() || pop.is_null() || synchronize.is_null() {
                ffi::dlclose(library);
                return Err("CUDA context functions are unavailable.".into());
            }
            let frames = (*(*surface.0).hw_frames_ctx)
                .data
                .cast::<ffi::AVHWFramesContext>();
            let device = (*(*frames).device_ctx).hwctx.cast::<DeviceContext>();
            let push: unsafe extern "C" fn(*mut c_void) -> i32 = std::mem::transmute(push);
            let result = push((*device).context);
            if result != 0 {
                ffi::dlclose(library);
                return Err(format!("Activate recording CUDA context: {result}."));
            }
            Ok(Self {
                library,
                pop: std::mem::transmute(pop),
                synchronize: std::mem::transmute(synchronize),
                _surface: surface,
            })
        }
    }
    unsafe fn symbol<T: Copy>(&self, name: &std::ffi::CStr) -> Result<T, String> {
        let pointer = unsafe { dlsym(self.library, name.as_ptr()) };
        if pointer.is_null() {
            return Err(format!("Missing CUDA function {}.", name.to_string_lossy()));
        }
        Ok(unsafe { std::mem::transmute_copy(&pointer) })
    }
    pub fn finish(&self) {
        // A failed completion check never authorizes reuse of capture or encoder storage.
        // Fail-stop is restricted to a poisoned driver context with indeterminate DMA lifetime.
        if unsafe { (self.synchronize)() } != 0 {
            eprintln!(
                "CUDA completion failed; terminating to prevent reuse of active DMA storage."
            );
            std::process::abort();
        }
    }
    pub fn upload(
        &self,
        bytes: &[u8],
        target: &Frame,
        stride: usize,
        uv_offset: usize,
        width: usize,
        height: usize,
    ) -> Result<(), String> {
        unsafe {
            let register: unsafe extern "C" fn(*mut c_void, usize, u32) -> i32 =
                self.symbol(c"cuMemHostRegister_v2")?;
            let unregister: unsafe extern "C" fn(*mut c_void) -> i32 =
                self.symbol(c"cuMemHostUnregister")?;
            let result = register(bytes.as_ptr().cast_mut().cast(), bytes.len(), 1);
            // Pageable upload remains valid when the exporter cannot be registered.
            let registered = result == 0;
            let copied = self.copy(
                bytes.as_ptr() as u64,
                false,
                target,
                stride,
                uv_offset,
                width,
                height,
            );
            self.finish();
            if registered && unregister(bytes.as_ptr().cast_mut().cast()) != 0 {
                eprintln!("CUDA host unregistration failed; terminating before storage reuse.");
                std::process::abort();
            }
            copied
        }
    }
    pub fn import(
        &self,
        source: &dyn SharedVideoFrame,
        target: &Frame,
        stride: usize,
        uv_offset: usize,
        width: usize,
        height: usize,
    ) -> Result<(), String> {
        let descriptor = source
            .descriptor()
            .try_clone_to_owned()
            .map_err(|error| error.to_string())?;
        unsafe {
            let import: unsafe extern "C" fn(*mut *mut c_void, *const ExternalDescriptor) -> i32 =
                self.symbol(c"cuImportExternalMemory")?;
            let map: unsafe extern "C" fn(*mut u64, *mut c_void, *const BufferDescriptor) -> i32 =
                self.symbol(c"cuExternalMemoryGetMappedBuffer")?;
            let free: unsafe extern "C" fn(u64) -> i32 = self.symbol(c"cuMemFree_v2")?;
            let destroy: unsafe extern "C" fn(*mut c_void) -> i32 =
                self.symbol(c"cuDestroyExternalMemory")?;
            let description = ExternalDescriptor {
                kind: 9,
                handle: Handle {
                    alignment: [ptr::null_mut(); 2],
                },
                size: source.allocation_size() as u64,
                flags: 0,
                reserved: [0; 16],
            };
            let mut description = description;
            description.handle.fd = descriptor.as_raw_fd();
            let mut memory = ptr::null_mut();
            let result = import(&mut memory, &description);
            if result != 0 {
                return Err(format!("CUDA DMABUF_FD import: {result}."));
            }
            // Unlike OPAQUE_FD, DMABUF_FD ownership is retained by the caller.
            let mut pointer = 0;
            let description = BufferDescriptor {
                offset: 0,
                size: source.allocation_size() as u64,
                flags: 0,
                reserved: [0; 16],
            };
            let mapped = map(&mut pointer, memory, &description);
            let result = if mapped == 0 {
                self.copy(pointer, true, target, stride, uv_offset, width, height)
            } else {
                Err(format!("CUDA DMABUF_FD mapping: {mapped}."))
            };
            self.finish();
            if (mapped == 0 && free(pointer) != 0) || destroy(memory) != 0 {
                eprintln!("CUDA external-memory cleanup failed; terminating before storage reuse.");
                std::process::abort();
            }
            result
        }
    }
    unsafe fn copy(
        &self,
        source: u64,
        device: bool,
        target: &Frame,
        stride: usize,
        uv_offset: usize,
        width: usize,
        height: usize,
    ) -> Result<(), String> {
        let copy: unsafe extern "C" fn(*const Copy2d) -> i32 =
            unsafe { self.symbol(c"cuMemcpy2DUnaligned_v2")? };
        for (plane, offset, rows) in [(0, 0, height), (1, uv_offset, height.div_ceil(2))] {
            let source = source
                .checked_add(offset as u64)
                .ok_or("CUDA source address overflow.")?;
            let description = Copy2d {
                source_kind: if device { 2 } else { 1 },
                source_host: if device {
                    ptr::null()
                } else {
                    source as *const c_void
                },
                source_device: if device { source } else { 0 },
                source_pitch: stride,
                target_kind: 2,
                target_device: unsafe { (*target.0).data[plane] as u64 },
                target_pitch: unsafe { (*target.0).linesize[plane] as usize },
                width,
                height: rows,
                ..Copy2d::default()
            };
            let result = unsafe { copy(&description) };
            if result != 0 {
                return Err(format!("Copy recording CUDA plane: {result}."));
            }
        }
        Ok(())
    }
}
impl Drop for Context<'_> {
    fn drop(&mut self) {
        self.finish();
        unsafe {
            let mut previous = ptr::null_mut();
            if (self.pop)(&mut previous) != 0 {
                std::process::abort();
            }
            ffi::dlclose(self.library);
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    #[cfg(target_pointer_width = "64")]
    fn driver_abi_layouts() {
        assert_eq!(std::mem::size_of::<super::ExternalDescriptor>(), 104);
        assert_eq!(std::mem::size_of::<super::BufferDescriptor>(), 88);
        assert_eq!(std::mem::size_of::<super::Copy2d>(), 128);
        assert_eq!(std::mem::offset_of!(super::ExternalDescriptor, size), 24);
    }
}
