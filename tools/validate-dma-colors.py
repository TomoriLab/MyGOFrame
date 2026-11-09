#!/usr/bin/env python3
"""Compare leased raw DMA-BUF GPU output with the production CPU converter."""

import argparse
from contextlib import contextmanager
import ctypes
import errno
import fcntl
import hashlib
import json
import math
import mmap
import os
import platform
from pathlib import Path
import statistics
import struct
import sys
import time
import zlib


class VideoConfig(ctypes.Structure):
    _fields_ = [
        ("path", ctypes.c_char_p), ("fourcc", ctypes.c_uint32),
        ("width", ctypes.c_uint32), ("height", ctypes.c_uint32),
        ("interval_numerator", ctypes.c_uint32),
        ("interval_denominator", ctypes.c_uint32), ("color_range", ctypes.c_int32),
    ]


class VideoInformation(ctypes.Structure):
    _fields_ = [(name, ctypes.c_uint32) for name in [
        "fourcc", "width", "height", "stride", "interval_numerator",
        "interval_denominator", "bytes_per_line", "quantization", "encoding",
        "colorspace", "transfer_function", "capture_buffers",
    ]]


class DmaFrameView(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32), ("struct_size", ctypes.c_uint32),
        ("token", ctypes.c_void_p), ("descriptor", ctypes.c_int32),
        ("index", ctypes.c_uint32), ("width", ctypes.c_uint32),
        ("height", ctypes.c_uint32), ("stride", ctypes.c_uint32),
        ("fourcc", ctypes.c_uint32), ("byte_length", ctypes.c_size_t),
        ("uv_offset", ctypes.c_uint64), ("modifier", ctypes.c_uint64),
        ("generation", ctypes.c_uint64), ("sequence", ctypes.c_uint64),
        ("timestamp_ns", ctypes.c_uint64), ("color_range", ctypes.c_int32),
        ("encoding", ctypes.c_uint32), ("colorspace", ctypes.c_uint32),
        ("transfer_function", ctypes.c_uint32),
    ]


class DeviceFormat(ctypes.Structure):
    _fields_ = [("type", ctypes.c_uint32), ("padding", ctypes.c_uint32),
                ("data", ctypes.c_uint8 * 200)]


def prepare_graphics_runtime(environment=None, directories=None, vendor_directories=None,
                             driver_version=None, library_directories=None):
    """Mirror internal/graphicsruntime policy before loading a graphics driver."""
    environment = os.environ if environment is None else environment
    if driver_version is None:
        try:
            driver_version = Path("/sys/module/nvidia/version").read_text().strip()
        except FileNotFoundError:
            return {}
        except (OSError, UnicodeError) as error:
            raise RuntimeError(f"read NVIDIA driver version: {error}") from error
    if driver_version != "615.78.08":
        return {}
    overrides = ("__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES", "__EGL_EXTERNAL_PLATFORM_CONFIG_DIRS",
                 "__EGL_VENDOR_LIBRARY_FILENAMES", "__EGL_VENDOR_LIBRARY_DIRS",
                 "GBM_BACKEND", "GBM_BACKENDS_PATH", "MESA_LOADER_DRIVER_OVERRIDE",
                 "VK_DRIVER_FILES", "VK_ICD_FILENAMES")
    backend = next((item.strip() for item in environment.get("GDK_BACKEND", "").split(",")
                    if item.strip()), "*")
    wayland = backend == "wayland" if backend != "*" else bool(
        environment.get("WAYLAND_DISPLAY") or environment.get("WAYLAND_SOCKET")
        or environment.get("XDG_SESSION_TYPE", "").lower() == "wayland"
    )
    selection_allowed = wayland and not any(key in environment for key in overrides)
    explicit_dbus = "__GL_DisableDBus" in environment
    if not selection_allowed and explicit_dbus:
        return {}
    directories = directories if directories is not None else [
        Path("/etc/egl/egl_external_platform.d"),
        Path("/usr/share/egl/egl_external_platform.d"),
    ]
    vendor_directories = vendor_directories if vendor_directories is not None else [
        Path("/etc/glvnd/egl_vendor.d"), Path("/usr/share/glvnd/egl_vendor.d"),
    ]

    def read_manifests(locations):
        manifests = []
        for directory in locations:
            try:
                paths = sorted(path for path in Path(directory).iterdir()
                               if path.name.endswith(".json") and not path.is_dir())
            except FileNotFoundError:
                continue
            except OSError as error:
                raise RuntimeError(f"read graphics manifest directory {directory}: {error}") from error
            for path in paths:
                try:
                    with path.open("rb") as stream:
                        data = stream.read(1024 * 1024 + 1)
                    if len(data) > 1024 * 1024:
                        raise ValueError("manifest exceeds the supported size")
                    document = json.loads(data)
                    library = document["ICD"]["library_path"]
                    if (document.get("file_format_version") != "1.0.0"
                            or not isinstance(library, str) or not library or "\0" in library):
                        raise ValueError("unsupported manifest version or library path")
                    if os.pathsep in str(path):
                        raise ValueError("manifest path contains the platform-list separator")
                except FileNotFoundError:
                    continue
                except (OSError, UnicodeError, ValueError, KeyError, TypeError, AttributeError) as error:
                    raise RuntimeError(f"decode graphics manifest {path}: {error}") from error
                manifests.append((path, Path(library).name))
        return manifests

    manifests = read_manifests(directories)
    libraries = {library for _, library in manifests}
    legacy, modern = "libnvidia-egl-wayland.so.1", "libnvidia-egl-wayland2.so.1"
    has_nvidia = bool(libraries & {legacy, modern})
    if not explicit_dbus:
        vendors = read_manifests(vendor_directories)
        has_nvidia = has_nvidia or any(library == "libEGL_nvidia.so.0" for _, library in vendors)
    changes = {}
    if has_nvidia and not explicit_dbus:
        changes["__GL_DisableDBus"] = "1"
    if library_directories is None:
        triplet = {"x86_64": "x86_64-linux-gnu", "aarch64": "aarch64-linux-gnu",
                   "i386": "i386-linux-gnu", "i686": "i386-linux-gnu",
                   "armv7l": "arm-linux-gnueabihf", "ppc64le": "powerpc64le-linux-gnu",
                   "riscv64": "riscv64-linux-gnu", "s390x": "s390x-linux-gnu"}.get(platform.machine())
        library_directories = ([Path("/usr/lib") / triplet, Path("/lib") / triplet] if triplet else [])
        if struct.calcsize("P") == 8:
            library_directories.extend([Path("/usr/lib64"), Path("/usr/lib"), Path("/lib64"), Path("/lib")])
        else:
            library_directories.extend([Path("/usr/lib"), Path("/lib")])
    runtime_available = any(all((Path(directory) / relative).is_file() for relative in
                               ("gbm/dri_gbm.so", "dri/zink_dri.so", "libvulkan.so.1"))
                            for directory in library_directories)
    if selection_allowed and modern in libraries and runtime_available:
        changes["GBM_BACKEND"] = "dri"
        changes["MESA_LOADER_DRIVER_OVERRIDE"] = "zink"
        if "__GL_MaxFramesAllowed" not in environment:
            changes["__GL_MaxFramesAllowed"] = "1"
    elif selection_allowed and legacy in libraries:
        if modern in libraries:
            changes["__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES"] = os.pathsep.join(
                str(path) for path, library in manifests if library != modern
            )
        if "__GL_MaxFramesAllowed" not in environment:
            changes["__GL_MaxFramesAllowed"] = "1"
    # Discovery completes before changing the process environment so errors leave it intact.
    environment.update(changes)
    return changes


class GraphicsContext:
    def __init__(self):
        self.runtime_changes = prepare_graphics_runtime()
        self.wayland = ctypes.CDLL("libwayland-client.so.0")
        self.egl = ctypes.CDLL("libEGL.so.1")
        self.connection = None
        self.display = None
        self.context = None
        self.connect = bind(self.wayland, "wl_display_connect", ctypes.c_void_p, ctypes.c_char_p)
        self.disconnect = bind(self.wayland, "wl_display_disconnect", None, ctypes.c_void_p)
        self.platform_display = bind(self.egl, "eglGetPlatformDisplay", ctypes.c_void_p,
                                     ctypes.c_uint32, ctypes.c_void_p, ctypes.POINTER(ctypes.c_ssize_t))
        self.initialize = bind(self.egl, "eglInitialize", ctypes.c_uint32, ctypes.c_void_p,
                               ctypes.POINTER(ctypes.c_int), ctypes.POINTER(ctypes.c_int))
        self.query = bind(self.egl, "eglQueryString", ctypes.c_char_p, ctypes.c_void_p, ctypes.c_int)
        self.bind_api = bind(self.egl, "eglBindAPI", ctypes.c_uint32, ctypes.c_uint32)
        self.create = bind(self.egl, "eglCreateContext", ctypes.c_void_p, ctypes.c_void_p,
                           ctypes.c_void_p, ctypes.c_void_p, ctypes.POINTER(ctypes.c_int))
        self.make_current = bind(self.egl, "eglMakeCurrent", ctypes.c_uint32, ctypes.c_void_p,
                                 ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p)
        self.destroy = bind(self.egl, "eglDestroyContext", ctypes.c_uint32,
                            ctypes.c_void_p, ctypes.c_void_p)
        self.terminate = bind(self.egl, "eglTerminate", ctypes.c_uint32, ctypes.c_void_p)
        self.error = bind(self.egl, "eglGetError", ctypes.c_uint32)
        self.get_proc = bind(self.egl, "eglGetProcAddress", ctypes.c_void_p, ctypes.c_char_p)
        self.finish = None

    def __enter__(self):
        try:
            self.connection = self.connect(None)
            if not self.connection:
                raise RuntimeError("A Wayland session is required.")
            self.display = self.platform_display(0x31D8, self.connection, None)
            major, minor = ctypes.c_int(), ctypes.c_int()
            if not self.initialize(self.display, ctypes.byref(major), ctypes.byref(minor)):
                raise RuntimeError(f"EGL initialization failed: {self.error():#x}.")
            if not self.bind_api(0x30A0):
                raise RuntimeError(f"GLES API selection failed: {self.error():#x}.")
            attributes = (ctypes.c_int * 3)(0x3098, 3, 0x3038)
            self.context = self.create(self.display, None, None, attributes)
            if not self.context or not self.make_current(self.display, None, None, self.context):
                raise RuntimeError(f"GLES context creation failed: {self.error():#x}.")
            address = self.get_proc(b"glFinish")
            if not address:
                raise RuntimeError("GLES completion entry point is unavailable.")
            self.finish = ctypes.CFUNCTYPE(None)(address)
            return self
        except BaseException:
            self.close()
            raise

    def close(self):
        if self.context:
            if self.finish:
                self.finish()
            self.make_current(self.display, None, None, None)
            self.destroy(self.display, self.context)
            self.context = None
        if self.display:
            self.terminate(self.display)
            self.display = None
        if self.connection:
            self.disconnect(self.connection)
            self.connection = None

    def __exit__(self, *unused):
        self.close()


class NativeCore:
    def __init__(self, path):
        self.library = ctypes.CDLL(str(path.resolve()))
        pointer_error = ctypes.POINTER(ctypes.c_void_p)
        self.create_video = bind(self.library, "mgfr_dma_video_create", ctypes.c_void_p,
                                 ctypes.POINTER(VideoConfig), ctypes.POINTER(VideoInformation), pointer_error)
        self.destroy_video = bind(self.library, "mgfr_dma_video_destroy", None, ctypes.c_void_p)
        self.acquire = bind(self.library, "mgfr_dma_video_acquire", ctypes.c_int32,
                            ctypes.c_void_p, ctypes.c_uint64, ctypes.POINTER(DmaFrameView))
        self.release = bind(self.library, "mgfr_dma_video_release", None, ctypes.c_void_p)
        self.reference = bind(self.library, "mgfr_dma_copy_rgba", ctypes.c_int32,
                              ctypes.c_void_p, ctypes.c_void_p, ctypes.c_size_t, pointer_error)
        self.create_gpu = bind(self.library, "mgfr_gpu_create", ctypes.c_void_p, pointer_error)
        self.destroy_gpu = bind(self.library, "mgfr_gpu_destroy", None, ctypes.c_void_p)
        self.readback = bind(self.library, "mgfr_gpu_readback", ctypes.c_int32,
                             ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p,
                             ctypes.c_size_t, pointer_error)
        self.free_error = bind(self.library, "mgfr_string_free", None, ctypes.c_void_p)

    def take_error(self, pointer):
        if not pointer.value:
            return "The native core did not provide an error message."
        try:
            return ctypes.string_at(pointer.value).decode("utf-8", "replace")
        finally:
            self.free_error(pointer)


class CacheFlushDiagnostic:
    def __init__(self, optimized=False):
        if platform.machine() != "x86_64":
            raise RuntimeError("The cache-flush control requires Linux x86-64.")
        cpu = Path("/proc/cpuinfo").read_text()
        flags = next(line.split(":", 1)[1].split() for line in cpu.splitlines() if line.startswith("flags"))
        size = next(int(line.split(":", 1)[1]) for line in cpu.splitlines() if line.startswith("clflush size"))
        if "clflush" not in flags or size != 64:
            raise RuntimeError("The diagnostic requires CLFLUSH with 64-byte cache lines.")
        if optimized and "clflushopt" not in flags:
            raise RuntimeError("The optimized diagnostic requires CLFLUSHOPT support.")
        self.capabilities = {"clflush": True, "clflushopt": "clflushopt" in flags, "cacheLineBytes": size,
                             "method": "clflushopt-sfence" if optimized else "clflush-mfence"}
        # These SysV x86-64 routines finish the cache-line writeback before GPU access.
        code = bytes.fromhex("48 85 f6 74 11 48 8d 04 37 66 0f ae 3f 48 83 c7 40 48 39 c7 72 f3 0f ae f8 c3"
                             if optimized else "48 85 f6 74 10 48 8d 04 37 0f ae 3f 48 83 c7 40 48 39 c7 72 f4 0f ae f0 c3")
        self.mapping = mmap.mmap(-1, mmap.PAGESIZE, prot=mmap.PROT_READ | mmap.PROT_WRITE)
        self.mapping.write(code)
        address = ctypes.addressof(ctypes.c_uint8.from_buffer(self.mapping))
        library = ctypes.CDLL(None, use_errno=True)
        protect = bind(library, "mprotect", ctypes.c_int, ctypes.c_void_p, ctypes.c_size_t, ctypes.c_int)
        if protect(address, mmap.PAGESIZE, mmap.PROT_READ | mmap.PROT_EXEC) != 0:
            self.mapping.close()
            raise OSError(ctypes.get_errno(), "The diagnostic code page could not be made executable.")
        self.function = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_size_t)(address)

    def flush(self, mapping, length):
        address = ctypes.addressof(ctypes.c_uint8.from_buffer(mapping))
        started = time.perf_counter_ns()
        self.function(address, length)
        return time.perf_counter_ns() - started

    def close(self):
        self.function = None
        self.mapping.close()


def bind(library, name, result, *arguments):
    function = getattr(library, name)
    function.restype = result
    function.argtypes = list(arguments)
    return function


def ioctl_retry(descriptor, request, value):
    while True:
        try:
            fcntl.ioctl(descriptor, request, value, True)
            return
        except OSError as error:
            if error.errno not in (errno.EINTR, errno.EAGAIN):
                raise


def synchronize(descriptor, ending, writing=True):
    flags = (2 if writing else 1) | (4 if ending else 0)
    ioctl_retry(descriptor, 0x40086200, bytearray(struct.pack("=Q", flags)))


def source_digest(view):
    synchronize(view.descriptor, False, writing=False)
    try:
        with mmap.mmap(view.descriptor, view.byte_length,
                       flags=mmap.MAP_SHARED, prot=mmap.PROT_READ) as mapping:
            return hashlib.sha256(mapping).hexdigest()
    finally:
        synchronize(view.descriptor, True, writing=False)


def encode_rgb(red, green, blue, full_range, encoding, depth=8):
    red_weight, blue_weight = (0.299, 0.114) if encoding == 1 else (0.2126, 0.0722)
    luminance = red_weight * red + (1 - red_weight - blue_weight) * green + blue_weight * blue
    maximum = (1 << depth) - 1
    factor = 1 << (depth - 8)
    neutral = 128 * factor
    luminance_scale, chroma_scale, offset = ((maximum, maximum / 2, 0) if full_range
                                              else (219 * factor, 112 * factor, 16 * factor))
    clamp = lambda value: min(maximum, max(0, round(value)))
    return (
        clamp(offset + luminance_scale * luminance / 255),
        clamp(neutral + chroma_scale * (blue - luminance) / (255 * (1 - blue_weight))),
        clamp(neutral + chroma_scale * (red - luminance) / (255 * (1 - red_weight))),
    )


def pattern_patches(view, frame_index=0, alternating_luma=False):
    full_range = view.color_range == 2
    format_name = view.fourcc.to_bytes(4, "little").decode("ascii")
    depth = 10 if format_name == "P010" else 8
    factor = 1 << (depth - 8)
    neutral = 128 * factor
    minimum, maximum = (0, (1 << depth) - 1) if full_range else (16 * factor, 235 * factor)
    chroma_minimum, chroma_maximum = (0, (1 << depth) - 1) if full_range else (16 * factor, 240 * factor)
    rgb = format_name in ("BGR3", "RGB3")
    colors = [
        ("white", (255, 255, 255)), ("yellow", (255, 255, 0)),
        ("cyan", (0, 255, 255)), ("green", (0, 255, 0)),
        ("magenta", (255, 0, 255)), ("red", (255, 0, 0)),
        ("blue", (0, 0, 255)), ("black", (0, 0, 0)),
    ]
    def encode(color):
        if rgb:
            return tuple(round(minimum + (maximum - minimum) * channel / 255) for channel in color)
        return encode_rgb(*color, full_range, view.encoding, depth)
    rows = [[(name, encode(color), color) for name, color in colors]]
    gray_codes = [minimum, minimum + 1, minimum + 2, 64 * factor + 3,
                  128 * factor + 1, 192 * factor + 2, maximum - 1, maximum]
    if frame_index:
        gray_codes[1:-1] = [minimum + 1 + (code - minimum - 1 + 17 * frame_index)
                           % (maximum - minimum - 1) for code in gray_codes[1:-1]]
    rows.append([(f"gray-{index}", ((code, code, code) if rgb else (code, neutral, neutral)), None) for index, code in enumerate(gray_codes)])
    middle = round((minimum + maximum) / 2)
    chroma_pairs = [
        (chroma_minimum, neutral), (chroma_maximum, neutral), (neutral, chroma_minimum),
        (neutral, chroma_maximum), (64 * factor + 1, 192 * factor + 2), (192 * factor + 3, 64 * factor),
        (chroma_minimum, chroma_minimum), (chroma_maximum, chroma_maximum),
    ]
    if frame_index:
        chroma_span = chroma_maximum - chroma_minimum + 1
        chroma_pairs = [(chroma_minimum + (blue - chroma_minimum + 11 * frame_index) % chroma_span,
                         chroma_minimum + (red - chroma_minimum + 29 * frame_index) % chroma_span)
                        for blue, red in chroma_pairs]
    rows.append([(f"chroma-{index}", (middle, blue, red), None)
                 for index, (blue, red) in enumerate(chroma_pairs)])
    patches = []
    for row_index, row in enumerate(rows):
        top = (view.height * row_index // 3) & ~1
        bottom = (view.height * (row_index + 1) // 3) & ~1
        for column_index, (name, values, original_rgb) in enumerate(row):
            left = (view.width * column_index // 8) & ~1
            right = (view.width * (column_index + 1) // 8) & ~1
            patches.append({"name": name, "bounds": [left, top, right, bottom],
                            "source": list(values), "inputRGB": original_rgb,
                            "alternateLuma": alternating_luma and name.startswith("gray-")
                            and name not in ("gray-0", "gray-7") and not rgb})
    return patches


def write_pattern(view, patches, flush_mapping=False, cache_flusher=None):
    if view.abi_version != 1 or view.struct_size != ctypes.sizeof(DmaFrameView):
        raise RuntimeError("The native DMA frame ABI does not match this tool.")
    format_name = view.fourcc.to_bytes(4, "little").decode("ascii")
    if view.modifier != 0 or format_name not in ("NV12", "P010", "YUYV", "UYVY", "BGR3", "RGB3"):
        raise RuntimeError("Reference patterns require a supported linear raw format.")
    synchronize(view.descriptor, False)
    flush_duration = None
    try:
        with mmap.mmap(view.descriptor, view.byte_length,
                       flags=mmap.MAP_SHARED, prot=mmap.PROT_READ | mmap.PROT_WRITE) as mapping:
            for patch in patches:
                left, top, right, bottom = patch["bounds"]
                first, second, third = patch["source"]
                # Alternating luma values expose packed-pixel parity and P010 low-bit errors.
                alternate = first
                if patch.get("alternateLuma", False):
                    alternate = max(0, first - 17)
                count = right - left
                if format_name in ("NV12", "P010"):
                    if format_name == "NV12":
                        luminance_row = bytes([first, alternate]) * (count // 2)
                        chroma_row = bytes([second, third]) * (count // 2)
                        bytes_per_sample = 1
                    else:
                        luminance_row = struct.pack("<HH", first << 6, alternate << 6) * (count // 2)
                        chroma_row = struct.pack("<HH", second << 6, third << 6) * (count // 2)
                        bytes_per_sample = 2
                    for row in range(top, bottom):
                        offset = row * view.stride + left * bytes_per_sample
                        mapping[offset:offset + len(luminance_row)] = luminance_row
                    for row in range(top // 2, bottom // 2):
                        offset = view.uv_offset + row * view.stride + left * bytes_per_sample
                        mapping[offset:offset + len(chroma_row)] = chroma_row
                else:
                    if format_name == "YUYV":
                        pixels = bytes([first, second, alternate, third])
                        row_bytes = pixels * (count // 2)
                        bytes_per_pixel = 2
                    elif format_name == "UYVY":
                        pixels = bytes([second, first, third, alternate])
                        row_bytes = pixels * (count // 2)
                        bytes_per_pixel = 2
                    else:
                        pixels = (first, second, third)
                        if format_name == "BGR3":
                            pixels = pixels[::-1]
                        row_bytes = bytes(pixels) * count
                        bytes_per_pixel = 3
                    for row in range(top, bottom):
                        offset = row * view.stride + left * bytes_per_pixel
                        mapping[offset:offset + len(row_bytes)] = row_bytes
            if flush_mapping:
                mapping.flush()
            if cache_flusher is not None:
                flush_duration = cache_flusher.flush(mapping, view.byte_length)
    finally:
        synchronize(view.descriptor, True)
    return flush_duration


def expected_rgb(view, patch, column):
    format_name = view.fourcc.to_bytes(4, "little").decode("ascii")
    samples = list(patch["source"])
    if patch.get("alternateLuma", False) and (column - patch["bounds"][0]) % 2:
        samples[0] = max(0, samples[0] - 17)
    full_range = view.color_range == 2
    if format_name in ("BGR3", "RGB3"):
        channels = samples if full_range else [(value - 16) * 255 / 219 for value in samples]
    else:
        factor = 4 if format_name == "P010" else 1
        maximum = 1023 if factor == 4 else 255
        black = 0 if full_range else 16 * factor
        luma_span = maximum if full_range else 219 * factor
        chroma_span = maximum if full_range else 224 * factor
        luminance = (samples[0] - black) / luma_span
        blue = (samples[1] - 128 * factor) / chroma_span
        red = (samples[2] - 128 * factor) / chroma_span
        red_weight, blue_weight = (0.299, 0.114) if view.encoding == 1 else (0.2126, 0.0722)
        green_weight = 1 - red_weight - blue_weight
        channels = [255 * (luminance + 2 * (1 - red_weight) * red),
                    255 * (luminance - 2 * blue_weight * (1 - blue_weight) / green_weight * blue
                           - 2 * red_weight * (1 - red_weight) / green_weight * red),
                    255 * (luminance + 2 * (1 - blue_weight) * blue)]
    return [max(0, min(255, round(value))) for value in channels]


def compare_images(view, patches, reference, gpu, maximum_error):
    errors = []
    alpha_errors = []
    patch_results = []
    for patch in patches:
        left, top, right, bottom = patch["bounds"]
        channel_errors = []
        worst_samples = []
        for row_index in range(8):
            row = top + 4 + (bottom - top - 9) * row_index // 7
            for column_index in range(8):
                column = left + 4 + (right - left - 9) * column_index // 7
                offset = (row * view.width + column) * 4
                reference_rgb = (list(reference[offset:offset + 3]) if reference is not None
                                 else expected_rgb(view, patch, column))
                sample_errors = [abs(reference_rgb[channel] - gpu[offset + channel]) for channel in range(3)]
                channel_errors.extend(sample_errors)
                worst_samples.append({"x": column, "y": row, "maximumError": max(sample_errors),
                                      "referenceRGB": reference_rgb,
                                      "gpuRGB": list(gpu[offset:offset + 3])})
                alpha_errors.append(abs(255 - gpu[offset + 3]))
        center = (((top + bottom) // 2) * view.width + (left + right) // 2) * 4
        summary = dict(patch)
        summary.update(referenceRGB=(list(reference[center:center + 3]) if reference is not None
                                     else expected_rgb(view, patch, (left + right) // 2)),
                       gpuRGB=list(gpu[center:center + 3]),
                       maximumError=max(channel_errors), meanError=statistics.mean(channel_errors),
                       worstSamples=sorted(worst_samples, key=lambda sample: sample["maximumError"], reverse=True)[:3])
        patch_results.append(summary)
        errors.extend(channel_errors)
    errors.sort()
    endpoints = [item for item in patch_results if item["name"] in ("white", "black")]
    endpoint_errors = [abs(value - (255 if item["name"] == "white" else 0))
                       for item in endpoints for value in item["gpuRGB"]]
    maximum = max(errors)
    return {
        "comparison": "RGB channels sampled at 8x8 interior points in each of 24 raw-format patches",
        "reference": "production FFmpeg converter" if reference is not None else "analytic source-sample conversion",
        "sampledChannelCount": len(errors), "fullFrameCompared": False,
        "maximumError": maximum, "meanError": statistics.mean(errors),
        "p95Error": errors[math.ceil((len(errors) - 1) * 0.95)],
        "maximumAlphaError": max(alpha_errors),
        "grayEndpointMaximumError": max(endpoint_errors),
        "maximumAllowedError": maximum_error,
        "passed": maximum <= maximum_error and max(alpha_errors) == 0 and max(endpoint_errors) <= maximum_error,
        "patches": patch_results,
    }


def dump_image(directory, name, image, width, height):
    directory.mkdir(parents=True, exist_ok=True)
    rgba = ctypes.string_at(ctypes.addressof(image), width * height * 4)
    rgb = bytearray(width * height * 3)
    for channel in range(3):
        rgb[channel::3] = rgba[channel::4]
    raw = directory / f"{name}.rgb"
    raw.write_bytes(rgb)
    row_length = width * 3
    filtered = bytearray(height * (row_length + 1))
    for row in range(height):
        target = row * (row_length + 1) + 1
        filtered[target:target + row_length] = rgb[row * row_length:(row + 1) * row_length]
    def chunk(kind, payload):
        return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", zlib.crc32(kind + payload))
    png = directory / f"{name}.png"
    png.write_bytes(b"\x89PNG\r\n\x1a\n"
                    + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
                    + chunk(b"IDAT", zlib.compress(filtered)) + chunk(b"IEND", b""))
    return {"png": str(png), "rawRGB": str(raw)}


@contextmanager
def packed_readback_state(graphics, width, enabled):
    result = {"poisoned": enabled, "restored": True}
    if not enabled:
        yield result
        return
    def entry(name, return_type, *arguments):
        address = graphics.get_proc(name.encode())
        if not address:
            raise RuntimeError(f"The GLES entry point {name} is unavailable.")
        return ctypes.CFUNCTYPE(return_type, *arguments)(address)
    get_integer = entry("glGetIntegerv", None, ctypes.c_uint32, ctypes.POINTER(ctypes.c_int))
    pixel_store = entry("glPixelStorei", None, ctypes.c_uint32, ctypes.c_int)
    generate = entry("glGenBuffers", None, ctypes.c_int, ctypes.POINTER(ctypes.c_uint32))
    bind_buffer = entry("glBindBuffer", None, ctypes.c_uint32, ctypes.c_uint32)
    buffer_data = entry("glBufferData", None, ctypes.c_uint32, ctypes.c_ssize_t,
                        ctypes.c_void_p, ctypes.c_uint32)
    delete = entry("glDeleteBuffers", None, ctypes.c_int, ctypes.POINTER(ctypes.c_uint32))
    def integer(key):
        value = ctypes.c_int()
        get_integer(key, ctypes.byref(value))
        return value.value
    keys = {"alignment": 0x0D05, "rowLength": 0x0D02,
            "skipRows": 0x0D03, "skipPixels": 0x0D04}
    original = {name: integer(key) for name, key in keys.items()}
    original_buffer = integer(0x88ED)
    buffer = ctypes.c_uint32()
    generate(1, ctypes.byref(buffer))
    expected = {"alignment": 8, "rowLength": width + 16, "skipRows": 2, "skipPixels": 3}
    try:
        bind_buffer(0x88EB, buffer.value)
        buffer_data(0x88EB, 64, None, 0x88E1)
        for name, key in keys.items():
            pixel_store(key, expected[name])
        result["expected"] = dict(expected, packBuffer=buffer.value)
        yield result
        actual = {name: integer(key) for name, key in keys.items()}
        actual["packBuffer"] = integer(0x88ED)
        result["actual"] = actual
        result["restored"] = actual == result["expected"]
    finally:
        for name, key in keys.items():
            pixel_store(key, original[name])
        bind_buffer(0x88EB, original_buffer)
        delete(1, ctypes.byref(buffer))


def acquire_frame(core, session, after_sequence):
    view = DmaFrameView()
    deadline = time.monotonic() + 5
    while True:
        available = core.acquire(session, after_sequence, ctypes.byref(view))
        if available == 1:
            return view
        if available < 0:
            raise RuntimeError("DMA frame acquisition failed.")
        if time.monotonic() >= deadline:
            raise RuntimeError("No new DMA frame was acquired within five seconds.")
        time.sleep(0.001)


def validate_frame(core, graphics, arguments, renderer, view, color_range, frame_index,
                   cache_flusher=None):
    name = "limited" if color_range == 1 else "full"
    failure = ctypes.c_void_p()
    try:
        patches = pattern_patches(view, frame_index, arguments.alternating_luma)
        flush_duration = write_pattern(view, patches, arguments.flush_mapping, cache_flusher)
        digest_before = source_digest(view)
        length = view.width * view.height * 4
        reference = (ctypes.c_uint8 * length)()
        guard_length = (view.width + 16) * (view.height + 3) * 4 - length + 64
        gpu = (ctypes.c_uint8 * (length + guard_length))()
        ctypes.memset(gpu, 0xA5, length + guard_length)
        if not core.reference(view.token, reference, length, ctypes.byref(failure)):
            raise RuntimeError(core.take_error(failure))
        with packed_readback_state(graphics, view.width, arguments.poison_pack_state) as pack_state:
            if not core.readback(renderer, view.token, gpu, length, ctypes.byref(failure)):
                raise RuntimeError(core.take_error(failure))
        digest_after = source_digest(view)
        reference_after = (ctypes.c_uint8 * length)()
        if not core.reference(view.token, reference_after, length, ctypes.byref(failure)):
            raise RuntimeError(core.take_error(failure))
        reference_change = compare_images(view, patches, reference, reference_after, 0)
        cpu_comparison = compare_images(view, patches, reference, gpu, arguments.maximum_error)
        analytic_comparison = compare_images(view, patches, None, gpu, arguments.maximum_error)
        # Alternating pixels test byte addressing against the exact source samples. The CPU
        # converter may resample them, so its complete comparison remains a separate result.
        result = dict(analytic_comparison if arguments.alternating_luma else cpu_comparison)
        result["analyticComparison"] = analytic_comparison
        if arguments.alternating_luma:
            result["cpuComparison"] = cpu_comparison
        result["passed"] = result["passed"] and analytic_comparison["passed"]
        guard_unchanged = all(value == 0xA5 for value in gpu[length:])
        result.update(format=view.fourcc.to_bytes(4, "little").decode("ascii"), range=name, frameIndex=frame_index, width=view.width, height=view.height, stride=view.stride,
                      encoding=view.encoding, generation=view.generation,
                      capturedSequence=view.sequence, sourceBytes=view.byte_length,
                      captureSlot=view.index,
                      producerStoppedBeforePattern=arguments.stop_before_pattern,
                      mappingFlushed=arguments.flush_mapping,
                      cpuCacheFlushDurationNs=flush_duration,
                      finishAfterCreate=arguments.finish_after_create,
                      sourceBeforeSHA256=digest_before, sourceAfterSHA256=digest_after,
                      sourceUnchanged=digest_before == digest_after,
                      cpuReferenceMaximumChange=reference_change["maximumError"],
                      packState=pack_state, guardBytes=guard_length, guardUnchanged=guard_unchanged)
        descriptor_info = Path(f"/proc/self/fdinfo/{view.descriptor}").read_text()
        result["dmaBufExporter"] = next((line.split(":", 1)[1].strip() for line in descriptor_info.splitlines()
                                         if line.startswith("exp_name:")), "unknown")
        result["passed"] = (result["passed"] and pack_state["restored"] and guard_unchanged
                            and digest_before == digest_after and reference_change["maximumError"] == 0)
        if arguments.dump_images and not result["passed"]:
            result["debugImages"] = {
                "reference": dump_image(arguments.dump_images, f"{name}-{frame_index}-reference", reference, view.width, view.height),
                "gpu": dump_image(arguments.dump_images, f"{name}-{frame_index}-gpu", gpu, view.width, view.height),
            }
        return result
    finally:
        core.release(view.token)


def validate_range(core, graphics, arguments, color_range, cache_flusher=None):
    session = None
    renderer = None
    try:
        config = VideoConfig(arguments.device.encode(), int.from_bytes(arguments.format.encode("ascii"), "little"),
                             arguments.width, arguments.height, 1, arguments.fps, color_range)
        information = VideoInformation()
        failure = ctypes.c_void_p()
        session = core.create_video(ctypes.byref(config), ctypes.byref(information), ctypes.byref(failure))
        if not session:
            raise RuntimeError(core.take_error(failure))
        if information.fourcc != config.fourcc or (information.width, information.height) != (config.width, config.height):
            raise RuntimeError("The device did not negotiate the requested format and dimensions.")
        renderer = core.create_gpu(ctypes.byref(failure))
        if not renderer:
            raise RuntimeError(core.take_error(failure))
        if arguments.finish_after_create:
            graphics.finish()
        frames = []
        after_sequence = 0
        for frame_index in range(arguments.repeat_frames):
            view = acquire_frame(core, session, after_sequence)
            after_sequence = view.sequence
            if arguments.stop_before_pattern:
                core.destroy_video(session)
                session = None
            frames.append(validate_frame(core, graphics, arguments, renderer, view, color_range,
                                         frame_index, cache_flusher))
        if len(frames) == 1:
            return frames[0]
        slots = [frame["captureSlot"] for frame in frames]
        errors = [frame["meanError"] for frame in frames]
        return {
            "range": "limited" if color_range == 1 else "full",
            "format": arguments.format, "width": information.width, "height": information.height,
            "frameCount": len(frames), "singleSession": True, "singleRenderer": True,
            "patternsVaryPerFrame": True, "captureSlots": sorted(set(slots)),
            "reusedSlotCount": len(slots) - len(set(slots)),
            "maximumError": max(frame["maximumError"] for frame in frames),
            "meanError": statistics.mean(errors),
            "maximumFrameP95Error": max(frame["p95Error"] for frame in frames),
            "passed": all(frame["passed"] for frame in frames), "frames": frames,
        }
    finally:
        if graphics.finish:
            graphics.finish()
        if renderer:
            core.destroy_gpu(renderer)
        if session:
            core.destroy_video(session)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    root = Path(__file__).resolve().parent.parent
    parser.add_argument("--device", default="/dev/video0", help="Select the exclusive V4L2 capture node.")
    parser.add_argument("--library", type=Path, default=root / "build/libmygoframe_media.so")
    parser.add_argument("--format", choices=("NV12", "P010", "YUYV", "UYVY", "BGR3", "RGB3"), default="NV12")
    parser.add_argument("--width", type=int, default=640)
    parser.add_argument("--height", type=int, default=480)
    parser.add_argument("--fps", type=int, default=60)
    parser.add_argument("--ranges", nargs="+", choices=("limited", "full"),
                        default=["limited", "full"], help="Select the range cases and their execution order.")
    parser.add_argument("--repeat-frames", type=int, default=1,
                        help="Validate this many new frame leases in one session and renderer per range.")
    parser.add_argument("--maximum-error", type=int, default=3,
                        help="Allow at most this many 8-bit RGB code values against the CPU converter.")
    parser.add_argument("--alternating-luma", action="store_true",
                        help="Compare alternating pixel luma against exact source values; retain FFmpeg differences separately.")
    parser.add_argument("--poison-pack-state", action="store_true",
                        help="Verify exact readback capacity and restoration of nondefault GL packing state.")
    parser.add_argument("--stop-before-pattern", action="store_true",
                        help="Stop capture after leasing a frame to isolate producer writes from GPU sampling.")
    parser.add_argument("--flush-mapping", action="store_true",
                        help="Issue mmap.flush after writing the reference pattern as a diagnostic control.")
    parser.add_argument("--finish-after-create", action="store_true",
                        help="Complete prior GL work after renderer creation as a diagnostic control.")
    parser.add_argument("--dump-images", type=Path,
                        help="Save failed synthetic-pattern reference and GPU output as PNG and raw RGB.")
    cache_controls = parser.add_mutually_exclusive_group()
    cache_controls.add_argument("--clflush-control", action="store_true",
                                help="Use a diagnostic-only x86-64 CLFLUSH/MFENCE routine before GPU import.")
    cache_controls.add_argument("--clflushopt-control", action="store_true",
                                help="Use a diagnostic-only x86-64 CLFLUSHOPT/SFENCE routine before GPU import.")
    parser.add_argument("--output", type=Path, help="Write the complete JSON result to this path.")
    arguments = parser.parse_args()
    if arguments.width < 128 or arguments.height < 96 or (arguments.width | arguments.height) & 1:
        parser.error("Dimensions must be even and at least 128 by 96.")
    if arguments.fps <= 0 or not 0 <= arguments.maximum_error <= 255:
        parser.error("Frame rate and maximum error are outside the supported range.")
    if not 1 <= arguments.repeat_frames <= 1000:
        parser.error("The repeated-frame count must be between 1 and 1000.")
    if arguments.stop_before_pattern and arguments.repeat_frames != 1:
        parser.error("Stopped-producer validation supports exactly one frame per range.")
    if ctypes.sizeof(DeviceFormat) != 208 or ctypes.sizeof(DmaFrameView) != 104:
        parser.error("This diagnostic currently requires the Linux 64-bit native ABI.")
    result = {"device": arguments.device, "tests": [], "alternatingLuma": arguments.alternating_luma,
              "acceptanceReference": "analytic source samples" if arguments.alternating_luma else "FFmpeg and analytic source samples",
              "method": "Overwrite held capture leases with known raw-format patterns before GPU submission.",
              "thresholdRationale": "Three 8-bit code values allow conversion coefficient and rounding differences; patch boundaries are excluded.",
              "endToEndDisplayColorMeasured": False}
    descriptor = None
    original = None
    cache_flusher = None
    try:
        if arguments.clflush_control or arguments.clflushopt_control:
            cache_flusher = CacheFlushDiagnostic(arguments.clflushopt_control)
            result["cpuCacheFlushCapabilities"] = cache_flusher.capabilities
        descriptor = os.open(arguments.device, os.O_RDWR | os.O_CLOEXEC)
        original = DeviceFormat()
        original.type = 1
        ioctl_retry(descriptor, 0xC0D05604, original)
        core = NativeCore(arguments.library)
        with GraphicsContext() as graphics:
            result["eglVendor"] = graphics.query(graphics.display, 0x3053).decode()
            result["graphicsRuntime"] = {
                "changes": graphics.runtime_changes,
                "disableNvidiaDBus": os.environ.get("__GL_DisableDBus"),
                "externalPlatformFiles": os.environ.get("__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES"),
                "gbmBackend": os.environ.get("GBM_BACKEND"),
                "mesaDriver": os.environ.get("MESA_LOADER_DRIVER_OVERRIDE"),
                "maximumFramesAllowed": os.environ.get("__GL_MaxFramesAllowed"),
            }
            for name in arguments.ranges:
                selection = 1 if name == "limited" else 2
                result["tests"].append(validate_range(core, graphics, arguments, selection, cache_flusher))
        result["passed"] = all(test["passed"] for test in result["tests"])
    except Exception as error:
        result["passed"] = False
        result["error"] = str(error)
    finally:
        if descriptor is not None:
            if original is not None:
                try:
                    deadline = time.monotonic() + 1
                    while True:
                        try:
                            ioctl_retry(descriptor, 0xC0D05605, original)
                            result["originalFormatRestored"] = True
                            break
                        except OSError as error:
                            if error.errno != errno.EBUSY or time.monotonic() >= deadline:
                                raise
                            time.sleep(0.005)
                except Exception as error:
                    result["restoreError"] = str(error)
                    result["passed"] = False
            os.close(descriptor)
        if cache_flusher is not None:
            cache_flusher.close()
    encoded = json.dumps(result, indent=2)
    if arguments.output:
        arguments.output.parent.mkdir(parents=True, exist_ok=True)
        arguments.output.write_text(encoded + "\n")
    print(encoded)
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
