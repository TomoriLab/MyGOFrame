import Foundation
import Glibc
import Synchronization
import NativeCore

private struct AudioFailure: Error, CustomStringConvertible {
    let description: String
}

// The immutable singleton owns initialization and the loader reference for the process lifetime.
private final class PipeWireRuntime: @unchecked Sendable {
    static let initialized = PipeWireRuntime()
    private let dbusLibrary: UnsafeMutableRawPointer?

    private init() {
        // PipeWire unloads its RT module between contexts, but libdbus keeps process-global caches.
        // Keeping the existing optional library resident preserves those roots and prevents each
        // reload from orphaning another cache. Global dbus_shutdown would break other GUI users.
        dbusLibrary = dlopen("libdbus-1.so.3", RTLD_LAZY | RTLD_LOCAL | RTLD_NODELETE)
        pw_init(nil, nil)
        atexit { pw_deinit() }
    }

    deinit {
        if let dbusLibrary { _ = dlclose(dbusLibrary) }
    }
}

private func allocated<Value>(_ value: Value) -> UnsafeMutablePointer<Value> {
    let pointer = UnsafeMutablePointer<Value>.allocate(capacity: 1)
    pointer.initialize(to: value)
    return pointer
}

// This storage remains pinned until both PipeWire streams and their callbacks are destroyed.
private struct AudioState: ~Copyable {
    let ring = AudioRing()
    let captureState = Atomic<Int32>(0)
    let playbackState = Atomic<Int32>(0)
    let error = UnsafeMutablePointer<CChar>.allocate(capacity: 512)
    let captureListener = allocated(spa_hook())
    let playbackListener = allocated(spa_hook())
    let captureEvents = allocated(pw_stream_events())
    let playbackEvents = allocated(pw_stream_events())
    var loop: OpaquePointer?
    var context: OpaquePointer?
    var core: OpaquePointer?
    var captureStream: OpaquePointer?
    var playbackStream: OpaquePointer?
    var loopStarted = false

    init() {
        error.initialize(repeating: 0, count: 512)
        captureEvents.pointee.version = UInt32(PW_VERSION_STREAM_EVENTS)
        captureEvents.pointee.state_changed = audioCaptureStateChanged
        captureEvents.pointee.process = audioCaptureProcess
        playbackEvents.pointee.version = UInt32(PW_VERSION_STREAM_EVENTS)
        playbackEvents.pointee.state_changed = audioPlaybackStateChanged
        playbackEvents.pointee.process = audioPlaybackProcess
    }

    deinit {
        if loopStarted { pw_thread_loop_stop(loop) }
        if let captureStream { pw_stream_destroy(captureStream) }
        if let playbackStream { pw_stream_destroy(playbackStream) }
        if let core { _ = pw_core_disconnect(core) }
        if let context { pw_context_destroy(context) }
        if let loop { pw_thread_loop_destroy(loop) }
        captureListener.deinitialize(count: 1)
        captureListener.deallocate()
        playbackListener.deinitialize(count: 1)
        playbackListener.deallocate()
        captureEvents.deinitialize(count: 1)
        captureEvents.deallocate()
        playbackEvents.deinitialize(count: 1)
        playbackEvents.deallocate()
        error.deinitialize(count: 512)
        error.deallocate()
    }

    borrowing func recordError(_ message: UnsafePointer<CChar>?) {
        if let message {
            _ = strncpy(error, message, 511)
            error[511] = 0
        }
    }
}

// These C-compatible callbacks borrow raw storage and do not retain Swift objects.
private func audioCaptureProcess(_ userdata: UnsafeMutableRawPointer?) {
    guard let state = userdata?.assumingMemoryBound(to: AudioState.self),
          let buffer = pw_stream_dequeue_buffer(state.pointee.captureStream) else { return }
    defer { _ = pw_stream_queue_buffer(state.pointee.captureStream, buffer) }
    guard let samples = buffer.pointee.buffer, samples.pointee.n_datas > 0,
          let data = samples.pointee.datas, let memory = data.pointee.data,
          let chunk = data.pointee.chunk,
          chunk.pointee.offset <= data.pointee.maxsize else { return }
    let stride = UInt32(MemoryLayout<Float>.stride * AudioRing.channels)
    guard chunk.pointee.flags & Int32(SPA_CHUNK_FLAG_CORRUPTED) == 0,
          chunk.pointee.offset % UInt32(MemoryLayout<Float>.alignment) == 0,
          chunk.pointee.stride == 0 || chunk.pointee.stride == Int32(stride) else { return }
    let length = min(chunk.pointee.size, data.pointee.maxsize - chunk.pointee.offset)
    let input = memory.advanced(by: Int(chunk.pointee.offset)).assumingMemoryBound(to: Float.self)
    let source: UnsafePointer<Float>? = chunk.pointee.flags & Int32(SPA_CHUNK_FLAG_EMPTY) == 0 ? UnsafePointer(input) : nil
    state.pointee.ring.capture(source, frames: length / stride)
}

private func audioPlaybackProcess(_ userdata: UnsafeMutableRawPointer?) {
    guard let state = userdata?.assumingMemoryBound(to: AudioState.self),
          let buffer = pw_stream_dequeue_buffer(state.pointee.playbackStream) else { return }
    defer { _ = pw_stream_queue_buffer(state.pointee.playbackStream, buffer) }
    guard let samples = buffer.pointee.buffer, samples.pointee.n_datas > 0,
          let data = samples.pointee.datas, let memory = data.pointee.data,
          let chunk = data.pointee.chunk else { return }
    let stride = UInt32(MemoryLayout<Float>.stride * AudioRing.channels)
    var frames = data.pointee.maxsize / stride
    if buffer.pointee.requested > 0 && buffer.pointee.requested < UInt64(frames) {
        frames = UInt32(buffer.pointee.requested)
    }
    state.pointee.ring.playback(memory.assumingMemoryBound(to: Float.self), frames: frames)
    chunk.pointee.offset = 0
    chunk.pointee.stride = Int32(stride)
    chunk.pointee.size = frames * stride
    buffer.pointee.size = UInt64(frames)
}

private func audioCaptureStateChanged(_ userdata: UnsafeMutableRawPointer?, _ previous: pw_stream_state,
                                      _ current: pw_stream_state, _ error: UnsafePointer<CChar>?) {
    guard let state = userdata?.assumingMemoryBound(to: AudioState.self) else { return }
    state.pointee.captureState.store(current.rawValue, ordering: .relaxed)
    if current == PW_STREAM_STATE_ERROR { state.pointee.recordError(error) }
    if current == PW_STREAM_STATE_UNCONNECTED && previous.rawValue > PW_STREAM_STATE_CONNECTING.rawValue {
        "Audio capture device disconnected.".withCString { state.pointee.recordError($0) }
    }
    pw_thread_loop_signal(state.pointee.loop, false)
}

private func audioPlaybackStateChanged(_ userdata: UnsafeMutableRawPointer?, _ previous: pw_stream_state,
                                       _ current: pw_stream_state, _ error: UnsafePointer<CChar>?) {
    guard let state = userdata?.assumingMemoryBound(to: AudioState.self) else { return }
    state.pointee.playbackState.store(current.rawValue, ordering: .relaxed)
    if current == PW_STREAM_STATE_ERROR { state.pointee.recordError(error) }
    if current == PW_STREAM_STATE_UNCONNECTED && previous.rawValue > PW_STREAM_STATE_CONNECTING.rawValue {
        "Audio output device disconnected.".withCString { state.pointee.recordError($0) }
    }
    pw_thread_loop_signal(state.pointee.loop, false)
}

private func audioProperties(target: String, capture: Bool) throws -> UnsafeMutablePointer<pw_properties> {
    var dictionary = spa_dict()
    guard let properties = pw_properties_new_dict(&dictionary) else {
        throw AudioFailure(description: "Cannot allocate PipeWire stream properties.")
    }
    let entries = [
        "media.type": "Audio", "media.category": capture ? "Capture" : "Playback", "media.role": "Game",
        "application.name": "MyGOFrame", "node.name": capture ? "mygoframe.capture" : "mygoframe.playback",
        "node.description": capture ? "MyGOFrame capture audio" : "MyGOFrame audio monitoring",
        "node.latency": "128/48000", "node.rate": "1/48000", "stream.dont-remix": "false",
        "node.dont-reconnect": "true", "node.dont-fallback": "true"
    ]
    for (key, value) in entries {
        guard pw_properties_set(properties, key, value) >= 0 else {
            pw_properties_free(properties)
            throw AudioFailure(description: "Cannot configure PipeWire stream properties.")
        }
    }
    if !target.isEmpty && pw_properties_set(properties, "target.object", target) < 0 {
        pw_properties_free(properties)
        throw AudioFailure(description: "Cannot set the PipeWire stream target.")
    }
    return properties
}

struct AudioMonitor: ~Copyable {
    private let state: UnsafeMutablePointer<AudioState>
    private let source: String
    private let output: String

    init(source: String, output: String, volume: Float) throws {
        guard !source.isEmpty else { throw AudioFailure(description: "Select an audio input device.") }
        _ = PipeWireRuntime.initialized
        let state = UnsafeMutablePointer<AudioState>.allocate(capacity: 1)
        state.initialize(to: AudioState())
        do {
            state.pointee.ring.setVolume(volume)
            guard let loop = pw_thread_loop_new("mygoframe-audio", nil) else {
                throw AudioFailure(description: "Cannot initialize the PipeWire audio loop.")
            }
            state.pointee.loop = loop
            guard let context = pw_context_new(pw_thread_loop_get_loop(loop), nil, 0) else {
                throw AudioFailure(description: "Cannot initialize the PipeWire audio context.")
            }
            state.pointee.context = context
            guard let core = pw_context_connect(context, nil, 0) else {
                throw AudioFailure(description: "Cannot connect to PipeWire: \(String(cString: strerror(errno)))")
            }
            state.pointee.core = core
            // Separate stream converters let PipeWire resample independent device clocks.
            state.pointee.captureStream = pw_stream_new(core, "MyGOFrame capture", try audioProperties(target: source, capture: true))
            state.pointee.playbackStream = pw_stream_new(core, "MyGOFrame playback", try audioProperties(target: output, capture: false))
            guard state.pointee.captureStream != nil, state.pointee.playbackStream != nil else {
                throw AudioFailure(description: "Cannot initialize PipeWire audio streams.")
            }
            pw_stream_add_listener(state.pointee.captureStream, state.pointee.captureListener, state.pointee.captureEvents, state)
            pw_stream_add_listener(state.pointee.playbackStream, state.pointee.playbackListener, state.pointee.playbackEvents, state)
            let parameters = UnsafeMutableRawPointer.allocate(byteCount: 1024, alignment: 8)
            defer { parameters.deallocate() }
            var builder = spa_pod_builder()
            spa_pod_builder_init(&builder, parameters, 1024)
            var format = spa_audio_info_raw()
            format.format = SPA_AUDIO_FORMAT_F32
            format.rate = 48000
            format.channels = UInt32(AudioRing.channels)
            format.position.0 = SPA_AUDIO_CHANNEL_FL.rawValue
            format.position.1 = SPA_AUDIO_CHANNEL_FR.rawValue
            guard let parameter = spa_format_audio_raw_build(&builder, UInt32(SPA_PARAM_EnumFormat.rawValue), &format) else {
                throw AudioFailure(description: "Cannot construct the PipeWire audio format.")
            }
            var parameterPointer: UnsafePointer<spa_pod>? = UnsafePointer(parameter)
            let flags = pw_stream_flags(rawValue: PW_STREAM_FLAG_AUTOCONNECT.rawValue | PW_STREAM_FLAG_MAP_BUFFERS.rawValue | PW_STREAM_FLAG_RT_PROCESS.rawValue)
            guard pw_stream_connect(state.pointee.captureStream, SPA_DIRECTION_INPUT, UInt32.max, flags, &parameterPointer, 1) >= 0,
                  pw_stream_connect(state.pointee.playbackStream, SPA_DIRECTION_OUTPUT, UInt32.max, flags, &parameterPointer, 1) >= 0 else {
                throw AudioFailure(description: "Cannot connect the selected PipeWire streams.")
            }
            guard pw_thread_loop_start(loop) >= 0 else {
                throw AudioFailure(description: "Cannot start the PipeWire audio loop.")
            }
            state.pointee.loopStarted = true
            pw_thread_loop_lock(loop)
            var deadline = timespec()
            _ = pw_thread_loop_get_time(loop, &deadline, 3_000_000_000)
            while state.pointee.captureState.load(ordering: .relaxed) != PW_STREAM_STATE_STREAMING.rawValue ||
                  state.pointee.playbackState.load(ordering: .relaxed) != PW_STREAM_STATE_STREAMING.rawValue {
                if state.pointee.error[0] != 0 { break }
                if pw_thread_loop_timed_wait_full(loop, &deadline) < 0 {
                    "Audio streams did not start. Check the selected input, output and PipeWire routing.".withCString { state.pointee.recordError($0) }
                    break
                }
            }
            let message = String(cString: state.pointee.error)
            pw_thread_loop_unlock(loop)
            if !message.isEmpty { throw AudioFailure(description: message) }
        } catch {
            state.deinitialize(count: 1)
            state.deallocate()
            throw error
        }
        self.state = state
        self.source = source
        self.output = output
    }

    deinit {
        state.deinitialize(count: 1)
        state.deallocate()
    }

    borrowing func setVolume(_ value: Float) { state.pointee.ring.setVolume(value) }

    var details: [String: Any] {
        let read = state.pointee.ring.readPosition.load(ordering: .acquiring)
        let write = state.pointee.ring.writePosition.load(ordering: .acquiring)
        let captureState = state.pointee.captureState.load(ordering: .relaxed)
        let playbackState = state.pointee.playbackState.load(ordering: .relaxed)
        pw_thread_loop_lock(state.pointee.loop)
        let message = String(cString: state.pointee.error)
        pw_thread_loop_unlock(state.pointee.loop)
        return [
            "running": captureState == PW_STREAM_STATE_STREAMING.rawValue && playbackState == PW_STREAM_STATE_STREAMING.rawValue && message.isEmpty,
            "source": source, "output": output, "captureState": captureState, "playbackState": playbackState,
            "queuedFrames": min(UInt64(AudioRing.capacity), write >= read ? write - read : 0),
            "droppedFrames": state.pointee.ring.droppedFrames.load(ordering: .relaxed),
            "underrunFrames": state.pointee.ring.underrunFrames.load(ordering: .relaxed),
            "capturedFrames": state.pointee.ring.capturedFrames.load(ordering: .relaxed),
            "playedFrames": state.pointee.ring.playedFrames.load(ordering: .relaxed),
            "requestedLatencyFrames": 128, "sampleRate": 48000, "error": message
        ]
    }
}

private final class AudioDeviceDiscovery {
    var loop: OpaquePointer?
    var context: OpaquePointer?
    var core: OpaquePointer?
    var registry: OpaquePointer?
    let coreListener = allocated(spa_hook())
    let registryListener = allocated(spa_hook())
    let coreEvents = allocated(pw_core_events())
    let registryEvents = allocated(pw_registry_events())
    var sources: [[String: String]] = []
    var outputs: [[String: String]] = []
    var syncSequence: Int32 = 0
    var done = false
    var error = ""
    var loopStarted = false

    init() {
        coreEvents.pointee.version = UInt32(PW_VERSION_CORE_EVENTS)
        coreEvents.pointee.done = audioDiscoveryDone
        coreEvents.pointee.error = audioDiscoveryError
        registryEvents.pointee.version = UInt32(PW_VERSION_REGISTRY_EVENTS)
        registryEvents.pointee.global = audioDiscoveryGlobal
    }

    deinit {
        if loopStarted { pw_thread_loop_stop(loop) }
        spa_hook_remove(registryListener)
        spa_hook_remove(coreListener)
        if let registry { pw_proxy_destroy(registry) }
        if let core { _ = pw_core_disconnect(core) }
        if let context { pw_context_destroy(context) }
        if let loop { pw_thread_loop_destroy(loop) }
        coreListener.deinitialize(count: 1)
        coreListener.deallocate()
        registryListener.deinitialize(count: 1)
        registryListener.deallocate()
        coreEvents.deinitialize(count: 1)
        coreEvents.deallocate()
        registryEvents.deinitialize(count: 1)
        registryEvents.deallocate()
    }

    func devices() throws -> [String: Any] {
        _ = PipeWireRuntime.initialized
        defer {
            if loopStarted {
                pw_thread_loop_stop(loop)
                loopStarted = false
            }
        }
        guard let loop = pw_thread_loop_new("mygoframe-devices", nil) else {
            throw AudioFailure(description: "Cannot initialize PipeWire device enumeration.")
        }
        self.loop = loop
        guard let context = pw_context_new(pw_thread_loop_get_loop(loop), nil, 0) else {
            throw AudioFailure(description: "Cannot initialize the PipeWire discovery context.")
        }
        self.context = context
        guard let core = pw_context_connect(context, nil, 0) else {
            throw AudioFailure(description: "Cannot connect to PipeWire: \(String(cString: strerror(errno)))")
        }
        self.core = core
        let userdata = Unmanaged.passUnretained(self).toOpaque()
        guard pw_core_add_listener(core, coreListener, coreEvents, userdata) >= 0,
              let registry = pw_core_get_registry(core, UInt32(PW_VERSION_REGISTRY), 0) else {
            throw AudioFailure(description: "Cannot access the PipeWire registry.")
        }
        self.registry = registry
        guard pw_registry_add_listener(registry, registryListener, registryEvents, userdata) >= 0 else {
            throw AudioFailure(description: "Cannot observe the PipeWire registry.")
        }
        syncSequence = pw_core_sync(core, UInt32(PW_ID_CORE), 0)
        guard syncSequence >= 0, pw_thread_loop_start(loop) >= 0 else {
            throw AudioFailure(description: "Cannot start PipeWire device enumeration.")
        }
        loopStarted = true
        pw_thread_loop_lock(loop)
        defer { pw_thread_loop_unlock(loop) }
        var deadline = timespec()
        _ = pw_thread_loop_get_time(loop, &deadline, 3_000_000_000)
        while !done {
            if pw_thread_loop_timed_wait_full(loop, &deadline) < 0 {
                error = "PipeWire device enumeration timed out."
                break
            }
        }
        if !error.isEmpty { throw AudioFailure(description: error) }
        return ["sources": sources.sorted { $0["name", default: ""] < $1["name", default: ""] },
                "outputs": outputs.sorted { $0["name", default: ""] < $1["name", default: ""] }]
    }
}

private func audioDiscoveryGlobal(_ userdata: UnsafeMutableRawPointer?, _ identifier: UInt32, _ permissions: UInt32,
                                  _ type: UnsafePointer<CChar>?, _ version: UInt32, _ properties: UnsafePointer<spa_dict>?) {
    guard let userdata, let type, let properties,
          strcmp(type, MGF_PW_TYPE_INTERFACE_NODE) == 0,
          let mediaClass = spa_dict_lookup(properties, "media.class") else { return }
    let source = strcmp(mediaClass, "Audio/Source") == 0
    guard source || strcmp(mediaClass, "Audio/Sink") == 0,
          let name = spa_dict_lookup(properties, "node.name"), name.pointee != 0 else { return }
    let description = spa_dict_lookup(properties, "node.description") ?? spa_dict_lookup(properties, "node.nick") ?? name
    let discovery = Unmanaged<AudioDeviceDiscovery>.fromOpaque(userdata).takeUnretainedValue()
    let device = ["id": String(cString: name), "name": String(cString: description)]
    if source { discovery.sources.append(device) }
    else { discovery.outputs.append(device) }
}

private func audioDiscoveryDone(_ userdata: UnsafeMutableRawPointer?, _ identifier: UInt32, _ sequence: Int32) {
    guard let userdata else { return }
    let discovery = Unmanaged<AudioDeviceDiscovery>.fromOpaque(userdata).takeUnretainedValue()
    if identifier == UInt32(PW_ID_CORE) && sequence == discovery.syncSequence {
        discovery.done = true
        pw_thread_loop_signal(discovery.loop, false)
    }
}

private func audioDiscoveryError(_ userdata: UnsafeMutableRawPointer?, _ identifier: UInt32, _ sequence: Int32,
                                 _ result: Int32, _ message: UnsafePointer<CChar>?) {
    guard let userdata else { return }
    let discovery = Unmanaged<AudioDeviceDiscovery>.fromOpaque(userdata).takeUnretainedValue()
    discovery.error = message.map { String(cString: $0) } ?? "PipeWire device discovery failed."
    discovery.done = true
    pw_thread_loop_signal(discovery.loop, false)
}

enum AudioDiscovery {
    static func devices() throws -> [String: Any] { try AudioDeviceDiscovery().devices() }
}
