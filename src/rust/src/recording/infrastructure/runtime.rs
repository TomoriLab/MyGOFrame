use crate::ffi;
use std::sync::OnceLock;

pub(crate) fn prepare(backend: &str) -> Result<(), String> {
    if backend != "nvenc"
        || std::fs::read_to_string("/sys/module/nvidia/version")
            .unwrap_or_default()
            .trim()
            != "615.78.08"
    {
        return Ok(());
    }
    static INITIALIZED: OnceLock<Result<(), String>> = OnceLock::new();
    INITIALIZED
        .get_or_init(|| {
            // This driver retains global legacy condition variables after unloading its codec runtime.
            // A process-lifetime mapping preserves their owners while individual encoder contexts still close normally.
            let library = unsafe {
                ffi::dlopen(
                    c"libnvidia-encode.so.1".as_ptr(),
                    ffi::RTLD_NOW as i32 | ffi::RTLD_LOCAL as i32 | ffi::RTLD_NODELETE as i32,
                )
            };
            if library.is_null() {
                return Err("Unable to retain the NVIDIA codec runtime.".into());
            }
            unsafe {
                ffi::dlclose(library);
            }
            Ok(())
        })
        .clone()
}
