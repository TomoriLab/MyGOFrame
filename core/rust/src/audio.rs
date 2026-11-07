use crate::{ffi, make_c_string};
use std::cell::UnsafeCell;
use std::ffi::{CStr, CString, c_char, c_void};
use std::mem::{align_of, size_of};
use std::ptr;
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, AtomicI32, AtomicU32, AtomicU64, Ordering};

const CHANNELS: usize = 2;
const CAPACITY: usize = 4096;
const SAMPLE_STRIDE: u32 = (size_of::<f32>() * CHANNELS) as u32;
const REQUESTED_LATENCY: u32 = 128;
const SAMPLE_RATE: u32 = 48_000;

struct PipeWireRuntime {
    users: u32,
    dbus_handle: usize,
}

static PIPEWIRE_RUNTIME: Mutex<PipeWireRuntime> = Mutex::new(PipeWireRuntime {
    users: 0,
    dbus_handle: 0,
});

unsafe extern "C" {
    fn dlopen(name: *const c_char, flags: i32) -> *mut c_void;
    fn dlclose(handle: *mut c_void) -> i32;
    fn pw_core_add_listener(
        core: *mut ffi::pw_core,
        listener: *mut ffi::spa_hook,
        events: *const ffi::pw_core_events,
        data: *mut c_void,
    ) -> i32;
    fn pw_core_get_registry(
        core: *mut ffi::pw_core,
        version: u32,
        user_data_size: usize,
    ) -> *mut ffi::pw_registry;
    fn pw_core_sync(core: *mut ffi::pw_core, identifier: u32, sequence: i32) -> i32;
    fn pw_registry_add_listener(
        registry: *mut ffi::pw_registry,
        listener: *mut ffi::spa_hook,
        events: *const ffi::pw_registry_events,
        data: *mut c_void,
    ) -> i32;
}

struct PipeWireGuard;

impl PipeWireGuard {
    fn new() -> Self {
        let mut runtime = PIPEWIRE_RUNTIME
            .lock()
            .unwrap_or_else(|error| error.into_inner());
        if runtime.users == 0 {
            // RTLD_NODELETE keeps D-Bus global caches resident across PipeWire contexts.
            runtime.dbus_handle =
                unsafe { dlopen(c"libdbus-1.so.3".as_ptr(), 1 | 0x1000) } as usize;
            unsafe { ffi::pw_init(ptr::null_mut(), ptr::null_mut()) };
        }
        runtime.users += 1;
        Self
    }
}

impl Drop for PipeWireGuard {
    fn drop(&mut self) {
        let mut runtime = PIPEWIRE_RUNTIME
            .lock()
            .unwrap_or_else(|error| error.into_inner());
        runtime.users -= 1;
        if runtime.users == 0 {
            unsafe { ffi::pw_deinit() };
            if runtime.dbus_handle != 0 {
                let _ = unsafe { dlclose(runtime.dbus_handle as *mut c_void) };
                runtime.dbus_handle = 0;
            }
        }
    }
}

struct AudioRing {
    // Only one capture callback and one playback callback may touch the corresponding samples.
    samples: Box<[UnsafeCell<f32>; CAPACITY * CHANNELS]>,
    capture_active: AtomicBool,
    playback_active: AtomicBool,
    write_position: AtomicU64,
    read_position: AtomicU64,
    dropped_frames: AtomicU64,
    underrun_frames: AtomicU64,
    captured_frames: AtomicU64,
    played_frames: AtomicU64,
    overflowed: AtomicBool,
    capture_quantum: AtomicU32,
    volume_bits: AtomicU32,
}

// Release/acquire positions transfer sample slots between the two callback threads.
unsafe impl Sync for AudioRing {}

impl AudioRing {
    fn new() -> Self {
        Self {
            samples: Box::new(std::array::from_fn(|_| UnsafeCell::new(0.0))),
            capture_active: AtomicBool::new(false),
            playback_active: AtomicBool::new(false),
            write_position: AtomicU64::new(0),
            read_position: AtomicU64::new(0),
            dropped_frames: AtomicU64::new(0),
            underrun_frames: AtomicU64::new(0),
            captured_frames: AtomicU64::new(0),
            played_frames: AtomicU64::new(0),
            overflowed: AtomicBool::new(false),
            capture_quantum: AtomicU32::new(0),
            volume_bits: AtomicU32::new(1.0_f32.to_bits()),
        }
    }

    fn set_volume(&self, value: f32) {
        let gain = if value.is_finite() {
            value.clamp(0.0, 1.0)
        } else {
            0.0
        };
        self.volume_bits.store(gain.to_bits(), Ordering::Relaxed);
    }

    fn capture(&self, input: Option<&[f32]>, frames: usize) {
        if self
            .capture_active
            .compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed)
            .is_err()
        {
            self.dropped_frames
                .fetch_add(frames as u64, Ordering::Relaxed);
            return;
        }
        let frames = frames.min(u32::MAX as usize);
        self.capture_quantum.store(frames as u32, Ordering::Relaxed);
        let write = self.write_position.load(Ordering::Relaxed);
        let read = self.read_position.load(Ordering::Acquire);
        let available = (CAPACITY as u64).saturating_sub(write.wrapping_sub(read));
        let accepted = (frames as u64).min(available) as usize;
        for index in 0..accepted {
            let offset = (write.wrapping_add(index as u64) as usize % CAPACITY) * CHANNELS;
            unsafe {
                self.samples[offset].get().write(
                    input
                        .and_then(|data| data.get(index * CHANNELS))
                        .copied()
                        .unwrap_or(0.0),
                );
                self.samples[offset + 1].get().write(
                    input
                        .and_then(|data| data.get(index * CHANNELS + 1))
                        .copied()
                        .unwrap_or(0.0),
                );
            }
        }
        self.write_position
            .store(write.wrapping_add(accepted as u64), Ordering::Release);
        self.captured_frames
            .fetch_add(frames as u64, Ordering::Relaxed);
        if accepted != frames {
            self.dropped_frames
                .fetch_add((frames - accepted) as u64, Ordering::Relaxed);
            self.overflowed.store(true, Ordering::Release);
        }
        self.capture_active.store(false, Ordering::Release);
    }

    fn playback(&self, output: &mut [f32]) {
        if self
            .playback_active
            .compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed)
            .is_err()
        {
            output.fill(0.0);
            return;
        }
        let frames = output.len() / CHANNELS;
        let mut read = self.read_position.load(Ordering::Relaxed);
        let write = self.write_position.load(Ordering::Acquire);
        let mut available = write.wrapping_sub(read).min(CAPACITY as u64);
        let quantum = (frames as u64).max(self.capture_quantum.load(Ordering::Relaxed) as u64);
        if self.overflowed.swap(false, Ordering::AcqRel) {
            self.dropped_frames.fetch_add(available, Ordering::Relaxed);
            read = write;
            available = 0;
        } else if available > quantum * 2 {
            let skipped = available - quantum;
            read = read.wrapping_add(skipped);
            available -= skipped;
            self.dropped_frames.fetch_add(skipped, Ordering::Relaxed);
        }
        let provided = (frames as u64).min(available) as usize;
        let gain = f32::from_bits(self.volume_bits.load(Ordering::Relaxed));
        for index in 0..provided {
            let offset = (read.wrapping_add(index as u64) as usize % CAPACITY) * CHANNELS;
            unsafe {
                output[index * CHANNELS] = self.samples[offset].get().read() * gain;
                output[index * CHANNELS + 1] = self.samples[offset + 1].get().read() * gain;
            }
        }
        output[provided * CHANNELS..].fill(0.0);
        self.read_position
            .store(read.wrapping_add(provided as u64), Ordering::Release);
        self.underrun_frames
            .fetch_add((frames - provided) as u64, Ordering::Relaxed);
        self.played_frames
            .fetch_add(provided as u64, Ordering::Relaxed);
        self.playback_active.store(false, Ordering::Release);
    }
}

#[repr(C)]
struct ScalarProperty {
    property: ffi::spa_pod_prop,
    value: u32,
    padding: u32,
}

impl ScalarProperty {
    fn new(key: u32, type_: u32, value: u32) -> Self {
        Self {
            property: ffi::spa_pod_prop {
                key,
                flags: 0,
                value: ffi::spa_pod {
                    size: size_of::<u32>() as u32,
                    type_,
                },
            },
            value,
            padding: 0,
        }
    }
}

#[repr(C)]
struct PositionProperty {
    property: ffi::spa_pod_prop,
    array_body: ffi::spa_pod_array_body,
    positions: [u32; CHANNELS],
}

#[repr(C)]
struct AudioFormatPod {
    object: ffi::spa_pod_object,
    media_type: ScalarProperty,
    media_subtype: ScalarProperty,
    format: ScalarProperty,
    rate: ScalarProperty,
    channels: ScalarProperty,
    positions: PositionProperty,
}

impl AudioFormatPod {
    fn new() -> Self {
        // SPA POD scalar values occupy an eight-byte aligned property slot.
        debug_assert_eq!(size_of::<ScalarProperty>(), 24);
        debug_assert_eq!(size_of::<PositionProperty>(), 32);
        Self {
            object: ffi::spa_pod_object {
                pod: ffi::spa_pod {
                    size: (size_of::<Self>() - size_of::<ffi::spa_pod>()) as u32,
                    type_: ffi::SPA_TYPE_Object,
                },
                body: ffi::spa_pod_object_body {
                    type_: ffi::SPA_TYPE_OBJECT_Format,
                    id: ffi::spa_param_type_SPA_PARAM_EnumFormat,
                },
            },
            media_type: ScalarProperty::new(
                ffi::spa_format_SPA_FORMAT_mediaType,
                ffi::SPA_TYPE_Id,
                ffi::spa_media_type_SPA_MEDIA_TYPE_audio,
            ),
            media_subtype: ScalarProperty::new(
                ffi::spa_format_SPA_FORMAT_mediaSubtype,
                ffi::SPA_TYPE_Id,
                ffi::spa_media_subtype_SPA_MEDIA_SUBTYPE_raw,
            ),
            format: ScalarProperty::new(
                ffi::spa_format_SPA_FORMAT_AUDIO_format,
                ffi::SPA_TYPE_Id,
                ffi::spa_audio_format_SPA_AUDIO_FORMAT_F32,
            ),
            rate: ScalarProperty::new(
                ffi::spa_format_SPA_FORMAT_AUDIO_rate,
                ffi::SPA_TYPE_Int,
                SAMPLE_RATE,
            ),
            channels: ScalarProperty::new(
                ffi::spa_format_SPA_FORMAT_AUDIO_channels,
                ffi::SPA_TYPE_Int,
                CHANNELS as u32,
            ),
            positions: PositionProperty {
                property: ffi::spa_pod_prop {
                    key: ffi::spa_format_SPA_FORMAT_AUDIO_position,
                    flags: 0,
                    value: ffi::spa_pod {
                        size: 16,
                        type_: ffi::SPA_TYPE_Array,
                    },
                },
                array_body: ffi::spa_pod_array_body {
                    child: ffi::spa_pod {
                        size: size_of::<u32>() as u32,
                        type_: ffi::SPA_TYPE_Id,
                    },
                },
                positions: [
                    ffi::spa_audio_channel_SPA_AUDIO_CHANNEL_FL,
                    ffi::spa_audio_channel_SPA_AUDIO_CHANNEL_FR,
                ],
            },
        }
    }
}

struct AudioState {
    ring: AudioRing,
    capture_state: AtomicI32,
    playback_state: AtomicI32,
    error: UnsafeCell<[c_char; 512]>,
    capture_listener: ffi::spa_hook,
    playback_listener: ffi::spa_hook,
    capture_events: ffi::pw_stream_events,
    playback_events: ffi::pw_stream_events,
    loop_: *mut ffi::pw_thread_loop,
    context: *mut ffi::pw_context,
    core: *mut ffi::pw_core,
    capture_stream: *mut ffi::pw_stream,
    playback_stream: *mut ffi::pw_stream,
    loop_started: bool,
}

// The PipeWire thread-loop lock protects error text while atomics protect realtime counters.
unsafe impl Sync for AudioState {}

impl AudioState {
    fn new() -> Self {
        let mut capture_events: ffi::pw_stream_events = unsafe { std::mem::zeroed() };
        capture_events.version = ffi::PW_VERSION_STREAM_EVENTS;
        capture_events.state_changed = Some(capture_state_changed);
        capture_events.process = Some(capture_process);
        let mut playback_events: ffi::pw_stream_events = unsafe { std::mem::zeroed() };
        playback_events.version = ffi::PW_VERSION_STREAM_EVENTS;
        playback_events.state_changed = Some(playback_state_changed);
        playback_events.process = Some(playback_process);
        Self {
            ring: AudioRing::new(),
            capture_state: AtomicI32::new(0),
            playback_state: AtomicI32::new(0),
            error: UnsafeCell::new([0; 512]),
            capture_listener: unsafe { std::mem::zeroed() },
            playback_listener: unsafe { std::mem::zeroed() },
            capture_events,
            playback_events,
            loop_: ptr::null_mut(),
            context: ptr::null_mut(),
            core: ptr::null_mut(),
            capture_stream: ptr::null_mut(),
            playback_stream: ptr::null_mut(),
            loop_started: false,
        }
    }

    unsafe fn record_error(&self, message: *const c_char) {
        if message.is_null() {
            return;
        }
        let target = unsafe { &mut *self.error.get() };
        let mut index = 0;
        while index < target.len() - 1 {
            let value = unsafe { *message.add(index) };
            if value == 0 {
                break;
            }
            target[index] = value;
            index += 1;
        }
        target[index] = 0;
    }

    fn error_string(&self) -> String {
        unsafe { CStr::from_ptr((*self.error.get()).as_ptr()) }
            .to_string_lossy()
            .into_owned()
    }

    fn set_error(&self, message: &CStr) {
        unsafe { self.record_error(message.as_ptr()) }
    }
}

struct AudioSession {
    state: Box<AudioState>,
    _pipewire_guard: PipeWireGuard,
}

impl Drop for AudioSession {
    fn drop(&mut self) {
        unsafe {
            let state = self.state.as_ref();
            if state.loop_started {
                ffi::pw_thread_loop_stop(state.loop_);
            }
            if !state.capture_stream.is_null() {
                ffi::pw_stream_destroy(state.capture_stream);
            }
            if !state.playback_stream.is_null() {
                ffi::pw_stream_destroy(state.playback_stream);
            }
            if !state.core.is_null() {
                ffi::pw_core_disconnect(state.core);
            }
            if !state.context.is_null() {
                ffi::pw_context_destroy(state.context);
            }
            if !state.loop_.is_null() {
                ffi::pw_thread_loop_destroy(state.loop_);
            }
        }
    }
}

unsafe extern "C" fn capture_process(userdata: *mut c_void) {
    if userdata.is_null() {
        return;
    }
    let state = unsafe { &*userdata.cast::<AudioState>() };
    let stream = state.capture_stream;
    if stream.is_null() {
        return;
    }
    let buffer = unsafe { ffi::pw_stream_dequeue_buffer(stream) };
    if buffer.is_null() {
        return;
    }
    unsafe {
        let payload = (*buffer).buffer;
        if !payload.is_null() && (*payload).n_datas > 0 && !(*payload).datas.is_null() {
            let data = &*(*payload).datas;
            if !data.data.is_null() && !data.chunk.is_null() {
                let chunk = &*data.chunk;
                if chunk.offset <= data.maxsize
                    && chunk.flags & ffi::SPA_CHUNK_FLAG_CORRUPTED as i32 == 0
                    && (chunk.stride == 0 || chunk.stride == SAMPLE_STRIDE as i32)
                {
                    let length = chunk.size.min(data.maxsize - chunk.offset);
                    let frames = (length / SAMPLE_STRIDE) as usize;
                    let address = data.data.cast::<u8>().add(chunk.offset as usize);
                    if address.addr() % align_of::<f32>() == 0 {
                        let samples = if chunk.flags & ffi::SPA_CHUNK_FLAG_EMPTY as i32 == 0 {
                            Some(std::slice::from_raw_parts(
                                address.cast::<f32>(),
                                frames * CHANNELS,
                            ))
                        } else {
                            None
                        };
                        state.ring.capture(samples, frames);
                    }
                }
            }
        }
        let _ = ffi::pw_stream_queue_buffer(stream, buffer);
    }
}

unsafe extern "C" fn playback_process(userdata: *mut c_void) {
    if userdata.is_null() {
        return;
    }
    let state = unsafe { &*userdata.cast::<AudioState>() };
    let stream = state.playback_stream;
    if stream.is_null() {
        return;
    }
    let buffer = unsafe { ffi::pw_stream_dequeue_buffer(stream) };
    if buffer.is_null() {
        return;
    }
    unsafe {
        let payload = (*buffer).buffer;
        if !payload.is_null() && (*payload).n_datas > 0 && !(*payload).datas.is_null() {
            let data = &mut *(*payload).datas;
            if !data.data.is_null()
                && !data.chunk.is_null()
                && data.data.addr() % align_of::<f32>() == 0
            {
                let mut frames = data.maxsize / SAMPLE_STRIDE;
                if (*buffer).requested > 0 && (*buffer).requested < frames as u64 {
                    frames = (*buffer).requested as u32;
                }
                let samples = std::slice::from_raw_parts_mut(
                    data.data.cast::<f32>(),
                    frames as usize * CHANNELS,
                );
                state.ring.playback(samples);
                let chunk = &mut *data.chunk;
                chunk.offset = 0;
                chunk.stride = SAMPLE_STRIDE as i32;
                chunk.size = frames * SAMPLE_STRIDE;
                (*buffer).size = frames as u64;
            }
        }
        let _ = ffi::pw_stream_queue_buffer(stream, buffer);
    }
}

unsafe extern "C" fn capture_state_changed(
    userdata: *mut c_void,
    previous: ffi::pw_stream_state,
    current: ffi::pw_stream_state,
    error: *const c_char,
) {
    if userdata.is_null() {
        return;
    }
    let state = unsafe { &*userdata.cast::<AudioState>() };
    state.capture_state.store(current, Ordering::Relaxed);
    if current == ffi::pw_stream_state_PW_STREAM_STATE_ERROR {
        unsafe { state.record_error(error) };
    } else if current == ffi::pw_stream_state_PW_STREAM_STATE_UNCONNECTED
        && previous > ffi::pw_stream_state_PW_STREAM_STATE_CONNECTING
    {
        state.set_error(c"Audio capture device disconnected.");
    }
    unsafe { ffi::pw_thread_loop_signal(state.loop_, false) };
}

unsafe extern "C" fn playback_state_changed(
    userdata: *mut c_void,
    previous: ffi::pw_stream_state,
    current: ffi::pw_stream_state,
    error: *const c_char,
) {
    if userdata.is_null() {
        return;
    }
    let state = unsafe { &*userdata.cast::<AudioState>() };
    state.playback_state.store(current, Ordering::Relaxed);
    if current == ffi::pw_stream_state_PW_STREAM_STATE_ERROR {
        unsafe { state.record_error(error) };
    } else if current == ffi::pw_stream_state_PW_STREAM_STATE_UNCONNECTED
        && previous > ffi::pw_stream_state_PW_STREAM_STATE_CONNECTING
    {
        state.set_error(c"Audio output device disconnected.");
    }
    unsafe { ffi::pw_thread_loop_signal(state.loop_, false) };
}

fn audio_properties(target: &str, capture: bool) -> Result<*mut ffi::pw_properties, String> {
    let dictionary = ffi::spa_dict {
        flags: 0,
        n_items: 0,
        items: ptr::null(),
    };
    let properties = unsafe { ffi::pw_properties_new_dict(&dictionary) };
    if properties.is_null() {
        return Err("Cannot allocate PipeWire stream properties.".into());
    }
    let values = [
        ("media.type", "Audio"),
        (
            "media.category",
            if capture { "Capture" } else { "Playback" },
        ),
        ("media.role", "Game"),
        ("application.name", "MyGOFrame"),
        (
            "node.name",
            if capture {
                "mygoframe.capture"
            } else {
                "mygoframe.playback"
            },
        ),
        (
            "node.description",
            if capture {
                "MyGOFrame capture audio"
            } else {
                "MyGOFrame audio monitoring"
            },
        ),
        ("node.latency", "128/48000"),
        ("node.rate", "1/48000"),
        ("stream.dont-remix", "false"),
        ("node.dont-reconnect", "true"),
        ("node.dont-fallback", "true"),
        ("target.object", target),
    ];
    for (key, value) in values {
        if key == "target.object" && value.is_empty() {
            continue;
        }
        let key = CString::new(key).expect("static property key has no NUL");
        let value = match CString::new(value) {
            Ok(value) => value,
            Err(_) => {
                unsafe { ffi::pw_properties_free(properties) };
                return Err("PipeWire target contains a NUL byte.".into());
            }
        };
        if unsafe { ffi::pw_properties_set(properties, key.as_ptr(), value.as_ptr()) } < 0 {
            unsafe { ffi::pw_properties_free(properties) };
            return Err("Cannot configure PipeWire stream properties.".into());
        }
    }
    Ok(properties)
}

fn create_session(source: &str, output: &str, volume: f32) -> Result<Box<AudioSession>, String> {
    if source.is_empty() {
        return Err("Select an audio input device.".into());
    }
    let pipewire_guard = PipeWireGuard::new();
    let mut session = Box::new(AudioSession {
        state: Box::new(AudioState::new()),
        _pipewire_guard: pipewire_guard,
    });
    session.state.ring.set_volume(volume);
    unsafe {
        let state = session.state.as_mut() as *mut AudioState;
        (*state).loop_ = ffi::pw_thread_loop_new(c"mygoframe-audio".as_ptr(), ptr::null());
        if (*state).loop_.is_null() {
            return Err("Cannot initialize the PipeWire audio loop.".into());
        }
        (*state).context = ffi::pw_context_new(
            ffi::pw_thread_loop_get_loop((*state).loop_),
            ptr::null_mut(),
            0,
        );
        if (*state).context.is_null() {
            return Err("Cannot initialize the PipeWire audio context.".into());
        }
        (*state).core = ffi::pw_context_connect((*state).context, ptr::null_mut(), 0);
        if (*state).core.is_null() {
            return Err("Cannot connect to PipeWire.".into());
        }
        let capture_properties = audio_properties(source, true)?;
        (*state).capture_stream = ffi::pw_stream_new(
            (*state).core,
            c"MyGOFrame capture".as_ptr(),
            capture_properties,
        );
        if (*state).capture_stream.is_null() {
            return Err("Cannot initialize PipeWire capture stream.".into());
        }
        let playback_properties = audio_properties(output, false)?;
        (*state).playback_stream = ffi::pw_stream_new(
            (*state).core,
            c"MyGOFrame playback".as_ptr(),
            playback_properties,
        );
        if (*state).playback_stream.is_null() {
            return Err("Cannot initialize PipeWire playback stream.".into());
        }
        let userdata = state.cast::<c_void>();
        ffi::pw_stream_add_listener(
            (*state).capture_stream,
            &mut (*state).capture_listener,
            &(*state).capture_events,
            userdata,
        );
        ffi::pw_stream_add_listener(
            (*state).playback_stream,
            &mut (*state).playback_listener,
            &(*state).playback_events,
            userdata,
        );
        let format = AudioFormatPod::new();
        let mut parameter = &format.object.pod as *const ffi::spa_pod;
        let flags = ffi::pw_stream_flags_PW_STREAM_FLAG_AUTOCONNECT
            | ffi::pw_stream_flags_PW_STREAM_FLAG_MAP_BUFFERS
            | ffi::pw_stream_flags_PW_STREAM_FLAG_RT_PROCESS;
        if ffi::pw_stream_connect(
            (*state).capture_stream,
            ffi::spa_direction_SPA_DIRECTION_INPUT,
            u32::MAX,
            flags,
            &mut parameter,
            1,
        ) < 0
            || ffi::pw_stream_connect(
                (*state).playback_stream,
                ffi::spa_direction_SPA_DIRECTION_OUTPUT,
                u32::MAX,
                flags,
                &mut parameter,
                1,
            ) < 0
        {
            return Err("Cannot connect the selected PipeWire streams.".into());
        }
        if ffi::pw_thread_loop_start((*state).loop_) < 0 {
            return Err("Cannot start the PipeWire audio loop.".into());
        }
        (*state).loop_started = true;
        ffi::pw_thread_loop_lock((*state).loop_);
        let mut deadline: ffi::timespec = std::mem::zeroed();
        let _ = ffi::pw_thread_loop_get_time((*state).loop_, &mut deadline, 3_000_000_000);
        while (*state).capture_state.load(Ordering::Relaxed)
            != ffi::pw_stream_state_PW_STREAM_STATE_STREAMING
            || (*state).playback_state.load(Ordering::Relaxed)
                != ffi::pw_stream_state_PW_STREAM_STATE_STREAMING
        {
            if !(*state).error_string().is_empty() {
                break;
            }
            if ffi::pw_thread_loop_timed_wait_full((*state).loop_, &deadline) < 0 {
                (*state).set_error(c"Audio streams did not start. Check the selected input, output and PipeWire routing.");
                break;
            }
        }
        let error = (*state).error_string();
        ffi::pw_thread_loop_unlock((*state).loop_);
        if !error.is_empty() {
            return Err(error);
        }
    }
    Ok(session)
}

#[unsafe(no_mangle)]
/// # Safety
/// `source` and `output` must be null or point to valid NUL-terminated strings.
/// `error` must be null or point to a writable pointer slot.
pub unsafe extern "C" fn mgfr_audio_create(
    source: *const c_char,
    output: *const c_char,
    volume: f32,
    error: *mut *mut c_char,
) -> *mut c_void {
    if !error.is_null() {
        unsafe { *error = ptr::null_mut() };
    }
    if source.is_null() {
        if !error.is_null() {
            unsafe { *error = make_c_string("An audio source is required.".into()) };
        }
        return ptr::null_mut();
    }
    let source = unsafe { CStr::from_ptr(source) }.to_string_lossy();
    let output = if output.is_null() {
        String::new()
    } else {
        unsafe { CStr::from_ptr(output) }
            .to_string_lossy()
            .into_owned()
    };
    match create_session(&source, &output, volume) {
        Ok(session) => Box::into_raw(session).cast::<c_void>(),
        Err(message) => {
            if !error.is_null() {
                unsafe { *error = make_c_string(message) };
            }
            ptr::null_mut()
        }
    }
}

#[unsafe(no_mangle)]
/// # Safety
/// `audio` must be null or an active handle returned by `mgfr_audio_create`.
/// The handle must be destroyed once and have no concurrent users.
pub unsafe extern "C" fn mgfr_audio_destroy(audio: *mut c_void) {
    if !audio.is_null() {
        unsafe { drop(Box::from_raw(audio.cast::<AudioSession>())) };
    }
}

#[unsafe(no_mangle)]
/// # Safety
/// `audio` must be null or an active handle returned by `mgfr_audio_create`.
/// Destruction must not run concurrently with this call.
pub unsafe extern "C" fn mgfr_audio_set_volume(audio: *mut c_void, volume: f32) {
    if !audio.is_null() {
        unsafe { &*audio.cast::<AudioSession>() }
            .state
            .ring
            .set_volume(volume);
    }
}

#[unsafe(no_mangle)]
/// # Safety
/// `audio` must be an active handle returned by `mgfr_audio_create`.
/// `output` must point to a writable status structure, and destruction must not run concurrently.
pub unsafe extern "C" fn mgfr_audio_status(
    audio: *mut c_void,
    output: *mut ffi::mgfr_audio_status_data,
) -> i32 {
    if audio.is_null() || output.is_null() {
        return -1;
    }
    let session = unsafe { &*audio.cast::<AudioSession>() };
    let state = session.state.as_ref();
    let read = state.ring.read_position.load(Ordering::Acquire);
    let write = state.ring.write_position.load(Ordering::Acquire);
    let capture_state = state.capture_state.load(Ordering::Relaxed);
    let playback_state = state.playback_state.load(Ordering::Relaxed);
    unsafe {
        ffi::pw_thread_loop_lock(state.loop_);
        let error = *state.error.get();
        ffi::pw_thread_loop_unlock(state.loop_);
        *output = ffi::mgfr_audio_status_data {
            running: i32::from(
                capture_state == ffi::pw_stream_state_PW_STREAM_STATE_STREAMING
                    && playback_state == ffi::pw_stream_state_PW_STREAM_STATE_STREAMING
                    && error[0] == 0,
            ),
            capture_state,
            playback_state,
            queued_frames: write.wrapping_sub(read).min(CAPACITY as u64),
            dropped_frames: state.ring.dropped_frames.load(Ordering::Relaxed),
            underrun_frames: state.ring.underrun_frames.load(Ordering::Relaxed),
            captured_frames: state.ring.captured_frames.load(Ordering::Relaxed),
            played_frames: state.ring.played_frames.load(Ordering::Relaxed),
            requested_latency_frames: REQUESTED_LATENCY,
            sample_rate: SAMPLE_RATE,
            error,
        };
    }
    0
}

struct AudioDevice {
    identifier: String,
    name: String,
}

struct Discovery {
    _pipewire_guard: PipeWireGuard,
    loop_: *mut ffi::pw_thread_loop,
    context: *mut ffi::pw_context,
    core: *mut ffi::pw_core,
    registry: *mut ffi::pw_registry,
    core_listener: ffi::spa_hook,
    registry_listener: ffi::spa_hook,
    core_events: ffi::pw_core_events,
    registry_events: ffi::pw_registry_events,
    core_hook_added: bool,
    registry_hook_added: bool,
    loop_started: bool,
    sync_sequence: i32,
    done: bool,
    error: String,
    sources: Vec<AudioDevice>,
    outputs: Vec<AudioDevice>,
}

impl Discovery {
    fn new(pipewire_guard: PipeWireGuard) -> Self {
        let mut core_events: ffi::pw_core_events = unsafe { std::mem::zeroed() };
        core_events.version = ffi::PW_VERSION_CORE_EVENTS;
        core_events.done = Some(discovery_done);
        core_events.error = Some(discovery_error);
        let mut registry_events: ffi::pw_registry_events = unsafe { std::mem::zeroed() };
        registry_events.version = ffi::PW_VERSION_REGISTRY_EVENTS;
        registry_events.global = Some(discovery_global);
        Self {
            _pipewire_guard: pipewire_guard,
            loop_: ptr::null_mut(),
            context: ptr::null_mut(),
            core: ptr::null_mut(),
            registry: ptr::null_mut(),
            core_listener: unsafe { std::mem::zeroed() },
            registry_listener: unsafe { std::mem::zeroed() },
            core_events,
            registry_events,
            core_hook_added: false,
            registry_hook_added: false,
            loop_started: false,
            sync_sequence: 0,
            done: false,
            error: String::new(),
            sources: Vec::new(),
            outputs: Vec::new(),
        }
    }
}

// spa_hook_remove is an inline SPA API and is implemented here to avoid a C shim.
unsafe fn remove_hook(hook: &mut ffi::spa_hook) {
    let list = &mut hook.link;
    if !list.prev.is_null() {
        unsafe {
            (*list.prev).next = list.next;
            (*list.next).prev = list.prev;
        }
    }
    if let Some(removed) = hook.removed {
        unsafe { removed(hook) };
    }
}

impl Drop for Discovery {
    fn drop(&mut self) {
        unsafe {
            if self.loop_started {
                ffi::pw_thread_loop_stop(self.loop_);
            }
            if self.registry_hook_added {
                remove_hook(&mut self.registry_listener);
            }
            if self.core_hook_added {
                remove_hook(&mut self.core_listener);
            }
            if !self.registry.is_null() {
                ffi::pw_proxy_destroy(self.registry.cast::<ffi::pw_proxy>());
            }
            if !self.core.is_null() {
                ffi::pw_core_disconnect(self.core);
            }
            if !self.context.is_null() {
                ffi::pw_context_destroy(self.context);
            }
            if !self.loop_.is_null() {
                ffi::pw_thread_loop_destroy(self.loop_);
            }
        }
    }
}

unsafe fn dict_value(dictionary: *const ffi::spa_dict, key: &[u8]) -> Option<String> {
    if dictionary.is_null() {
        return None;
    }
    let dictionary = unsafe { &*dictionary };
    if dictionary.items.is_null() {
        return None;
    }
    for item in unsafe { std::slice::from_raw_parts(dictionary.items, dictionary.n_items as usize) }
    {
        if item.key.is_null() || item.value.is_null() {
            continue;
        }
        if unsafe { CStr::from_ptr(item.key) }.to_bytes() == key {
            return Some(
                unsafe { CStr::from_ptr(item.value) }
                    .to_string_lossy()
                    .into_owned(),
            );
        }
    }
    None
}

unsafe extern "C" fn discovery_global(
    userdata: *mut c_void,
    _identifier: u32,
    _permissions: u32,
    type_: *const c_char,
    _version: u32,
    properties: *const ffi::spa_dict,
) {
    if userdata.is_null() || type_.is_null() {
        return;
    }
    if unsafe { CStr::from_ptr(type_) }.to_bytes()
        != &ffi::PW_TYPE_INTERFACE_Node[..ffi::PW_TYPE_INTERFACE_Node.len() - 1]
    {
        return;
    }
    let class = unsafe { dict_value(properties, b"media.class") };
    let source = class.as_deref() == Some("Audio/Source");
    if !source && class.as_deref() != Some("Audio/Sink") {
        return;
    }
    let Some(identifier) = (unsafe { dict_value(properties, b"node.name") }) else {
        return;
    };
    if identifier.is_empty() {
        return;
    }
    let name = unsafe { dict_value(properties, b"node.description") }
        .or_else(|| unsafe { dict_value(properties, b"node.nick") })
        .unwrap_or_else(|| identifier.clone());
    let discovery = unsafe { &mut *userdata.cast::<Discovery>() };
    let device = AudioDevice { identifier, name };
    if source {
        discovery.sources.push(device);
    } else {
        discovery.outputs.push(device);
    }
}

unsafe extern "C" fn discovery_done(userdata: *mut c_void, identifier: u32, sequence: i32) {
    if userdata.is_null() {
        return;
    }
    let discovery = unsafe { &mut *userdata.cast::<Discovery>() };
    if identifier == ffi::PW_ID_CORE && sequence == discovery.sync_sequence {
        discovery.done = true;
        unsafe { ffi::pw_thread_loop_signal(discovery.loop_, false) };
    }
}

unsafe extern "C" fn discovery_error(
    userdata: *mut c_void,
    _identifier: u32,
    _sequence: i32,
    _result: i32,
    message: *const c_char,
) {
    if userdata.is_null() {
        return;
    }
    let discovery = unsafe { &mut *userdata.cast::<Discovery>() };
    discovery.error = if message.is_null() {
        "PipeWire device discovery failed.".into()
    } else {
        unsafe { CStr::from_ptr(message) }
            .to_string_lossy()
            .into_owned()
    };
    discovery.done = true;
    unsafe { ffi::pw_thread_loop_signal(discovery.loop_, false) };
}

fn discover_devices() -> Result<Box<Discovery>, String> {
    let pipewire_guard = PipeWireGuard::new();
    let mut discovery = Box::new(Discovery::new(pipewire_guard));
    unsafe {
        let state = discovery.as_mut() as *mut Discovery;
        (*state).loop_ = ffi::pw_thread_loop_new(c"mygoframe-devices".as_ptr(), ptr::null());
        if (*state).loop_.is_null() {
            return Err("Cannot initialize PipeWire device enumeration.".into());
        }
        (*state).context = ffi::pw_context_new(
            ffi::pw_thread_loop_get_loop((*state).loop_),
            ptr::null_mut(),
            0,
        );
        if (*state).context.is_null() {
            return Err("Cannot initialize PipeWire discovery context.".into());
        }
        (*state).core = ffi::pw_context_connect((*state).context, ptr::null_mut(), 0);
        if (*state).core.is_null() {
            return Err("Cannot connect to PipeWire.".into());
        }
        let userdata = state.cast::<c_void>();
        if pw_core_add_listener(
            (*state).core,
            &mut (*state).core_listener,
            &(*state).core_events,
            userdata,
        ) < 0
        {
            return Err("Cannot observe PipeWire core.".into());
        }
        (*state).core_hook_added = true;
        (*state).registry = pw_core_get_registry((*state).core, ffi::PW_VERSION_REGISTRY, 0);
        if (*state).registry.is_null() {
            return Err("Cannot access PipeWire registry.".into());
        }
        if pw_registry_add_listener(
            (*state).registry,
            &mut (*state).registry_listener,
            &(*state).registry_events,
            userdata,
        ) < 0
        {
            return Err("Cannot observe PipeWire registry.".into());
        }
        (*state).registry_hook_added = true;
        (*state).sync_sequence = pw_core_sync((*state).core, ffi::PW_ID_CORE, 0);
        if (*state).sync_sequence < 0 || ffi::pw_thread_loop_start((*state).loop_) < 0 {
            return Err("Cannot start PipeWire device enumeration.".into());
        }
        (*state).loop_started = true;
        ffi::pw_thread_loop_lock((*state).loop_);
        let mut deadline: ffi::timespec = std::mem::zeroed();
        let _ = ffi::pw_thread_loop_get_time((*state).loop_, &mut deadline, 3_000_000_000);
        while !(*state).done {
            if ffi::pw_thread_loop_timed_wait_full((*state).loop_, &deadline) < 0 {
                (*state).error = "PipeWire device enumeration timed out.".into();
                break;
            }
        }
        ffi::pw_thread_loop_unlock((*state).loop_);
        ffi::pw_thread_loop_stop((*state).loop_);
        (*state).loop_started = false;
        let error = std::mem::take(&mut (*state).error);
        if !error.is_empty() {
            return Err(error);
        }
    }
    Ok(discovery)
}

fn json_escape(value: &str) -> String {
    let mut result = String::with_capacity(value.len() + 2);
    result.push('"');
    for character in value.chars() {
        match character {
            '"' => result.push_str("\\\""),
            '\\' => result.push_str("\\\\"),
            '\n' => result.push_str("\\n"),
            '\r' => result.push_str("\\r"),
            '\t' => result.push_str("\\t"),
            character if character < ' ' => {
                result.push_str(&format!("\\u{:04x}", character as u32))
            }
            character => result.push(character),
        }
    }
    result.push('"');
    result
}

fn devices_json(devices: &[AudioDevice]) -> String {
    let mut sorted = devices.iter().collect::<Vec<_>>();
    sorted.sort_by(|left, right| left.name.cmp(&right.name));
    let entries = sorted
        .iter()
        .map(|device| {
            format!(
                "{{\"id\":{},\"name\":{}}}",
                json_escape(&device.identifier),
                json_escape(&device.name)
            )
        })
        .collect::<Vec<_>>()
        .join(",");
    format!("[{entries}]")
}

#[unsafe(no_mangle)]
pub extern "C" fn mgfr_audio_devices_json() -> *mut c_char {
    match discover_devices() {
        Ok(discovery) => make_c_string(format!(
            "{{\"sources\":{},\"outputs\":{}}}",
            devices_json(&discovery.sources),
            devices_json(&discovery.outputs)
        )),
        Err(message) => make_c_string(format!("{{\"error\":{}}}", json_escape(&message))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::time::{Duration, Instant};

    #[test]
    fn ring_gain_underrun_overflow_and_discard() {
        let ring = AudioRing::new();
        ring.set_volume(0.5);
        ring.capture(Some(&[1.0, -1.0, 0.25, -0.25]), 2);
        let mut output = [f32::NAN; 8];
        ring.playback(&mut output);
        assert_eq!(output, [0.5, -0.5, 0.125, -0.125, 0.0, 0.0, 0.0, 0.0]);
        assert_eq!(ring.underrun_frames.load(Ordering::Relaxed), 2);
        ring.capture(None, 2);
        ring.playback(&mut output);
        assert!(output.iter().all(|sample| *sample == 0.0));
        let long = vec![1.0_f32; (CAPACITY + 16) * CHANNELS];
        ring.capture(Some(&long), CAPACITY + 16);
        ring.playback(&mut output);
        assert!(output.iter().all(|sample| *sample == 0.0));
        assert!(ring.dropped_frames.load(Ordering::Relaxed) >= 16);
    }

    #[test]
    fn concurrent_callbacks_keep_stereo_pairs() {
        let ring = Arc::new(AudioRing::new());
        let producer = Arc::clone(&ring);
        let thread = std::thread::spawn(move || {
            let mut input = [0.0_f32; 256];
            for index in 0..20_000 {
                input.fill(index as f32);
                for pair in input.as_chunks_mut::<2>().0 {
                    pair[1] = -pair[0];
                }
                producer.capture(Some(&input), 128);
            }
        });
        let mut output = [0.0_f32; 256];
        for _ in 0..20_000 {
            ring.playback(&mut output);
            for pair in output.as_chunks::<2>().0 {
                assert_eq!(pair[0], -pair[1]);
            }
        }
        thread.join().unwrap();
        assert_eq!(ring.captured_frames.load(Ordering::Relaxed), 2_560_000);
    }

    #[test]
    fn audio_pod_matches_spa_layout() {
        let format = AudioFormatPod::new();
        assert_eq!(size_of::<AudioFormatPod>(), 168);
        assert_eq!(format.object.pod.size, 160);
        assert_eq!(format.positions.property.value.size, 16);
        assert_eq!(format.positions.positions, [3, 4]);
        // The reference bytes come from spa_format_audio_raw_build in the installed SPA headers.
        let reference = "a00000000f0000000300040003000000010000000000000004000000030000000100000000000000020000000000000004000000030000000100000000000000010001000000000004000000030000001b010000000000000300010000000000040000000400000080bb0000000000000400010000000000040000000400000002000000000000000500010000000000100000000d00000004000000030000000300000004000000";
        let expected = reference
            .as_bytes()
            .as_chunks::<2>()
            .0
            .iter()
            .map(|digits| u8::from_str_radix(std::str::from_utf8(digits).unwrap(), 16).unwrap())
            .collect::<Vec<_>>();
        let actual = unsafe {
            std::slice::from_raw_parts(
                (&format as *const AudioFormatPod).cast::<u8>(),
                size_of::<AudioFormatPod>(),
            )
        };
        assert_eq!(actual, expected);
    }

    #[test]
    fn json_escapes_device_names() {
        assert_eq!(json_escape("x\"\\\n"), "\"x\\\"\\\\\\n\"");
    }

    fn process_resource_counts() -> (usize, usize) {
        let descriptors = std::fs::read_dir("/proc/self/fd")
            .expect("Cannot inspect process file descriptors.")
            .count();
        let threads = std::fs::read_dir("/proc/self/task")
            .expect("Cannot inspect process threads.")
            .count();
        (descriptors, threads)
    }

    fn assert_resources_return_to(baseline: (usize, usize), iteration: usize) {
        let deadline = Instant::now() + Duration::from_secs(1);
        loop {
            let current = process_resource_counts();
            if current == baseline {
                return;
            }
            assert!(
                Instant::now() < deadline,
                "PipeWire cycle {iteration} retained resources: baseline {baseline:?}, current {current:?}."
            );
            std::thread::sleep(Duration::from_millis(10));
        }
    }

    struct AudioHandle(*mut c_void);

    impl Drop for AudioHandle {
        fn drop(&mut self) {
            unsafe { mgfr_audio_destroy(self.0) };
        }
    }

    fn assert_discovery_contains(source: &str) {
        let json_pointer = mgfr_audio_devices_json();
        assert!(
            !json_pointer.is_null(),
            "PipeWire discovery returned no result."
        );
        let json = unsafe { CStr::from_ptr(json_pointer) }
            .to_string_lossy()
            .into_owned();
        unsafe { crate::mgfr_string_free(json_pointer) };
        assert!(
            json.starts_with("{\"sources\":"),
            "PipeWire discovery failed: {json}"
        );
        assert!(
            json.contains(&json_escape(source)),
            "AUDIO_SOURCE was not found in the PipeWire source list."
        );
    }

    fn assert_missing_source_rejected(output: &str) {
        let source = CString::new("mygoframe.nonexistent.integration-source").unwrap();
        let output = CString::new(output).expect("AUDIO_OUTPUT contains a NUL byte.");
        let mut error = ptr::null_mut();
        let audio = unsafe { mgfr_audio_create(source.as_ptr(), output.as_ptr(), 0.0, &mut error) };
        let message = if error.is_null() {
            String::new()
        } else {
            let message = unsafe { CStr::from_ptr(error) }
                .to_string_lossy()
                .into_owned();
            unsafe { crate::mgfr_string_free(error) };
            message
        };
        if !audio.is_null() {
            unsafe { mgfr_audio_destroy(audio) };
        }
        assert!(audio.is_null(), "A missing audio source was accepted.");
        assert!(
            !message.is_empty(),
            "A missing audio source produced no error."
        );
    }

    fn assert_muted_stream_runs(source: &str, output: &str) {
        let source = CString::new(source).expect("AUDIO_SOURCE contains a NUL byte.");
        let output = CString::new(output).expect("AUDIO_OUTPUT contains a NUL byte.");
        let mut error = ptr::null_mut();
        let audio = unsafe { mgfr_audio_create(source.as_ptr(), output.as_ptr(), 0.0, &mut error) };
        let message = if error.is_null() {
            String::new()
        } else {
            let message = unsafe { CStr::from_ptr(error) }
                .to_string_lossy()
                .into_owned();
            unsafe { crate::mgfr_string_free(error) };
            message
        };
        assert!(
            !audio.is_null(),
            "Cannot start the selected PipeWire source: {message}"
        );
        let audio = AudioHandle(audio);
        std::thread::sleep(Duration::from_millis(200));
        let mut status: ffi::mgfr_audio_status_data = unsafe { std::mem::zeroed() };
        assert_eq!(unsafe { mgfr_audio_status(audio.0, &mut status) }, 0);
        assert_eq!(status.running, 1);
        assert_eq!(
            status.capture_state,
            ffi::pw_stream_state_PW_STREAM_STATE_STREAMING
        );
        assert_eq!(
            status.playback_state,
            ffi::pw_stream_state_PW_STREAM_STATE_STREAMING
        );
        assert!(status.captured_frames > 0);
        assert!(status.played_frames > 0);
        let state = unsafe { &*audio.0.cast::<AudioSession>() }.state.as_ref();
        assert_eq!(
            f32::from_bits(state.ring.volume_bits.load(Ordering::Relaxed)),
            0.0
        );
    }

    #[test]
    #[ignore = "requires a running PipeWire session and an explicit AUDIO_SOURCE node name"]
    fn pipewire_real_device_lifecycle() {
        let source = std::env::var("AUDIO_SOURCE")
            .expect("Set AUDIO_SOURCE to an explicit PipeWire audio input node name.");
        assert!(!source.is_empty(), "AUDIO_SOURCE must not be empty.");
        let output = std::env::var("AUDIO_OUTPUT").unwrap_or_default();
        let missing_source = "mygoframe.nonexistent.integration-source";
        assert_ne!(source, missing_source);

        assert_discovery_contains(&source);
        assert_missing_source_rejected(&output);
        assert_muted_stream_runs(&source, &output);
        let baseline = process_resource_counts();
        println!(
            "PipeWire lifecycle baseline: {} descriptors, {} threads.",
            baseline.0, baseline.1
        );

        for iteration in 1..=5 {
            for _ in 0..10 {
                assert_discovery_contains(&source);
            }
            assert_missing_source_rejected(&output);
            assert_muted_stream_runs(&source, &output);
            assert_resources_return_to(baseline, iteration);
            println!(
                "PipeWire lifecycle cycle {iteration}: {} descriptors, {} threads.",
                baseline.0, baseline.1
            );
        }
    }
}
