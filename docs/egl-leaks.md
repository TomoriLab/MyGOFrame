# NVIDIA EGL allocation leaks

Date: 2026-11-07. This application compatibility fix is limited to NVIDIA kernel-driver version `615.78.08`, verified on an RTX 5080 desktop. It avoids two driver allocation paths before graphics initialization. It does not patch vendor binaries, suppress sanitizer findings, free foreign pointers, or shut down D-Bus globally.

## Root causes

### GBM modifier storage

A native Rust reproduction using only libgbm opens the render node, calls `gbm_create_device`, calls the matching `gbm_device_destroy`, and closes the descriptor. One lifecycle leaves 112 bytes; five leave 560 bytes. Python, EGL, Wayland, MyGo, and the media core are absent from this reproducer.

The allocation belongs to `gbm/nvidia-drm_gbm.so`, a link to `libnvidia-allocator.so.615.78.08`. An allocation-time full stack and disassembly identify fourteen 64-bit modifier entries stored in the private device structure. The public destroy path leaves that array allocated. An old mapping captured after library unload was insufficient evidence; the fresh allocation-time trace and GBM-only reproduction establish ownership.

The newer `libnvidia-egl-wayland2.so.1.0.2` invokes this GBM path during display acquisition and initialization. Releasing EGL thread state, exiting its owner thread, extra completion waits, and closing the GDK display do not free the forgotten storage. Selecting installed `libnvidia-egl-wayland.so.1.1.20` avoids those allocations, but controlled dual-window tests found a lower sharing-window update rate. The preferred fix retains Wayland2 and uses Mesa DRI/Zink allocation backed by the existing NVIDIA Vulkan device. Native loader traces confirm the Vulkan allocator device and GLES renderer are both the RTX 5080. Plain Mesa DRI allocation without Zink was clean but also slower and was rejected. NVIDIA documents coexistence and the newer implementation's higher default priority in its [Wayland2 installation notes](https://github.com/NVIDIA/egl-wayland2/blob/v1.0.2/README.md#building-and-installing).

### D-Bus reply reference

Native stacks and GDB request/reference traces identify an unreleased `org.freedesktop.DBus.ListNames` reply on a private system-bus connection in NVIDIA EGL core. The request is unreferenced. Vendor teardown eventually closes and unreferences the connection. The reply retains reference count one. Its 184-byte message owns indirect string storage, whose size varies with the returned bus-name list.

Keeping libdbus resident does not release this reply. Calling `dbus_shutdown` while NVIDIA still owns a connection violates the [D-Bus shutdown contract](https://dbus.freedesktop.org/doc/api/html/group__DBusMemory.html) and caused an old-generation assertion in a control experiment.

Installed `nvidia-application-profiles-615.78.08-rc` contains `DisableDBus` and applies it to X-Plane. Setting `__GL_DisableDBus=1` before NVIDIA initialization prevents these vendor calls and removes the allocation. A separate native private-bus control still connects, sends, closes, and releases its ordinary D-Bus objects. This setting controls NVIDIA's integration, not all application D-Bus operations. It is a shipped vendor setting, not a documented stable public API. Its broader notebook power-management/FRL effects were not validated.

## Startup policy

`internal/graphicsruntime.Prepare` runs before MyGo initializes graphics. The native color and EGL lifecycle diagnostics use equivalent Python discovery. Both consume the same policy fixtures.

1. Read `/sys/module/nvidia/version`. Apply defaults only for exact version `615.78.08`; missing, future, and other versions keep their normal configuration.
2. Discover installed EGL platform/vendor manifests by `ICD.library_path`, rather than fixed filenames. Default platform directories include `/etc/egl/egl_external_platform.d` and `/usr/share/egl/egl_external_platform.d`, as described in the [NVIDIA maintainer's loader explanation](https://github.com/NVIDIA/egl-wayland/issues/39).
3. If NVIDIA is present and `__GL_DisableDBus` is unset, set it to `1` for this process.
4. In a selected Wayland session with the modern platform and a complete same-architecture Mesa DRI/Zink/Vulkan runtime, set `GBM_BACKEND=dri`, `MESA_LOADER_DRIVER_OVERRIDE=zink`, and `__GL_MaxFramesAllowed=1`. Retain the normal platform selection, including Wayland2. No Vulkan ICD is forced.
5. If those components are unavailable, retain a legacy fallback: exclude Wayland2 only when legacy is installed and keep all other platform manifests, including GBM and X11. The one-frame queue setting also applies to this fallback when unset.

Explicit backend/device values, including empty values, of `GBM_BACKEND`, `GBM_BACKENDS_PATH`, `MESA_LOADER_DRIVER_OVERRIDE`, `VK_DRIVER_FILES`, `VK_ICD_FILENAMES`, `__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES`, `__EGL_EXTERNAL_PLATFORM_CONFIG_DIRS`, `__EGL_VENDOR_LIBRARY_FILENAMES`, or `__EGL_VENDOR_LIBRARY_DIRS` inhibit automatic backend and queue decisions. `__GL_DisableDBus` and `__GL_MaxFramesAllowed` preserve their own explicit values. These defaults remain limited to the tested driver; the queue setting is validated empirically rather than claimed as a stable public Wayland API. Discovery finishes before mutation. Malformed manifests or read errors are reported without partial loader changes. Go rolls back earlier settings if a later environment update fails. No system configuration or driver package is changed.

The preferred workaround needs `libgbm1`, the Zink module from `libgl1-mesa-dri`, `libvulkan1`, and a working GPU Vulkan ICD. Debian recommends those runtime packages; its existing NVIDIA driver supplies the tested ICD. No Vulkan development headers are needed. The fallback requires the installed legacy backend. Explicit overrides can select the original leaking paths. This fix does not establish leak-free behavior on other drivers or unsupported configurations. Rust borrows GTK's EGL display and must not terminate it to compensate for vendor allocations.

## Verification

Run the optional no-capture check:

```sh
make test-egl-integration
python3 tools/validate-egl-lifecycle.py --baseline --output build/egl-baseline.json
```

The normal check builds a pure Rust executable against existing runtime SONAMEs and repeats five complete EGL lifecycles on both the main thread and a joined worker thread. It requires Wayland and the existing Rust/LLD sanitizer toolchain, but no capture device or media-core build. It forces leak detection and nonzero leak exits, rejects suppression files, and records child exit codes and effective settings. The baseline skips automatic compatibility; it fails with the original leaks on the tested default installation. Explicit caller environment values remain preserved.

For ASan color validation, build the instrumented media library and use its matching ASan runtime:

```sh
RUSTC_BOOTSTRAP=1 \
RUSTFLAGS='-Zsanitizer=address -C link-arg=-fuse-ld=lld' \
CARGO_TARGET_DIR="$PWD/build/rust-gpu-asan" \
cargo build --manifest-path core/rust/Cargo.toml --offline --lib

LD_PRELOAD=/usr/lib/llvm-23/lib/clang/23/lib/linux/libclang_rt.asan-x86_64.so \
PYTHONMALLOC=malloc \
ASAN_OPTIONS=detect_leaks=1:halt_on_error=1:abort_on_error=1 \
LSAN_OPTIONS=exitcode=23:report_objects=1 \
python3 tools/validate-dma-colors.py --library build/rust-gpu-asan/debug/libmygoframe_media.so \
  --width 3840 --height 2160 --fps 60 --repeat-frames 24 --poison-pack-state \
  --output build/dma-colors-asan.json
```

The preload path is the tested LLVM 23 runtime and must match the selected toolchain. The automatic-policy check exited zero without an ASan/LSan report. Limited/full each passed 24 varied frames, six-slot reuse, source hashes, readback canaries, and pack-state restoration. Maximum RGB errors remained three and two code values.

An eight-second asynchronous two-context check also exited zero: 464 live frames, 928 submissions, all six slots, no retained capture/fence threads, and zero source export FDs after cleanup. Final descriptor/thread counts matched baseline exactly at 27/1, with zero source and video descriptors. The native five-cycle main/worker checks also exited zero; the baseline without automatic settings still detected the original leaks and failed. Its sanitized throughput includes startup; use the release [benchmark](benchmark-video.md) for performance.

Real MyGo checks completed window recreation, resizing, fullscreen entry/exit, and GPU owner release. An actual Go-ASan application run with automatic policy and explicit compatibility variables unset mapped the preferred Wayland2, Mesa DRI/Zink, and NVIDIA libraries. Its original GBM and NVIDIA D-Bus allocation stacks were absent.

## Separate GUI allocation results

The full GUI process is not reported as leak-free. The subsequent [native text ownership audit](font-memory.md) adds terminal release of MyGo's own Pango map, context, fonts, and caches. It also identifies the smaller configuration signature as a Fontconfig relative-offset reachability false positive through native owner-retained and owner-released controls. The report remains visible. No production `FcFini` or font-cache suppression was added.

Native EGL, color, and asynchronous capture checks return zero with leak detection enabled. The separate full-GUI run retains its nonzero sanitizer result. This distinction prevents the successful driver fix from being reported as a resolution of every GUI allocation.
