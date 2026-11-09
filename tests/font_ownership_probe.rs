//! This isolated process distinguishes owned Fontconfig state from lost references.
//! The sole-owner FcFini control is diagnostic-only and is not application shutdown policy.

use std::ffi::{CStr, c_char, c_int, c_void};
use std::ptr;
use std::sync::atomic::{AtomicPtr, Ordering};

static PATTERN: AtomicPtr<c_void> = AtomicPtr::new(ptr::null_mut());
static CONFIG: AtomicPtr<c_void> = AtomicPtr::new(ptr::null_mut());

unsafe extern "C" {
    fn __lsan_do_recoverable_leak_check() -> c_int;
}

#[link(name = "libfontconfig.so.1", kind = "dylib", modifiers = "+verbatim")]
unsafe extern "C" {
    fn FcPatternCreate() -> *mut c_void;
    fn FcPatternAddString(pattern: *mut c_void, object: *const c_char, value: *const u8) -> c_int;
    fn FcPatternGetString(
        pattern: *mut c_void,
        object: *const c_char,
        index: c_int,
        value: *mut *mut u8,
    ) -> c_int;
    fn FcPatternDestroy(pattern: *mut c_void);
    fn FcInit() -> c_int;
    fn FcFini();
    fn FcInitLoadConfigAndFonts() -> *mut c_void;
    fn FcConfigDestroy(config: *mut c_void);
    fn FcConfigGetCurrent() -> *mut c_void;
    fn FcConfigReference(config: *mut c_void) -> *mut c_void;
    fn FcConfigSetCurrent(config: *mut c_void) -> c_int;
    fn FcFontMatch(config: *mut c_void, pattern: *mut c_void, result: *mut c_int) -> *mut c_void;
    fn FcGetVersion() -> c_int;
}

#[link(
    name = "libpangocairo-1.0.so.0",
    kind = "dylib",
    modifiers = "+verbatim"
)]
unsafe extern "C" {
    fn pango_cairo_font_map_new() -> *mut c_void;
}

#[link(name = "libpango-1.0.so.0", kind = "dylib", modifiers = "+verbatim")]
unsafe extern "C" {
    fn pango_version_string() -> *const c_char;
    fn pango_font_map_create_context(font_map: *mut c_void) -> *mut c_void;
    fn pango_font_map_list_families(
        font_map: *mut c_void,
        families: *mut *mut *mut c_void,
        count: *mut c_int,
    );
}

#[link(name = "libgobject-2.0.so.0", kind = "dylib", modifiers = "+verbatim")]
unsafe extern "C" {
    fn g_object_unref(object: *mut c_void);
}

#[link(name = "libglib-2.0.so.0", kind = "dylib", modifiers = "+verbatim")]
unsafe extern "C" {
    fn g_free(pointer: *mut c_void);
}

fn pattern(release: bool) {
    unsafe {
        let pattern = FcPatternCreate();
        assert!(!pattern.is_null());
        assert_eq!(
            FcPatternAddString(
                pattern,
                c"family".as_ptr(),
                c"MyGO leak control".as_ptr().cast()
            ),
            1
        );
        let mut string = ptr::null_mut();
        assert_eq!(
            FcPatternGetString(pattern, c"family".as_ptr(), 0, &mut string),
            0
        );
        assert_eq!(CStr::from_ptr(string.cast()), c"MyGO leak control");
        PATTERN.store(pattern, Ordering::SeqCst);
        println!("pattern={pattern:p} getString=PASS retained={}", !release);
        if release {
            FcPatternDestroy(PATTERN.swap(ptr::null_mut(), Ordering::SeqCst));
        }
    }
}

fn owned_config(release: bool) {
    unsafe {
        let config = FcInitLoadConfigAndFonts();
        assert!(!config.is_null());
        CONFIG.store(config, Ordering::SeqCst);
        println!("ownedConfig={config:p} retained={}", !release);
        if release {
            FcConfigDestroy(CONFIG.swap(ptr::null_mut(), Ordering::SeqCst));
        }
    }
}

fn pango(cycles: usize, preinitialize: bool) {
    let baseline_threads = std::fs::read_dir("/proc/self/task").unwrap().count();
    unsafe {
        if preinitialize {
            assert_eq!(FcInit(), 1);
        }
        for cycle in 0..cycles {
            let map = pango_cairo_font_map_new();
            assert!(!map.is_null());
            let context = pango_font_map_create_context(map);
            assert!(!context.is_null());
            let mut families = ptr::null_mut();
            let mut count = 0;
            pango_font_map_list_families(map, &mut families, &mut count);
            assert!(count > 0);
            g_free(families.cast());
            g_object_unref(context);
            g_object_unref(map);
            println!("cycle={cycle} families={count} released=PASS");
        }
    }
    // Pango queues FC_END after its owned references disappear, rather than exposing a join API.
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(2);
    let final_threads = loop {
        let count = std::fs::read_dir("/proc/self/task").unwrap().count();
        if count == baseline_threads || std::time::Instant::now() >= deadline {
            break count;
        }
        std::thread::sleep(std::time::Duration::from_millis(5));
    };
    assert_eq!(final_threads, baseline_threads);
    println!("baselineThreads={baseline_threads} finalThreads={final_threads}");
}

fn detached_config() {
    unsafe {
        for cycle in 0..5 {
            assert_eq!(FcInit(), 1);
            // The caller's explicit reference must survive removal of the global reference.
            let config = FcConfigReference(FcConfigGetCurrent());
            assert!(!config.is_null());
            assert_eq!(FcConfigSetCurrent(ptr::null_mut()), 1);
            let pattern = FcPatternCreate();
            assert!(!pattern.is_null());
            let mut result = -1;
            let font = FcFontMatch(config, pattern, &mut result);
            assert_eq!(result, 0);
            assert!(!font.is_null());
            let mut family = ptr::null_mut();
            assert_eq!(FcPatternGetString(font, c"family".as_ptr(), 0, &mut family), 0);
            assert!(!CStr::from_ptr(family.cast()).is_empty());
            FcPatternDestroy(pattern);
            FcConfigDestroy(config);
            // A matched font owns its data independently of the configuration lifetime.
            assert_eq!(FcPatternGetString(font, c"family".as_ptr(), 0, &mut family), 0);
            assert!(!CStr::from_ptr(family.cast()).is_empty());
            FcPatternDestroy(font);
            // Font matching can initialize a fresh default configuration internally.
            assert_eq!(FcConfigSetCurrent(ptr::null_mut()), 1);
            println!("detachedConfigCycle={cycle} retainedConfig=PASS retainedFont=PASS");
        }
    }
}

fn main() {
    assert_eq!(
        std::env::args().count(),
        2,
        "Exactly one control mode is required."
    );
    unsafe {
        println!(
            "fontconfigVersion={} pangoVersion={}",
            FcGetVersion(),
            CStr::from_ptr(pango_version_string()).to_string_lossy()
        );
    }
    let mode = std::env::args().nth(1).expect("mode required");
    match mode.as_str() {
        "pattern-retain" => pattern(false),
        "pattern-release" => pattern(true),
        "pattern-lost-owner" => {
            pattern(false);
            PATTERN.store(ptr::null_mut(), Ordering::SeqCst);
        }
        "pattern-checked" => {
            pattern(false);
            unsafe {
                assert_eq!(__lsan_do_recoverable_leak_check(), 1);
                println!("retainedPatternLeakCheck=1");
                let mut string = ptr::null_mut();
                assert_eq!(
                    FcPatternGetString(
                        PATTERN.load(Ordering::SeqCst),
                        c"family".as_ptr(),
                        0,
                        &mut string
                    ),
                    0
                );
                assert_eq!(CStr::from_ptr(string.cast()), c"MyGO leak control");
                println!("retainedPatternQuery=PASS");
                FcPatternDestroy(PATTERN.swap(ptr::null_mut(), Ordering::SeqCst));
                assert_eq!(__lsan_do_recoverable_leak_check(), 0);
                println!("releasedPatternLeakCheck=0");
            }
        }
        "config-retain" => owned_config(false),
        "config-release" => owned_config(true),
        "global-retain" => unsafe {
            assert_eq!(FcInit(), 1);
        },
        // This isolated process owns its sole FcInit and uses no Pango or GTK font users.
        "global-paired" => unsafe {
            assert_eq!(FcInit(), 1);
            FcFini();
        },
        "pango-release" => pango(1, false),
        "pango-release-5" => pango(5, false),
        "pango-preinit" => pango(1, true),
        "pango-detach-config" => {
            pango(5, false);
            unsafe { assert_eq!(FcConfigSetCurrent(ptr::null_mut()), 1) };
        }
        "config-detach-live-reference" => detached_config(),
        _ => panic!("unsupported mode"),
    }
}
