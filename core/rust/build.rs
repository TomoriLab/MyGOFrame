use std::env;
use std::path::PathBuf;
use std::process::{Command, Stdio};

fn main() {
    let header = PathBuf::from("../include/rust_bindings.h");
    println!("cargo:rerun-if-changed={}", header.display());
    for name in ["rust_core.h", "video.h", "audio.h"] {
        println!("cargo:rerun-if-changed=../include/{name}");
    }

    let packages = ["libpipewire-0.3", "libavcodec", "libavutil", "libswscale"];
    let flags = Command::new("pkg-config")
        .arg("--cflags")
        .args(packages)
        .output()
        .expect("pkg-config must be installed");
    assert!(
        flags.status.success(),
        "native development headers are required"
    );

    let generated =
        PathBuf::from(env::var_os("OUT_DIR").expect("Cargo sets OUT_DIR")).join("bindings.rs");
    let mut command = Command::new("bindgen");
    command
        .arg(&header)
        .arg("--output")
        .arg(&generated)
        .args(["--allowlist-function", "^(pw_.*|spa_.*|av_.*|avcodec_.*|sws_.*|open|close|ioctl|mmap|munmap|poll|clock_gettime|usleep|dlopen|dlclose)$"])
        .args(["--allowlist-type", "^(pw_.*|spa_.*|v4l2_.*|AV.*|SwsContext|mgfr_.*|pollfd|timespec|timeval)$"])
        .args(["--allowlist-var", "^(PW_.*|SPA_.*|V4L2_.*|VIDIOC_.*|AV_.*|SWS_.*|MGF_.*|O_.*|PROT_.*|MAP_.*|POLL.*|CLOCK_MONOTONIC|RTLD_.*)$"])
        .args(["--no-layout-tests", "--no-doc-comments"])
        .arg("--")
        .arg("-I../include")
        .args(String::from_utf8_lossy(&flags.stdout).split_whitespace())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let status = command.status().expect("bindgen must be installed");
    assert!(
        status.success(),
        "bindgen failed to generate native bindings"
    );

    for library in ["pipewire-0.3", "avcodec", "avutil", "swscale", "dl", "m"] {
        println!("cargo:rustc-link-lib={library}");
    }
}
