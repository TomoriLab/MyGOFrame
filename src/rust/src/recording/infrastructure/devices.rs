use crate::ffi;
use std::ffi::{CStr, c_char, c_void};
use std::fs;

#[derive(Clone, Debug)]
pub(crate) struct Device {
    pub id: String,
    pub name: String,
    pub backend: String,
    pub device: String,
    pub pci: String,
}

struct Library(*mut c_void);
impl Drop for Library {
    fn drop(&mut self) {
        unsafe {
            ffi::dlclose(self.0);
        }
    }
}
unsafe extern "C" {
    fn dlsym(handle: *mut c_void, name: *const c_char) -> *mut c_void;
}

fn cuda_devices() -> Result<Vec<Device>, String> {
    let pointer = unsafe {
        ffi::dlopen(
            c"libcuda.so.1".as_ptr(),
            ffi::RTLD_LAZY as i32 | ffi::RTLD_LOCAL as i32,
        )
    };
    if pointer.is_null() {
        return Ok(Vec::new());
    }
    let library = Library(pointer);
    unsafe {
        let initialize = dlsym(library.0, c"cuInit".as_ptr());
        let get_count = dlsym(library.0, c"cuDeviceGetCount".as_ptr());
        let get_device = dlsym(library.0, c"cuDeviceGet".as_ptr());
        let get_name = dlsym(library.0, c"cuDeviceGetName".as_ptr());
        let get_pci = dlsym(library.0, c"cuDeviceGetPCIBusId".as_ptr());
        let get_uuid = dlsym(library.0, c"cuDeviceGetUuid_v2".as_ptr());
        if [
            initialize, get_count, get_device, get_name, get_pci, get_uuid,
        ]
        .iter()
        .any(|value| value.is_null())
        {
            return Err("The CUDA driver is missing required device enumeration symbols.".into());
        }
        // The loaded CUDA driver defines these C ABI signatures and remains resident during every call.
        let initialize: unsafe extern "C" fn(u32) -> i32 = std::mem::transmute(initialize);
        let get_count: unsafe extern "C" fn(*mut i32) -> i32 = std::mem::transmute(get_count);
        let get_device: unsafe extern "C" fn(*mut i32, i32) -> i32 =
            std::mem::transmute(get_device);
        let get_name: unsafe extern "C" fn(*mut c_char, i32, i32) -> i32 =
            std::mem::transmute(get_name);
        let get_pci: unsafe extern "C" fn(*mut c_char, i32, i32) -> i32 =
            std::mem::transmute(get_pci);
        let get_uuid: unsafe extern "C" fn(*mut [u8; 16], i32) -> i32 =
            std::mem::transmute(get_uuid);
        let mut count = 0;
        let initialized = initialize(0);
        let enumerated = if initialized == 0 {
            get_count(&mut count)
        } else {
            -1
        };
        if initialized != 0 || enumerated != 0 {
            return Err(format!(
                "CUDA GPU discovery failed: initialization={initialized}, enumeration={enumerated}."
            ));
        }
        let mut result = Vec::new();
        for index in 0..count.clamp(0, 64) {
            let mut handle = 0;
            let mut name = [0 as c_char; 256];
            let mut pci = [0 as c_char; 64];
            let mut uuid = [0_u8; 16];
            if get_device(&mut handle, index) != 0
                || get_name(name.as_mut_ptr(), 255, handle) != 0
                || get_pci(pci.as_mut_ptr(), 63, handle) != 0
                || get_uuid(&mut uuid, handle) != 0
            {
                continue;
            }
            let identity = uuid
                .iter()
                .map(|byte| format!("{byte:02x}"))
                .collect::<String>();
            result.push(Device {
                id: format!("nvenc:{identity}"),
                name: CStr::from_ptr(name.as_ptr()).to_string_lossy().into_owned(),
                backend: "nvenc".into(),
                device: index.to_string(),
                pci: CStr::from_ptr(pci.as_ptr())
                    .to_string_lossy()
                    .to_lowercase(),
            });
        }
        Ok(result)
    }
}

pub(crate) fn discover() -> Result<Vec<Device>, String> {
    let cuda = cuda_devices();
    let failure = cuda.as_ref().err().cloned();
    let mut result = cuda.unwrap_or_default();
    if let Ok(entries) = fs::read_dir("/sys/class/drm") {
        for entry in entries.flatten() {
            let name = entry.file_name().to_string_lossy().into_owned();
            if !name.starts_with("renderD") {
                continue;
            }
            let path = entry.path().join("device");
            let Ok(device) = path.canonicalize() else {
                continue;
            };
            let Some(pci) = device.file_name().and_then(|value| value.to_str()) else {
                continue;
            };
            let vendor = fs::read_to_string(path.join("vendor")).unwrap_or_default();
            let (backend, brand) = match vendor.trim() {
                "0x8086" => ("qsv", "Intel"),
                "0x1002" => ("vaapi", "AMD"),
                _ => continue,
            };
            let stable_path = format!("/dev/dri/by-path/pci-{pci}-render");
            let selected = if std::path::Path::new(&stable_path).exists() {
                stable_path
            } else {
                format!("/dev/dri/{name}")
            };
            result.push(Device {
                id: format!("{backend}:{pci}"),
                name: format!("{brand} GPU ({pci})"),
                backend: backend.into(),
                device: selected,
                pci: pci.into(),
            });
        }
    }
    result.sort_by_key(|value| {
        (
            match value.backend.as_str() {
                "nvenc" => 0,
                "qsv" => 1,
                _ => 2,
            },
            value.id.clone(),
        )
    });
    if result.is_empty()
        && let Some(error) = failure
    {
        return Err(error);
    }
    Ok(result)
}

pub(crate) fn resolve(identity: &str) -> Result<Device, String> {
    let devices = discover()?;
    if identity == "auto" || identity.is_empty() {
        devices
            .into_iter()
            .next()
            .ok_or_else(|| "No supported hardware recording GPU is available.".into())
    } else {
        devices
            .into_iter()
            .find(|value| value.id == identity)
            .ok_or_else(|| "The selected recording GPU is unavailable.".into())
    }
}

pub(crate) fn json_string(value: &str) -> String {
    let mut result = String::from("\"");
    for character in value.chars() {
        match character {
            '"' => result.push_str("\\\""),
            '\\' => result.push_str("\\\\"),
            character if character <= '\u{1f}' => {
                result.push_str(&format!("\\u{:04x}", character as u32))
            }
            _ => result.push(character),
        }
    }
    result.push('"');
    result
}
