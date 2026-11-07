#![deny(unsafe_op_in_unsafe_fn)]

#[allow(
    non_camel_case_types,
    non_snake_case,
    non_upper_case_globals,
    dead_code,
    unnecessary_transmutes,
    clippy::ptr_offset_with_cast,
    clippy::type_complexity,
    clippy::upper_case_acronyms
)]
pub(crate) mod ffi {
    include!(concat!(env!("OUT_DIR"), "/bindings.rs"));
}

pub mod audio;
pub mod video;

use std::ffi::{CString, c_char};

pub(crate) fn make_c_string(value: String) -> *mut c_char {
    CString::new(value.replace('\0', " "))
        .expect("NUL characters were removed")
        .into_raw()
}

#[unsafe(no_mangle)]
/// Releases a string returned by the media core.
///
/// # Safety
///
/// `value` must be null or an allocation returned by a media-core string API
/// that has not already been released.
pub unsafe extern "C" fn mgfr_string_free(value: *mut c_char) {
    if !value.is_null() {
        // The pointer was produced by make_c_string and has one matching release.
        unsafe { drop(CString::from_raw(value)) };
    }
}
