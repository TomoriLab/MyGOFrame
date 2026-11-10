use crate::capture::domain::format::DmaFormat;
use crate::capture::infrastructure::dmabuf as dma;
use crate::{ffi, make_c_string};
use std::collections::{HashMap, VecDeque};
use std::ffi::{CStr, c_char, c_void};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::ptr;
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Duration;

type Display = *mut c_void;
type Image = *mut c_void;
type Sync = *mut c_void;

unsafe extern "C" {
    fn dlopen(name: *const c_char, flags: i32) -> *mut c_void;
    fn dlsym(handle: *mut c_void, name: *const c_char) -> *mut c_void;
    fn fcntl(descriptor: i32, command: i32, ...) -> i32;
}

// Every signature is matched to the EGL or GLES entry point named at the call site.
unsafe fn function<T: Copy>(library: *mut c_void, name: &CStr) -> Result<T, String> {
    let address = unsafe { dlsym(library, name.as_ptr()) };
    if address.is_null() || std::mem::size_of::<T>() != std::mem::size_of_val(&address) {
        return Err(format!(
            "Missing graphics entry point {}.",
            name.to_string_lossy()
        ));
    }
    Ok(unsafe { std::mem::transmute_copy(&address) })
}

macro_rules! graphics_functions {
    ($( $field:ident : $signature:ty = $name:literal; )*) => {
        struct Graphics {
            get_current_display: unsafe extern "C" fn() -> Display,
            get_current_context: unsafe extern "C" fn() -> *mut c_void,
            query_string: unsafe extern "C" fn(Display, i32) -> *const c_char,
            create_image: unsafe extern "C" fn(Display, *mut c_void, u32, *mut c_void, *const i32) -> Image,
            destroy_image: unsafe extern "C" fn(Display, Image) -> u32,
            create_sync: unsafe extern "C" fn(Display, u32, *const i32) -> Sync,
            destroy_sync: unsafe extern "C" fn(Display, Sync) -> u32,
            duplicate_fence: unsafe extern "C" fn(Display, Sync) -> i32,
            image_target: unsafe extern "C" fn(u32, Image),
            $( $field: $signature, )*
        }
        impl Graphics {
            fn load() -> Result<Self, String> {
                // The process keeps EGL loaded because the function table is process-global.
                let library = unsafe { dlopen(c"libEGL.so.1".as_ptr(), 2 | 0x1000) };
                if library.is_null() { return Err("Unable to load the EGL runtime.".into()); }
                let get_proc: unsafe extern "C" fn(*const c_char)->*mut c_void =
                    unsafe { function(library, c"eglGetProcAddress")? };
                unsafe fn extension<T: Copy>(
                    get_proc: unsafe extern "C" fn(*const c_char)->*mut c_void, name: &CStr
                ) -> Result<T, String> {
                    let address = unsafe { get_proc(name.as_ptr()) };
                    if address.is_null() || std::mem::size_of::<T>() != std::mem::size_of_val(&address) {
                        return Err(format!("Missing graphics extension {}.", name.to_string_lossy()));
                    }
                    Ok(unsafe { std::mem::transmute_copy(&address) })
                }
                Ok(Self {
                    get_current_display: unsafe { function(library,c"eglGetCurrentDisplay")? },
                    get_current_context: unsafe { function(library,c"eglGetCurrentContext")? },
                    query_string: unsafe { function(library,c"eglQueryString")? },
                    create_image: unsafe { extension(get_proc,c"eglCreateImageKHR")? },
                    destroy_image: unsafe { extension(get_proc,c"eglDestroyImageKHR")? },
                    create_sync: unsafe { extension(get_proc,c"eglCreateSyncKHR")? },
                    destroy_sync: unsafe { extension(get_proc,c"eglDestroySyncKHR")? },
                    duplicate_fence: unsafe { extension(get_proc,c"eglDupNativeFenceFDANDROID")? },
                    image_target: unsafe { extension(get_proc,c"glEGLImageTargetTexture2DOES")? },
                    $( $field: unsafe { extension(get_proc, CStr::from_bytes_with_nul_unchecked(concat!($name,"\0").as_bytes()))? }, )*
                })
            }
        }
    }
}
graphics_functions! {
    get_string: unsafe extern "C" fn(u32)->*const u8 = "glGetString";
    get_integer: unsafe extern "C" fn(u32,*mut i32) = "glGetIntegerv";
    get_error: unsafe extern "C" fn()->u32 = "glGetError";
    is_enabled: unsafe extern "C" fn(u32)->u8 = "glIsEnabled";
    enable: unsafe extern "C" fn(u32) = "glEnable";
    disable: unsafe extern "C" fn(u32) = "glDisable";
    color_mask: unsafe extern "C" fn(u8,u8,u8,u8) = "glColorMask";
    gen_textures: unsafe extern "C" fn(i32,*mut u32) = "glGenTextures";
    delete_textures: unsafe extern "C" fn(i32,*const u32) = "glDeleteTextures";
    bind_texture: unsafe extern "C" fn(u32,u32) = "glBindTexture";
    active_texture: unsafe extern "C" fn(u32) = "glActiveTexture";
    texture_parameter: unsafe extern "C" fn(u32,u32,i32) = "glTexParameteri";
    texture_image: unsafe extern "C" fn(u32,i32,i32,i32,i32,i32,u32,u32,*const c_void) = "glTexImage2D";
    gen_framebuffers: unsafe extern "C" fn(i32,*mut u32) = "glGenFramebuffers";
    delete_framebuffers: unsafe extern "C" fn(i32,*const u32) = "glDeleteFramebuffers";
    bind_framebuffer: unsafe extern "C" fn(u32,u32) = "glBindFramebuffer";
    framebuffer_texture: unsafe extern "C" fn(u32,u32,u32,u32,i32) = "glFramebufferTexture2D";
    framebuffer_status: unsafe extern "C" fn(u32)->u32 = "glCheckFramebufferStatus";
    create_shader: unsafe extern "C" fn(u32)->u32 = "glCreateShader";
    shader_source: unsafe extern "C" fn(u32,i32,*const *const c_char,*const i32) = "glShaderSource";
    compile_shader: unsafe extern "C" fn(u32) = "glCompileShader";
    shader_integer: unsafe extern "C" fn(u32,u32,*mut i32) = "glGetShaderiv";
    shader_log: unsafe extern "C" fn(u32,i32,*mut i32,*mut c_char) = "glGetShaderInfoLog";
    delete_shader: unsafe extern "C" fn(u32) = "glDeleteShader";
    create_program: unsafe extern "C" fn()->u32 = "glCreateProgram";
    attach_shader: unsafe extern "C" fn(u32,u32) = "glAttachShader";
    link_program: unsafe extern "C" fn(u32) = "glLinkProgram";
    program_integer: unsafe extern "C" fn(u32,u32,*mut i32) = "glGetProgramiv";
    program_log: unsafe extern "C" fn(u32,i32,*mut i32,*mut c_char) = "glGetProgramInfoLog";
    delete_program: unsafe extern "C" fn(u32) = "glDeleteProgram";
    use_program: unsafe extern "C" fn(u32) = "glUseProgram";
    uniform_location: unsafe extern "C" fn(u32,*const c_char)->i32 = "glGetUniformLocation";
    uniform_integer: unsafe extern "C" fn(i32,i32) = "glUniform1i";
    uniform_three: unsafe extern "C" fn(i32,f32,f32,f32) = "glUniform3f";
    bind_buffer: unsafe extern "C" fn(u32,u32) = "glBindBuffer";
    pixel_store: unsafe extern "C" fn(u32,i32) = "glPixelStorei";
    gen_vertex_arrays: unsafe extern "C" fn(i32,*mut u32) = "glGenVertexArrays";
    delete_vertex_arrays: unsafe extern "C" fn(i32,*const u32) = "glDeleteVertexArrays";
    bind_vertex_array: unsafe extern "C" fn(u32) = "glBindVertexArray";
    viewport: unsafe extern "C" fn(i32,i32,i32,i32) = "glViewport";
    draw: unsafe extern "C" fn(u32,i32,i32) = "glDrawArrays";
    read_pixels: unsafe extern "C" fn(i32,i32,i32,i32,u32,u32,*mut c_void) = "glReadPixels";
    flush: unsafe extern "C" fn() = "glFlush";
    finish: unsafe extern "C" fn() = "glFinish";
}
fn graphics() -> Result<&'static Graphics, String> {
    static API: OnceLock<Result<Graphics, String>> = OnceLock::new();
    API.get_or_init(Graphics::load)
        .as_ref()
        .map_err(Clone::clone)
}
fn monotonic_ns() -> u64 {
    let mut value = ffi::timespec {
        tv_sec: 0,
        tv_nsec: 0,
    };
    if unsafe { ffi::clock_gettime(1, &mut value) } != 0 {
        return 0;
    }
    (value.tv_sec as u64) * 1_000_000_000 + value.tv_nsec as u64
}

struct State<'a> {
    api: &'a Graphics,
    program: i32,
    draw_framebuffer: i32,
    read_framebuffer: i32,
    viewport: [i32; 4],
    active_texture: i32,
    textures: [i32; 2],
    external: [i32; 2],
    unpack_buffer: i32,
    vertex_array: i32,
    enabled: [bool; 4],
    color_mask: [i32; 4],
}
impl<'a> State<'a> {
    fn save(api: &'a Graphics) -> Self {
        let integer = |key| {
            let mut value = 0;
            unsafe { (api.get_integer)(key, &mut value) };
            value
        };
        let mut viewport = [0; 4];
        let mut color_mask = [0; 4];
        unsafe {
            (api.get_integer)(0x0BA2, viewport.as_mut_ptr());
            (api.get_integer)(0x0C23, color_mask.as_mut_ptr());
        }
        let active_texture = integer(0x84E0);
        let mut textures = [0; 2];
        let mut external = [0; 2];
        for index in 0..2 {
            unsafe { (api.active_texture)(0x84C0 + index as u32) };
            textures[index] = integer(0x8069);
            external[index] = integer(0x8D67);
        }
        unsafe { (api.active_texture)(0x84C0) };
        let unpack_buffer = integer(0x88EF);
        unsafe { (api.bind_buffer)(0x88EC, 0) };
        Self {
            api,
            program: integer(0x8B8D),
            draw_framebuffer: integer(0x8CA6),
            read_framebuffer: integer(0x8CAA),
            viewport,
            active_texture,
            textures,
            external,
            unpack_buffer,
            vertex_array: integer(0x85B5),
            enabled: [0x0C11, 0x0BE2, 0x0B71, 0x0B44]
                .map(|key| unsafe { (api.is_enabled)(key) != 0 }),
            color_mask,
        }
    }
}
impl Drop for State<'_> {
    fn drop(&mut self) {
        let api = self.api;
        unsafe {
            (api.use_program)(self.program as u32);
            (api.bind_framebuffer)(0x8CA9, self.draw_framebuffer as u32);
            (api.bind_framebuffer)(0x8CA8, self.read_framebuffer as u32);
            (api.viewport)(
                self.viewport[0],
                self.viewport[1],
                self.viewport[2],
                self.viewport[3],
            );
            (api.bind_vertex_array)(self.vertex_array as u32);
            for index in 0..2 {
                (api.active_texture)(0x84C0 + index as u32);
                (api.bind_texture)(0x0DE1, self.textures[index] as u32);
                (api.bind_texture)(0x8D65, self.external[index] as u32);
            }
            (api.bind_buffer)(0x88EC, self.unpack_buffer as u32);
            (api.active_texture)(self.active_texture as u32);
            for (key, enabled) in [0x0C11, 0x0BE2, 0x0B71, 0x0B44]
                .into_iter()
                .zip(self.enabled)
            {
                if enabled {
                    (api.enable)(key)
                } else {
                    (api.disable)(key)
                }
            }
            (api.color_mask)(
                self.color_mask[0] as u8,
                self.color_mask[1] as u8,
                self.color_mask[2] as u8,
                self.color_mask[3] as u8,
            );
        }
    }
}

struct ImportedPlane {
    image: Image,
    texture: u32,
}
struct Imported {
    planes: Vec<ImportedPlane>,
}

struct PlaneLayout {
    width: u32,
    height: u32,
    stride: u32,
    offset: u64,
    fourcc: u32,
}

fn import_plane(
    api: &Graphics,
    display: Display,
    descriptor: i32,
    layout: PlaneLayout,
) -> Result<ImportedPlane, String> {
    let PlaneLayout {
        width,
        height,
        stride,
        offset,
        fourcc,
    } = layout;
    // Capture pixels must survive EGL image creation before the GPU samples them.
    let attributes = [
        0x30D2,
        1,
        0x3057,
        width as i32,
        0x3056,
        height as i32,
        0x3271,
        fourcc as i32,
        0x3272,
        descriptor,
        0x3273,
        i32::try_from(offset).map_err(|_| "Invalid plane offset.")?,
        0x3274,
        stride as i32,
        0x3443,
        0,
        0x3444,
        0,
        0x3038,
    ];
    let image = unsafe {
        (api.create_image)(
            display,
            ptr::null_mut(),
            0x3270,
            ptr::null_mut(),
            attributes.as_ptr(),
        )
    };
    if image.is_null() {
        return Err("Unable to import a raw component plane into EGL.".into());
    }
    let mut texture = 0;
    unsafe {
        (api.gen_textures)(1, &mut texture);
        (api.bind_texture)(0x8D65, texture);
        (api.texture_parameter)(0x8D65, 0x2801, 0x2600);
        (api.texture_parameter)(0x8D65, 0x2800, 0x2600);
        (api.texture_parameter)(0x8D65, 0x2802, 0x812F);
        (api.texture_parameter)(0x8D65, 0x2803, 0x812F);
        (api.image_target)(0x8D65, image);
    }
    let failure = unsafe { (api.get_error)() };
    if failure != 0 {
        unsafe {
            (api.delete_textures)(1, &texture);
            (api.destroy_image)(display, image)
        };
        return Err(format!(
            "Bind a raw component plane: GL error {failure:#x}."
        ));
    }
    Ok(ImportedPlane { image, texture })
}

fn color_coefficients(encoding: u32) -> ([f32; 3], [f32; 3], [f32; 3]) {
    if encoding == 1 {
        (
            [1.0, 0.0, 1.402],
            [1.0, -0.344_136, -0.714_136],
            [1.0, 1.772, 0.0],
        )
    } else {
        (
            [1.0, 0.0, 1.5748],
            [1.0, -0.187_324, -0.468_124],
            [1.0, 1.8556, 0.0],
        )
    }
}

fn sample_normalization(format: DmaFormat, full_range: bool) -> ([f32; 3], [f32; 3]) {
    let (maximum, black, neutral, luma_span, chroma_span) = if format == DmaFormat::P010 {
        (1023.0, 64.0, 512.0, 876.0, 896.0)
    } else {
        (255.0, 16.0, 128.0, 219.0, 224.0)
    };
    if full_range {
        ([0.0, neutral / maximum, neutral / maximum], [1.0; 3])
    } else {
        (
            [black / maximum, neutral / maximum, neutral / maximum],
            [
                maximum / luma_span,
                maximum / chroma_span,
                maximum / chroma_span,
            ],
        )
    }
}

const VERTEX_SHADER: &CStr = c"#version 300 es
out vec2 uv;
void main() {
    vec2 positions[3] = vec2[3](vec2(-1., -1.), vec2(3., -1.), vec2(-1., 3.));
    vec2 position = positions[gl_VertexID];
    uv = position * .5 + .5;
    gl_Position = vec4(position, 0., 1.);
}";

// Raw R8 bytes avoid driver-dependent channel order, 16-bit import support, and YUV range hints.
const FRAGMENT_SHADER: &CStr = c"#version 300 es
#extension GL_OES_EGL_image_external_essl3 : require
precision highp float;
precision highp int;
uniform highp samplerExternalOES lumaTexture;
uniform highp samplerExternalOES chromaTexture;
uniform int inputFormat;
uniform vec3 scaling;
uniform vec3 sampleOffsets;
uniform vec3 imageDimensions;
uniform vec3 redRow;
uniform vec3 greenRow;
uniform vec3 blueRow;
in vec2 uv;
out vec4 color;
float sourceByte(float column) {
    return texture(lumaTexture, vec2((column + .5) / imageDimensions.z, uv.y)).r;
}
float chromaByte(float column) {
    return texture(chromaTexture, vec2((column + .5) / imageDimensions.z, uv.y)).r;
}
float word10(float low, float high) {
    return floor((floor(low * 255. + .5) + floor(high * 255. + .5) * 256.) / 64.) / 1023.;
}
void main() {
    float pixel = min(floor(uv.x * imageDimensions.x), imageDimensions.x - 1.);
    float pair = floor(pixel * .5);
    vec3 samples;
    if (inputFormat == 0) {
        samples = vec3(sourceByte(pixel), chromaByte(pair * 2.), chromaByte(pair * 2. + 1.));
    } else if (inputFormat == 1) {
        samples = vec3(word10(sourceByte(pixel * 2.), sourceByte(pixel * 2. + 1.)),
            word10(chromaByte(pair * 4.), chromaByte(pair * 4. + 1.)),
            word10(chromaByte(pair * 4. + 2.), chromaByte(pair * 4. + 3.)));
    } else if (inputFormat == 2) {
        samples = vec3(sourceByte(pixel * 2.), sourceByte(pair * 4. + 1.), sourceByte(pair * 4. + 3.));
    } else if (inputFormat == 3) {
        samples = vec3(sourceByte(pixel * 2. + 1.), sourceByte(pair * 4.), sourceByte(pair * 4. + 2.));
    } else {
        samples = vec3(sourceByte(pixel * 3.), sourceByte(pixel * 3. + 1.), sourceByte(pixel * 3. + 2.));
        if (inputFormat == 4) samples = samples.bgr;
        color = vec4((samples - vec3(sampleOffsets.x)) * scaling.x, 1.);
        return;
    }
    samples = (samples - sampleOffsets) * scaling;
    color = vec4(dot(redRow, samples), dot(greenRow, samples), dot(blueRow, samples), 1.);
}";

struct Renderer {
    api: &'static Graphics,
    display: Display,
    context: *mut c_void,
    needs_cache_writeback: bool,
    program: u32,
    vertex_array: u32,
    framebuffer: u32,
    texture: u32,
    width: u32,
    height: u32,
    generation: u64,
    sequence: u64,
    imports: HashMap<u32, Imported>,
    metrics: ffi::mgfr_gpu_metrics,
}
impl Renderer {
    fn new() -> Result<Self, String> {
        let api = graphics()?;
        let display = unsafe { (api.get_current_display)() };
        let context = unsafe { (api.get_current_context)() };
        if display.is_null() || context.is_null() {
            return Err("A current EGL context is required.".into());
        }
        let string = unsafe { (api.get_string)(0x1F02) };
        if string.is_null()
            || !unsafe { CStr::from_ptr(string.cast()) }
                .to_string_lossy()
                .starts_with("OpenGL ES 3")
        {
            let actual = if string.is_null() {
                "unknown".into()
            } else {
                unsafe { CStr::from_ptr(string.cast()) }.to_string_lossy()
            };
            return Err(format!(
                "DMA-BUF presentation requires GLES 3; current context is {actual}."
            ));
        }
        let gl_extensions = unsafe { (api.get_string)(0x1F03) };
        if gl_extensions.is_null()
            || !unsafe { CStr::from_ptr(gl_extensions.cast()) }
                .to_string_lossy()
                .split_whitespace()
                .any(|name| name == "GL_OES_EGL_image_external_essl3")
        {
            return Err(
                "External component sampling is unavailable in this graphics context.".into(),
            );
        }
        let extensions = unsafe { (api.query_string)(display, 0x3055) };
        if extensions.is_null()
            || !unsafe { CStr::from_ptr(extensions) }
                .to_string_lossy()
                .split_whitespace()
                .any(|name| name == "EGL_ANDROID_native_fence_sync")
        {
            return Err("Native GPU fence export is unavailable.".into());
        }
        let _state = State::save(api);
        let vendor = unsafe { (api.get_string)(0x1F00) };
        if vendor.is_null() {
            return Err("The graphics driver did not report its vendor.".into());
        }
        let needs_cache_writeback = unsafe { CStr::from_ptr(vendor.cast()) }
            .to_string_lossy()
            .contains("NVIDIA");
        let vertex = compile(api, 0x8B31, VERTEX_SHADER)?;
        let fragment = match compile(api, 0x8B30, FRAGMENT_SHADER) {
            Ok(shader) => shader,
            Err(error) => {
                unsafe { (api.delete_shader)(vertex) };
                return Err(error);
            }
        };
        let program = unsafe { (api.create_program)() };
        unsafe {
            (api.attach_shader)(program, vertex);
            (api.attach_shader)(program, fragment);
            (api.link_program)(program);
            (api.delete_shader)(vertex);
            (api.delete_shader)(fragment);
        }
        let mut linked = 0;
        unsafe { (api.program_integer)(program, 0x8B82, &mut linked) };
        if linked == 0 {
            let mut log = [0_i8; 2048];
            unsafe {
                (api.program_log)(program, 2048, ptr::null_mut(), log.as_mut_ptr());
                (api.delete_program)(program)
            };
            return Err(format!(
                "Link GPU conversion program: {}",
                unsafe { CStr::from_ptr(log.as_ptr()) }.to_string_lossy()
            ));
        }
        unsafe {
            (api.use_program)(program);
            (api.uniform_integer)((api.uniform_location)(program, c"lumaTexture".as_ptr()), 0);
            (api.uniform_integer)(
                (api.uniform_location)(program, c"chromaTexture".as_ptr()),
                1,
            )
        };
        let (mut vertex_array, mut framebuffer, mut texture) = (0, 0, 0);
        unsafe {
            (api.gen_vertex_arrays)(1, &mut vertex_array);
            (api.gen_framebuffers)(1, &mut framebuffer);
            (api.gen_textures)(1, &mut texture);
        }
        Ok(Self {
            api,
            display,
            context,
            needs_cache_writeback,
            program,
            vertex_array,
            framebuffer,
            texture,
            width: 0,
            height: 0,
            generation: 0,
            sequence: 0,
            imports: HashMap::new(),
            metrics: ffi::mgfr_gpu_metrics {
                sequence: 0,
                capture_timestamp_ns: 0,
                submit_timestamp_ns: 0,
                gpu_submit_cpu_ns: 0,
                width: 0,
                height: 0,
            },
        })
    }
    fn is_current(&self) -> bool {
        unsafe {
            (self.api.get_current_context)() == self.context
                && (self.api.get_current_display)() == self.display
        }
    }
    fn clear_imports(&mut self, delete_gl: bool) {
        for (_, imported) in self.imports.drain() {
            unsafe {
                for plane in imported.planes {
                    if delete_gl {
                        (self.api.delete_textures)(1, &plane.texture)
                    }
                    (self.api.destroy_image)(self.display, plane.image);
                }
            }
        }
    }
    fn render(&mut self, frame: Arc<dma::DmaFrame>) -> Result<u32, String> {
        if !self.is_current() {
            return Err("GPU renderer used with a different graphics context.".into());
        }
        if frame.generation == self.generation && frame.sequence == self.sequence {
            return Ok(self.texture);
        }
        let started = monotonic_ns();
        let source_format = DmaFormat::from_fourcc(frame.fourcc)?;
        let layout = source_format.layout(frame.width, frame.height, frame.stride)?;
        if frame.uv_offset != layout.uv_offset
            || frame.byte_length < layout.byte_length
            || frame.modifier != 0
        {
            return Err("Invalid DMA-BUF source layout for GPU conversion.".into());
        }
        let mut maximum_size = 0;
        unsafe { (self.api.get_integer)(0x0D33, &mut maximum_size) };
        if maximum_size <= 0
            || layout.row_bytes > maximum_size as u32
            || frame.height > maximum_size as u32
        {
            return Err("The raw component image exceeds the graphics texture size limit.".into());
        }
        if self.needs_cache_writeback {
            frame.ensure_gpu_visibility()?;
        }
        let api = self.api;
        let _state = State::save(api);
        if self.generation != frame.generation {
            self.clear_imports(true);
            self.generation = frame.generation;
        }
        if !self.imports.contains_key(&frame.index) {
            // Explicit component sampling preserves range selection on drivers that ignore YUV hints.
            let luma = import_plane(
                api,
                self.display,
                frame.descriptor(),
                PlaneLayout {
                    width: layout.row_bytes,
                    height: frame.height,
                    stride: frame.stride,
                    offset: 0,
                    fourcc: u32::from_le_bytes(*b"R8  "),
                },
            )?;
            let mut planes = vec![luma];
            if source_format.is_planar() {
                let chroma = match import_plane(
                    api,
                    self.display,
                    frame.descriptor(),
                    PlaneLayout {
                        width: layout.row_bytes,
                        height: frame.height / 2,
                        stride: frame.stride,
                        offset: frame.uv_offset,
                        fourcc: u32::from_le_bytes(*b"R8  "),
                    },
                ) {
                    Ok(plane) => plane,
                    Err(error) => {
                        unsafe {
                            (api.delete_textures)(1, &planes[0].texture);
                            (api.destroy_image)(self.display, planes[0].image)
                        };
                        return Err(error);
                    }
                };
                planes.push(chroma);
            }
            self.imports.insert(frame.index, Imported { planes });
        }
        unsafe {
            (api.bind_texture)(0x0DE1, self.texture);
            if self.width != frame.width || self.height != frame.height {
                (api.texture_image)(
                    0x0DE1,
                    0,
                    0x8058,
                    frame.width as i32,
                    frame.height as i32,
                    0,
                    0x1908,
                    0x1401,
                    ptr::null(),
                );
                (api.texture_parameter)(0x0DE1, 0x2801, 0x2601);
                (api.texture_parameter)(0x0DE1, 0x2800, 0x2601);
                (api.texture_parameter)(0x0DE1, 0x2802, 0x812F);
                (api.texture_parameter)(0x0DE1, 0x2803, 0x812F);
                self.width = frame.width;
                self.height = frame.height;
            }
            (api.bind_framebuffer)(0x8D40, self.framebuffer);
            (api.framebuffer_texture)(0x8D40, 0x8CE0, 0x0DE1, self.texture, 0);
        }
        if unsafe { (api.framebuffer_status)(0x8D40) } != 0x8CD5 {
            return Err("GPU conversion framebuffer is incomplete.".into());
        }
        unsafe {
            for key in [0x0C11, 0x0BE2, 0x0B71, 0x0B44] {
                (api.disable)(key)
            }
            (api.color_mask)(1, 1, 1, 1);
            (api.bind_vertex_array)(self.vertex_array);
            (api.use_program)(self.program);
            let imported = &self.imports[&frame.index];
            (api.active_texture)(0x84C0);
            (api.bind_texture)(0x8D65, imported.planes[0].texture);
            (api.active_texture)(0x84C1);
            // Both active samplers need a complete binding even when the packed branch ignores chroma.
            (api.bind_texture)(0x8D65, imported.planes.last().unwrap().texture);
            (api.uniform_integer)(
                (api.uniform_location)(self.program, c"inputFormat".as_ptr()),
                source_format as i32,
            );
            (api.uniform_three)(
                (api.uniform_location)(self.program, c"imageDimensions".as_ptr()),
                frame.width as f32,
                frame.height as f32,
                layout.row_bytes as f32,
            );
            let (red, green, blue) = color_coefficients(frame.encoding);
            for (name, row) in [(c"redRow", red), (c"greenRow", green), (c"blueRow", blue)] {
                (api.uniform_three)(
                    (api.uniform_location)(self.program, name.as_ptr()),
                    row[0],
                    row[1],
                    row[2],
                );
            }
            let (offsets, scales) = sample_normalization(source_format, frame.color_range == 2);
            (api.uniform_three)(
                (api.uniform_location)(self.program, c"sampleOffsets".as_ptr()),
                offsets[0],
                offsets[1],
                offsets[2],
            );
            (api.uniform_three)(
                (api.uniform_location)(self.program, c"scaling".as_ptr()),
                scales[0],
                scales[1],
                scales[2],
            );
            (api.viewport)(0, 0, frame.width as i32, frame.height as i32);
            (api.draw)(0x0004, 0, 3);
        }
        let error = unsafe { (api.get_error)() };
        if error != 0 {
            // Exceptional completion prevents an in-flight read from outliving its lease.
            unsafe { (api.finish)() };
            return Err(format!("Convert DMA-BUF image: GL error {error:#x}."));
        }
        let fence = match native_fence(api, self.display) {
            Ok(fence) => fence,
            Err(error) => {
                unsafe { (api.finish)() };
                return Err(error);
            }
        };
        if let Err(error) = dma::submit_gpu_read(&frame, fence) {
            unsafe { (api.finish)() };
            return Err(error);
        }
        let submitted = monotonic_ns();
        self.sequence = frame.sequence;
        self.metrics = ffi::mgfr_gpu_metrics {
            sequence: frame.sequence,
            capture_timestamp_ns: frame.timestamp_ns,
            submit_timestamp_ns: submitted,
            gpu_submit_cpu_ns: submitted.saturating_sub(started),
            width: frame.width,
            height: frame.height,
        };
        Ok(self.texture)
    }
    fn destroy(mut self, permit_gl: bool) {
        let current = permit_gl && self.is_current();
        self.clear_imports(current);
        if current {
            unsafe {
                (self.api.delete_textures)(1, &self.texture);
                (self.api.delete_framebuffers)(1, &self.framebuffer);
                (self.api.delete_vertex_arrays)(1, &self.vertex_array);
                (self.api.delete_program)(self.program);
            }
        }
    }
}
fn compile(api: &Graphics, kind: u32, source: &CStr) -> Result<u32, String> {
    let shader = unsafe { (api.create_shader)(kind) };
    let address = source.as_ptr();
    unsafe {
        (api.shader_source)(shader, 1, &address, ptr::null());
        (api.compile_shader)(shader)
    };
    let mut compiled = 0;
    unsafe { (api.shader_integer)(shader, 0x8B81, &mut compiled) };
    if compiled == 0 {
        let mut log = [0_i8; 2048];
        unsafe {
            (api.shader_log)(shader, 2048, ptr::null_mut(), log.as_mut_ptr());
            (api.delete_shader)(shader)
        };
        return Err(format!(
            "Compile GPU conversion shader: {}",
            unsafe { CStr::from_ptr(log.as_ptr()) }.to_string_lossy()
        ));
    }
    Ok(shader)
}
fn native_fence(api: &Graphics, display: Display) -> Result<OwnedFd, String> {
    let sync = unsafe { (api.create_sync)(display, 0x3144, [0x3038].as_ptr()) };
    if sync.is_null() {
        return Err("Unable to create a native GPU completion fence.".into());
    }
    unsafe { (api.flush)() };
    let descriptor = unsafe { (api.duplicate_fence)(display, sync) };
    unsafe { (api.destroy_sync)(display, sync) };
    if descriptor < 0 {
        return Err("Unable to export the GPU completion fence.".into());
    }
    let owned = unsafe { OwnedFd::from_raw_fd(descriptor) };
    if unsafe { fcntl(descriptor, 2, 1) } < 0 {
        return Err("Unable to protect the native fence descriptor across exec.".into());
    }
    Ok(owned)
}
fn set_error(output: *mut *mut c_char, error: String) {
    if !output.is_null() {
        unsafe { *output = make_c_string(error) }
    }
}

/// # Safety
/// The calling thread must have a live GLES 3 EGL context current.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_gpu_create(error: *mut *mut c_char) -> *mut c_void {
    match Renderer::new() {
        Ok(renderer) => Box::into_raw(Box::new(renderer)).cast(),
        Err(message) => {
            set_error(error, message);
            ptr::null_mut()
        }
    }
}
/// # Safety
/// The renderer and DMA token must be live, output pointers writable, and its EGL context current.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_gpu_render(
    renderer: *mut c_void,
    token: *mut c_void,
    texture: *mut u32,
    metrics: *mut ffi::mgfr_gpu_metrics,
    error: *mut *mut c_char,
) -> i32 {
    if renderer.is_null() || token.is_null() || texture.is_null() {
        set_error(error, "Invalid GPU renderer arguments.".into());
        return 0;
    }
    let renderer = unsafe { &mut *renderer.cast::<Renderer>() };
    match renderer.render(unsafe { dma::retain_frame(token) }) {
        Ok(value) => {
            unsafe {
                *texture = value;
                if !metrics.is_null() {
                    *metrics = renderer.metrics
                }
            };
            1
        }
        Err(message) => {
            set_error(error, message);
            0
        }
    }
}
/// # Safety
/// The renderer must be live and released once with its original context current.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_gpu_destroy(renderer: *mut c_void) {
    if !renderer.is_null() {
        unsafe { *Box::from_raw(renderer.cast::<Renderer>()) }.destroy(true)
    }
}
/// # Safety
/// The renderer must be live and released once after its context has been lost.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_gpu_abandon(renderer: *mut c_void) {
    if !renderer.is_null() {
        unsafe { *Box::from_raw(renderer.cast::<Renderer>()) }.destroy(false)
    }
}
/// # Safety
/// The renderer and token must be live, its context current, and output must hold width*height*4 bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_gpu_readback(
    renderer: *mut c_void,
    token: *mut c_void,
    output: *mut u8,
    length: usize,
    error: *mut *mut c_char,
) -> i32 {
    if renderer.is_null() || token.is_null() || output.is_null() {
        return 0;
    }
    let renderer = unsafe { &mut *renderer.cast::<Renderer>() };
    if let Err(message) = renderer.render(unsafe { dma::retain_frame(token) }) {
        set_error(error, message);
        return 0;
    }
    let required = (renderer.width as usize) * (renderer.height as usize) * 4;
    if length != required {
        set_error(error, "Invalid GPU readback capacity.".into());
        return 0;
    }
    let _state = State::save(renderer.api);
    let api = renderer.api;
    let mut settings = [0; 5];
    let keys = [0x0D05, 0x0D02, 0x0D04, 0x0D03, 0x88ED];
    for (key, setting) in keys.into_iter().zip(&mut settings) {
        unsafe { (api.get_integer)(key, setting) };
    }
    unsafe {
        (api.bind_buffer)(0x88EB, 0);
        for (key, value) in [(0x0D05, 1), (0x0D02, 0), (0x0D04, 0), (0x0D03, 0)] {
            (api.pixel_store)(key, value)
        }
        (api.bind_framebuffer)(0x8D40, renderer.framebuffer);
        (api.read_pixels)(
            0,
            0,
            renderer.width as i32,
            renderer.height as i32,
            0x1908,
            0x1401,
            output.cast(),
        );
        for (key, value) in keys[..4].iter().zip(&settings[..4]) {
            (api.pixel_store)(*key, *value)
        }
        (api.bind_buffer)(0x88EB, settings[4] as u32);
    }
    let failure = unsafe { (renderer.api.get_error)() };
    if failure != 0 {
        set_error(error, format!("GPU readback failed: {failure:#x}."));
        return 0;
    }
    1
}

struct TimingPending {
    fence: OwnedFd,
    context: u64,
    sequence: u64,
    capture: u64,
    submitted: u64,
}
struct TimingSample {
    context: u64,
    sequence: u64,
    age: u64,
    submitted_age: u64,
    observed: u64,
}
struct TimingState {
    pending: Mutex<Vec<TimingPending>>,
    samples: Mutex<VecDeque<TimingSample>>,
    start: std::sync::atomic::AtomicU64,
}
fn timing_state() -> Result<&'static Arc<TimingState>, String> {
    static TIMINGS: OnceLock<Result<Arc<TimingState>, String>> = OnceLock::new();
    TIMINGS
        .get_or_init(|| {
            let state = Arc::new(TimingState {
                pending: Mutex::new(Vec::new()),
                samples: Mutex::new(VecDeque::new()),
                start: std::sync::atomic::AtomicU64::new(0),
            });
            let worker = Arc::clone(&state);
            std::thread::Builder::new()
                .name("mygoframe-timing".into())
                .spawn(move || {
                    loop {
                        let mut completed = Vec::new();
                        {
                            let mut pending = worker.pending.lock().unwrap();
                            let mut index = 0;
                            while index < pending.len() {
                                let mut descriptor = ffi::pollfd {
                                    fd: pending[index].fence.as_raw_fd(),
                                    events: 1,
                                    revents: 0,
                                };
                                if unsafe { ffi::poll(&mut descriptor, 1, 0) } > 0 {
                                    let entry = pending.swap_remove(index);
                                    let observed = monotonic_ns();
                                    if descriptor.revents & 1 != 0 && observed >= entry.capture {
                                        completed.push(TimingSample {
                                            context: entry.context,
                                            sequence: entry.sequence,
                                            age: observed - entry.capture,
                                            submitted_age: entry
                                                .submitted
                                                .saturating_sub(entry.capture),
                                            observed,
                                        });
                                    }
                                } else {
                                    index += 1;
                                }
                            }
                        }
                        if !completed.is_empty() {
                            let mut samples = worker.samples.lock().unwrap();
                            for sample in completed {
                                if samples.len() >= 60_000 {
                                    samples.pop_front();
                                }
                                samples.push_back(sample);
                            }
                        }
                        std::thread::sleep(Duration::from_millis(1));
                    }
                })
                .map_err(|error| format!("Start GPU timing collector: {error}"))?;
            Ok(state)
        })
        .as_ref()
        .map_err(Clone::clone)
}
/// # Safety
/// The caller must have a live EGL context current and provide CLOCK_MONOTONIC capture timestamps.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mgfr_gpu_timing_mark(capture: u64, sequence: u64, context: u64) -> i32 {
    let result = (|| {
        let api = graphics()?;
        let display = unsafe { (api.get_current_display)() };
        if display.is_null() {
            return Err("No current EGL display.".into());
        }
        let state = timing_state()?;
        let fence = native_fence(api, display)?;
        let submitted = monotonic_ns();
        let _ = state.start.compare_exchange(
            0,
            submitted,
            std::sync::atomic::Ordering::Relaxed,
            std::sync::atomic::Ordering::Relaxed,
        );
        let mut pending = state.pending.lock().unwrap();
        if pending.len() >= 512 {
            return Err("GPU timing queue reached its capacity.".into());
        }
        pending.push(TimingPending {
            fence,
            context,
            sequence,
            capture,
            submitted,
        });
        Ok::<_, String>(())
    })();
    i32::from(result.is_ok())
}
fn percentile(values: &mut [u64], fraction: f64) -> u64 {
    if values.is_empty() {
        return 0;
    }
    values.sort_unstable();
    values[((values.len() - 1) as f64 * fraction).ceil() as usize]
}
#[unsafe(no_mangle)]
pub extern "C" fn mgfr_gpu_timings_json() -> *mut c_char {
    let Ok(state) = timing_state() else {
        return make_c_string("{}".into());
    };
    let start = state.start.load(std::sync::atomic::Ordering::Relaxed);
    let samples = state.samples.lock().unwrap();
    let mut contexts: HashMap<u64, Vec<&TimingSample>> = HashMap::new();
    for sample in samples
        .iter()
        .filter(|sample| sample.observed >= start + 2_000_000_000)
    {
        contexts.entry(sample.context).or_default().push(sample);
    }
    let mut identifiers: Vec<_> = contexts.keys().copied().collect();
    identifiers.sort_unstable();
    let mut entries = Vec::new();
    for context in identifiers {
        let values = &contexts[&context];
        let mut ages: Vec<_> = values.iter().map(|sample| sample.age).collect();
        let mut submitted: Vec<_> = values.iter().map(|sample| sample.submitted_age).collect();
        let first = values.iter().min_by_key(|sample| sample.observed).unwrap();
        let last = values.iter().max_by_key(|sample| sample.observed).unwrap();
        let elapsed = last.observed.saturating_sub(first.observed);
        let fps = if elapsed > 0 {
            (values.len().saturating_sub(1) as f64) * 1e9 / (elapsed as f64)
        } else {
            0.0
        };
        entries.push(format!("{{\"context\":{context},\"samples\":{},\"firstSequence\":{},\"lastSequence\":{},\"observedFps\":{fps:.3},\"captureToTextureReadyP50Ns\":{},\"captureToTextureReadyP95Ns\":{},\"captureToSubmitP50Ns\":{},\"captureToSubmitP95Ns\":{}}}",
            values.len(),first.sequence,last.sequence,percentile(&mut ages,0.5),percentile(&mut ages,0.95),percentile(&mut submitted,0.5),percentile(&mut submitted,0.95)));
    }
    make_c_string(format!(
        "{{\"warmupNs\":2000000000,\"pollIntervalNs\":1000000,\"contexts\":[{}]}}",
        entries.join(",")
    ))
}

#[cfg(test)]
mod tests {
    #[test]
    fn percentile_handles_empty_and_small_samples() {
        assert_eq!(super::percentile(&mut [], 0.95), 0);
        assert_eq!(super::percentile(&mut [10, 1, 5], 0.5), 5);
        assert_eq!(super::percentile(&mut [10, 1, 5], 0.95), 10);
    }
}
