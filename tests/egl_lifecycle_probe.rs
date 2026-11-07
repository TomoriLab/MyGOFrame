use std::ffi::{CStr, c_char, c_void};
use std::process::ExitCode;
use std::ptr;

type Pointer = *mut c_void;

#[link(name = "libEGL.so.1", kind = "dylib", modifiers = "+verbatim")]
unsafe extern "C" {
    fn eglGetPlatformDisplay(platform: u32, native: Pointer, attributes: *const isize) -> Pointer;
    fn eglInitialize(display: Pointer, major: *mut i32, minor: *mut i32) -> u32;
    fn eglBindAPI(api: u32) -> u32;
    fn eglCreateContext(
        display: Pointer,
        config: Pointer,
        shared: Pointer,
        attributes: *const i32,
    ) -> Pointer;
    fn eglMakeCurrent(display: Pointer, draw: Pointer, read: Pointer, context: Pointer) -> u32;
    fn eglDestroyContext(display: Pointer, context: Pointer) -> u32;
    fn eglTerminate(display: Pointer) -> u32;
    fn eglReleaseThread() -> u32;
    fn eglGetError() -> u32;
    fn eglQueryString(display: Pointer, key: i32) -> *const c_char;
    fn eglGetProcAddress(name: *const c_char) -> Pointer;
}

#[link(
    name = "libwayland-client.so.0",
    kind = "dylib",
    modifiers = "+verbatim"
)]
unsafe extern "C" {
    fn wl_display_connect(name: *const c_char) -> Pointer;
    fn wl_display_disconnect(display: Pointer);
}

fn checked(operation: &str, success: u32) -> Result<(), String> {
    if success == 1 {
        Ok(())
    } else {
        Err(format!("{operation}: EGL error {:#x}", unsafe {
            eglGetError()
        }))
    }
}

struct Session {
    native: Pointer,
    display: Pointer,
    context: Pointer,
    current: bool,
    thread_state: bool,
    finish: Option<unsafe extern "C" fn()>,
}

impl Session {
    fn new() -> Result<Self, String> {
        let mut session = Self {
            native: ptr::null_mut(),
            display: ptr::null_mut(),
            context: ptr::null_mut(),
            current: false,
            thread_state: false,
            finish: None,
        };
        // The session remains on its creating thread and owns every successful acquisition.
        unsafe {
            session.native = wl_display_connect(ptr::null());
            if session.native.is_null() {
                return Err("A live Wayland display is required.".into());
            }
            session.thread_state = true;
            session.display = eglGetPlatformDisplay(0x31D8, session.native, ptr::null());
            if session.display.is_null() {
                return Err(format!(
                    "eglGetPlatformDisplay: EGL error {:#x}",
                    eglGetError()
                ));
            }
            let (mut major, mut minor) = (0, 0);
            checked(
                "eglInitialize",
                eglInitialize(session.display, &mut major, &mut minor),
            )?;
            checked("eglBindAPI", eglBindAPI(0x30A0))?;
            session.context = eglCreateContext(
                session.display,
                ptr::null_mut(),
                ptr::null_mut(),
                [0x3098, 3, 0x3038].as_ptr(),
            );
            if session.context.is_null() {
                return Err(format!("eglCreateContext: EGL error {:#x}", eglGetError()));
            }
            checked(
                "eglMakeCurrent",
                eglMakeCurrent(
                    session.display,
                    ptr::null_mut(),
                    ptr::null_mut(),
                    session.context,
                ),
            )?;
            session.current = true;
            let finish = eglGetProcAddress(c"glFinish".as_ptr());
            if finish.is_null() {
                return Err("The GLES completion entry point is unavailable.".into());
            }
            // Safety: The named GLES entry point has the exact void(void) C signature.
            session.finish = Some(std::mem::transmute::<Pointer, unsafe extern "C" fn()>(
                finish,
            ));
        }
        Ok(session)
    }

    fn vendor(&self) -> Result<String, String> {
        let vendor = unsafe { eglQueryString(self.display, 0x3053) };
        if vendor.is_null() {
            return Err("The EGL vendor string is unavailable.".into());
        }
        Ok(unsafe { CStr::from_ptr(vendor) }
            .to_string_lossy()
            .into_owned())
    }

    fn close(&mut self) -> Result<(), String> {
        let mut errors = Vec::new();
        let mut record = |operation, success| {
            if let Err(error) = checked(operation, success) {
                errors.push(error);
            }
        };
        // Cleanup continues after a failed call so one error cannot skip independent owners.
        unsafe {
            if self.current {
                if let Some(finish) = self.finish {
                    finish();
                }
                record(
                    "eglMakeCurrent-none",
                    eglMakeCurrent(
                        self.display,
                        ptr::null_mut(),
                        ptr::null_mut(),
                        ptr::null_mut(),
                    ),
                );
                self.current = false;
            }
            if !self.context.is_null() {
                record(
                    "eglDestroyContext",
                    eglDestroyContext(self.display, self.context),
                );
                self.context = ptr::null_mut();
            }
            if !self.display.is_null() {
                record("eglTerminate", eglTerminate(self.display));
                self.display = ptr::null_mut();
            }
            if self.thread_state {
                // A successful release is the final EGL call; querying its error recreates TLS.
                record("eglReleaseThread", eglReleaseThread());
                self.thread_state = false;
            }
            if !self.native.is_null() {
                wl_display_disconnect(self.native);
                self.native = ptr::null_mut();
            }
        }
        if errors.is_empty() {
            Ok(())
        } else {
            Err(errors.join("; "))
        }
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        if let Err(error) = self.close() {
            eprintln!("Session cleanup failed: {error}");
        }
    }
}

fn run(cycles: usize) -> Result<(), String> {
    for index in 0..cycles {
        let mut session = Session::new()?;
        let vendor = session.vendor()?;
        session.close()?;
        println!(
            "Completed EGL lifecycle {} of {cycles}; vendor={vendor}",
            index + 1
        );
    }
    Ok(())
}

fn main() -> ExitCode {
    let arguments: Vec<_> = std::env::args().collect();
    let Some(cycles) = arguments
        .get(1)
        .and_then(|value| value.parse::<usize>().ok())
    else {
        eprintln!("A lifecycle count is required.");
        return ExitCode::FAILURE;
    };
    if !(1..=1000).contains(&cycles) {
        eprintln!("The lifecycle count must be between 1 and 1000.");
        return ExitCode::FAILURE;
    }
    let result = match arguments.get(2).map(String::as_str) {
        Some("main") => run(cycles),
        Some("worker") => match std::thread::Builder::new()
            .name("egl-lifecycle".into())
            .spawn(move || run(cycles))
        {
            Ok(worker) => worker
                .join()
                .unwrap_or_else(|_| Err("The EGL owner thread panicked.".into())),
            Err(error) => Err(format!("Start EGL owner thread: {error}")),
        },
        _ => Err("The owner must be main or worker.".into()),
    };
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("EGL lifecycle validation failed: {error}");
            ExitCode::FAILURE
        }
    }
}
