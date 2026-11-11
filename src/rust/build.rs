use std::env;
use std::path::PathBuf;
use std::process::{Command, Stdio};

fn main() {
    let header = PathBuf::from("../abi/rust_bindings.h");
    println!("cargo:rerun-if-changed={}", header.display());
    for name in ["rust_core.h", "video.h", "audio.h", "recording.h"] {
        println!("cargo:rerun-if-changed=../abi/{name}");
    }

    let packages = [
        "libpipewire-0.3",
        "libavcodec",
        "libavutil",
        "libswscale",
        "libavformat",
        "libswresample",
    ];
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
        .args(["--allowlist-function", "^(pw_.*|spa_.*|av_.*|avcodec_.*|avformat_.*|avio_.*|swr_.*|sws_.*|open|close|ioctl|mmap|munmap|poll|clock_gettime|usleep|dlopen|dlclose)$"])
        .args(["--allowlist-type", "^(pw_.*|spa_.*|v4l2_.*|AV.*|SwsContext|SwrContext|mgfr_.*|pollfd|timespec|timeval)$"])
        .args(["--allowlist-var", "^(PW_.*|SPA_.*|V4L2_.*|VIDIOC_.*|AV_.*|AVFMT_.*|AVIO_.*|AVSEEK_.*|SWS_.*|SWR_.*|MGF_.*|O_.*|PROT_.*|MAP_.*|POLL.*|CLOCK_MONOTONIC|RTLD_.*)$"])
        .args(["--no-layout-tests", "--no-doc-comments"])
        .arg("--")
        .arg("-I../abi")
        .args(String::from_utf8_lossy(&flags.stdout).split_whitespace())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let status = command.status().expect("bindgen must be installed");
    assert!(
        status.success(),
        "bindgen failed to generate native bindings"
    );

    // Integration tests bind only the public ABI rather than exposing implementation modules.
    let public_bindings =
        PathBuf::from(env::var_os("OUT_DIR").expect("Cargo sets OUT_DIR")).join("public_api.rs");
    let status = Command::new("bindgen")
        .arg("../abi/rust_core.h")
        .arg("--output")
        .arg(public_bindings)
        .args([
            "--allowlist-function",
            "^mgfr_.*",
            "--allowlist-type",
            "^mgfr_.*",
            "--no-layout-tests",
            "--no-doc-comments",
        ])
        .status()
        .expect("bindgen must be installed");
    assert!(
        status.success(),
        "bindgen failed to generate public API test bindings"
    );

    for library in [
        "pipewire-0.3",
        "avcodec",
        "avutil",
        "swscale",
        "avformat",
        "swresample",
        "dl",
        "m",
    ] {
        println!("cargo:rustc-link-lib={library}");
    }
}
