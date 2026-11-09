import Foundation
import Glibc
import NativeCore

enum CoreError: Error, CustomStringConvertible {
    case message(String)

    var description: String {
        switch self { case .message(let message): return message }
    }
}

struct VideoConfiguration: Codable, Sendable {
    let path: String
    let fourcc: UInt32
    let width: UInt32
    let height: UInt32
    let intervalNumerator: UInt32
    let intervalDenominator: UInt32
    let colorRange: Int32
    let backend: Int32
    let fallbackReason: String
}

private final class VideoSession {
    private let owner: NativeHandle
    var handle: UnsafeMutableRawPointer { owner.pointer }
    let usesDMA: Bool

    init(handle: UnsafeMutableRawPointer, usesDMA: Bool) {
        owner = NativeHandle(pointer: handle, destroy: usesDMA ? { mgfr_dma_video_destroy($0) } : { mgfr_video_destroy($0) })
        self.usesDMA = usesDMA
    }

    func readStatus(_ output: UnsafeMutablePointer<mgfr_video_status_data>) -> Int32 {
        usesDMA ? mgfr_dma_video_status(handle, output) : mgfr_video_status(handle, output)
    }

}

private final class AudioSession {
    private let owner: NativeHandle
    var handle: UnsafeMutableRawPointer { owner.pointer }
    let source: String
    let output: String

    init(handle: UnsafeMutableRawPointer, source: String, output: String) {
        owner = NativeHandle(pointer: handle, destroy: { mgfr_audio_destroy($0) })
        self.source = source
        self.output = output
    }

}

private func rustError(_ pointer: UnsafeMutablePointer<CChar>?) -> String {
    guard let pointer else { return "The media core did not provide an error message." }
    defer { mgfr_string_free(pointer) }
    return String(cString: pointer)
}

private func fixedString<Storage>(_ storage: Storage) -> String {
    withUnsafeBytes(of: storage) { bytes in
        String(decoding: bytes.prefix { $0 != 0 }, as: UTF8.self)
    }
}

private func copyRustJSON(_ pointer: UnsafeMutablePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let pointer else { return strdup("{\"error\":\"The media core returned no response.\"}") }
    defer { mgfr_string_free(pointer) }
    return strdup(pointer)
}

private func formatName(_ fourcc: UInt32) -> String {
    String(bytes: (0..<4).map { UInt8((fourcc >> ($0 * 8)) & 255) }, encoding: .ascii) ?? "Unknown"
}

// The registry isolates session state while Rust owns workers and frame storage.
struct CaptureState: ~Copyable {
    private let notification: NativeHandle

    init() throws {
        var failure: UnsafeMutablePointer<CChar>?
        guard let handle = mgfr_notification_create(&failure) else { throw CoreError.message(rustError(failure)) }
        notification = NativeHandle(pointer: handle, destroy: { mgfr_notification_destroy($0) })
    }

    borrowing func duplicateEvents() -> Int32 { mgfr_notification_duplicate(notification.pointer) }

    private var video: VideoSession?
    private var audio: AudioSession?
    private var videoError = ""
    private var videoPhase = SessionPhase.stopped
    private var audioPhase = SessionPhase.stopped
    private var fallbackReason = ""
    private var videoDetails = NegotiatedVideo()
    private var requestedVideo: VideoConfiguration?
    private(set) var closed = false
    private var generation: UInt64 = 0
    private var commandID: UInt64 = 0
    private var frames: UInt64 = 0
    private var droppedFrames: UInt64 = 0
    private var audioPolicy = AudioPolicy()
    private var audioError = ""

    mutating func startVideo(_ configuration: VideoConfiguration) throws {
        advanceCommand(videoChange: true)
        stopVideo(recordCommand: false)
        requestedVideo = configuration
        videoPhase = .starting
        videoError = ""
        fallbackReason = configuration.fallbackReason
        videoDetails = NegotiatedVideo()
        frames = 0
        droppedFrames = 0
        var nativeConfiguration = mgfr_video_config()
        nativeConfiguration.fourcc = configuration.fourcc
        nativeConfiguration.width = configuration.width
        nativeConfiguration.height = configuration.height
        nativeConfiguration.interval_numerator = configuration.intervalNumerator
        nativeConfiguration.interval_denominator = configuration.intervalDenominator
        nativeConfiguration.color_range = configuration.colorRange
        var information = mgfr_video_info()
        var failure: UnsafeMutablePointer<CChar>?
        var usesDMA = false
        let handle = configuration.path.withCString { path -> UnsafeMutableRawPointer? in
            nativeConfiguration.path = path
            if configuration.backend != 1 {
                if let dma = mgfr_dma_video_create(&nativeConfiguration, &information, &failure) {
                    usesDMA = true
                    return dma
                }
                fallbackReason = rustError(failure)
                failure = nil
            }
            return mgfr_video_create(&nativeConfiguration, &information, &failure)
        }
        guard let handle else {
            videoPhase = .failed
            videoError = rustError(failure)
            throw CoreError.message(videoError)
        }
        if let failure { mgfr_string_free(failure) }
        video = VideoSession(handle: handle, usesDMA: usesDMA)
        videoPhase = .running
        if usesDMA { mgfr_dma_video_set_notification(handle, notification.pointer) }
        else { mgfr_video_set_notification(handle, notification.pointer) }
        videoDetails = NegotiatedVideo(path: configuration.path, backend: usesDMA ? "dmabuf" : "cpu",
            fallbackReason: fallbackReason, fourcc: information.fourcc, format: formatName(information.fourcc),
            width: information.width, height: information.height,
            intervalNumerator: information.interval_numerator, intervalDenominator: information.interval_denominator,
            colorRange: configuration.colorRange, bytesPerLine: information.bytes_per_line,
            quantization: information.quantization, encoding: information.encoding, colorspace: information.colorspace,
            transferFunction: information.transfer_function, captureBuffers: information.capture_buffers)
    }

    mutating func stopVideo(recordCommand: Bool = true) {
        if recordCommand { advanceCommand(videoChange: true) }
        videoPhase = .stopping
        if let video {
            var snapshot = mgfr_video_status_data()
            if video.readStatus(&snapshot) == 1 {
                frames = snapshot.frames
                droppedFrames = snapshot.dropped_frames
                let message = fixedString(snapshot.error)
                if !message.isEmpty { videoError = message }
            }
        }
        video = nil
        videoPhase = .stopped
    }

    mutating func acquire(after sequence: UInt64, output: UnsafeMutablePointer<mgf_frame_view>) -> Int32 {
        guard let video, !video.usesDMA else { return 0 }
        var frame = mgfr_frame_view()
        guard mgfr_video_acquire(video.handle, sequence, &frame) == 1 else { return 0 }
        let width = Int(frame.width)
        let height = Int(frame.height)
        let stride = Int(frame.stride)
        guard let token = frame.token, let pixels = frame.pixels,
              width > 0, height > 0, width <= 16384, height <= 16384,
              stride == width * 4, height <= (128 * 1024 * 1024) / stride,
              frame.byte_length == stride * height else {
            mgfr_video_release(frame.token)
            return -1
        }
        output.pointee = mgf_frame_view(token: token, pixels: pixels, byte_length: frame.byte_length,
            width: frame.width, height: frame.height, stride: frame.stride,
            sequence: frame.sequence, timestamp_ns: frame.timestamp_ns)
        return 1
    }

    mutating func acquireDMA(after sequence: UInt64, output: UnsafeMutablePointer<mgfr_dma_frame_view>) -> Int32 {
        guard let video, video.usesDMA else { return 0 }
        var frame = mgfr_dma_frame_view()
        let result = mgfr_dma_video_acquire(video.handle, sequence, &frame)
        guard result == 1 else { return result }
        guard frame.abi_version == 1, frame.struct_size >= MemoryLayout<mgfr_dma_frame_view>.size,
              frame.token != nil, frame.fd >= 0, frame.width > 0, frame.height > 0,
              frame.width <= 16384, frame.height <= 16384 else {
            mgfr_dma_video_release(frame.token)
            return -1
        }
        output.pointee = frame
        return 1
    }

    mutating func startAudio(source: String, output: String) throws {
        advanceCommand()
        stopAudio(recordCommand: false)
        guard !source.isEmpty else { return }
        audioPhase = .starting
        var failure: UnsafeMutablePointer<CChar>?
        let handle = source.withCString { sourcePointer in
            output.withCString { outputPointer in
                mgfr_audio_create(sourcePointer, outputPointer, audioPolicy.gain, &failure)
            }
        }
        guard let handle else {
            audioPhase = .failed
            audioError = rustError(failure)
            throw CoreError.message(audioError)
        }
        if let failure { mgfr_string_free(failure) }
        audio = AudioSession(handle: handle, source: source, output: output)
        audioPhase = .running
    }

    mutating func stopAudio(recordCommand: Bool = true) {
        if recordCommand { advanceCommand() }
        audioPhase = .stopping
        audio = nil
        audioPhase = .stopped
        audioError = ""
    }

    mutating func setVolume(_ value: Float) {
        advanceCommand()
        audioPolicy.setVolume(value)
        if let audio { mgfr_audio_set_volume(audio.handle, audioPolicy.gain) }
    }

    mutating func configureAudio(volume: Float, muted: Bool) {
        advanceCommand()
        audioPolicy.configure(volume: volume, muted: muted)
        if let audio { mgfr_audio_set_volume(audio.handle, audioPolicy.gain) }
    }

    mutating func fallback(expectedGeneration: UInt64, reason: String) throws -> Bool {
        guard expectedGeneration == generation, video?.usesDMA == true,
              let requested = requestedVideo else { return false }
        // Recovery changes the active backend without replacing the user's requested settings.
        defer { requestedVideo = requested }
        try startVideo(VideoConfiguration(path: requested.path, fourcc: requested.fourcc,
            width: requested.width, height: requested.height, intervalNumerator: requested.intervalNumerator,
            intervalDenominator: requested.intervalDenominator, colorRange: requested.colorRange,
            backend: 1, fallbackReason: reason))
        return true
    }

    mutating func refreshDevices(path: String, source: String, output: String) -> DeviceCatalog {
        advanceCommand()
        var catalog = DeviceCatalog()
        var errors: [String] = []
        var videoAvailable = false
        var audioAvailable = false
        do {
            catalog.devices = try decodeRustJSON(mgfr_video_devices_json(), as: [VideoDevice].self)
            videoAvailable = true
            catalog.videoAvailable = true
        } catch { errors.append(String(describing: error)) }
        do {
            catalog.audioDevices = try decodeRustJSON(mgfr_audio_devices_json(), as: AudioDevices.self)
            audioAvailable = true
            catalog.audioAvailable = true
        } catch { errors.append(String(describing: error)) }
        let selection = DeviceSelection.resolve(devices: catalog.devices, audio: catalog.audioDevices,
            path: path, source: source, output: output)
        catalog.selectedPath = videoAvailable ? selection.path : path
        // Enumeration failures do not prove that a running audio device disappeared.
        catalog.source = audioAvailable ? selection.source : source
        catalog.output = audioAvailable ? selection.output : output
        if audioAvailable && selection.stopAudio { stopAudio(recordCommand: false) }
        if !catalog.selectedPath.isEmpty {
            do {
                catalog.modes = try catalog.selectedPath.withCString {
                    try decodeRustJSON(mgfr_video_modes_json($0), as: [VideoMode].self)
                }
                catalog.modesAvailable = true
            } catch { errors.append(String(describing: error)) }
        }
        catalog.error = errors.joined(separator: "; ")
        return catalog
    }

    private mutating func advanceCommand(videoChange: Bool = false) {
        // Counter exhaustion cannot alias an earlier session or command.
        precondition(commandID < UInt64.max && (!videoChange || generation < UInt64.max))
        commandID += 1
        if videoChange { generation += 1 }
    }

    mutating func shutdown() {
        closed = true
        stopVideo(recordCommand: false)
        stopAudio(recordCommand: false)
    }

    mutating func status() -> SessionSnapshot {
        var videoRunning = false
        if let video {
            var snapshot = mgfr_video_status_data()
            if video.readStatus(&snapshot) == 1 {
                videoRunning = snapshot.running != 0
                frames = snapshot.frames
                droppedFrames = snapshot.dropped_frames
                let message = fixedString(snapshot.error)
                if !message.isEmpty { videoError = message }
            } else { videoError = "Unable to read video status from the media core." }
        }
        var audioStatus = AudioSnapshot(error: audioError)
        if let audio {
            var snapshot = mgfr_audio_status_data()
            if mgfr_audio_status(audio.handle, &snapshot) == 0 {
                audioStatus = AudioSnapshot(running: snapshot.running != 0, source: audio.source, output: audio.output,
                    captureState: snapshot.capture_state, playbackState: snapshot.playback_state,
                    queuedFrames: snapshot.queued_frames, droppedFrames: snapshot.dropped_frames,
                    underrunFrames: snapshot.underrun_frames, capturedFrames: snapshot.captured_frames,
                    playedFrames: snapshot.played_frames, requestedLatencyFrames: snapshot.requested_latency_frames,
                    sampleRate: snapshot.sample_rate, error: fixedString(snapshot.error))
            } else { audioStatus.error = "Unable to read audio status from the media core." }
        }
        if video != nil { videoPhase = videoRunning ? .running : (videoError.isEmpty ? .stopped : .failed) }
        if audio != nil { audioPhase = audioStatus.running ? .running : (audioStatus.error.isEmpty ? .stopped : .failed) }
        return SessionSnapshot(generation: generation, commandID: commandID,
            videoPhase: videoPhase,
            audioPhase: audioPhase,
            videoRunning: videoRunning, error: videoError, frames: frames, droppedFrames: droppedFrames,
            video: videoDetails, requestedVideo: requestedVideo, videoBackend: videoDetails.backend,
            fallbackReason: fallbackReason, audio: audioStatus, volume: audioPolicy.volume,
            muted: audioPolicy.muted, effectiveGain: audioPolicy.gain)
    }

}

private func jsonString(_ value: Any) -> UnsafeMutablePointer<CChar>? {
    do {
        let data = try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
        return strdup(String(decoding: data, as: UTF8.self))
    } catch { return strdup("{\"error\":\"Unable to encode the core response.\"}") }
}

@_cdecl("mgf_engine_create")
public func createEngine() -> UInt64 { EngineRegistry.create() }

@_cdecl("mgf_engine_destroy")
public func destroyEngine(_ handle: UInt64) { EngineRegistry.destroy(handle) }

@_cdecl("mgf_string_free")
public func freeString(_ pointer: UnsafeMutablePointer<CChar>?) { free(pointer) }

@_cdecl("mgf_video_devices_json")
public func videoDevices() -> UnsafeMutablePointer<CChar>? {
    copyRustJSON(mgfr_video_devices_json())
}

@_cdecl("mgf_video_modes_json")
public func videoModes(_ path: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let path else { return jsonString(["error": "A video device path is required."]) }
    return copyRustJSON(mgfr_video_modes_json(path))
}

@_cdecl("mgf_audio_devices_json")
public func audioDevices() -> UnsafeMutablePointer<CChar>? {
    copyRustJSON(mgfr_audio_devices_json())
}

@_cdecl("mgf_start_video")
public func startVideo(_ pointer: UInt64, _ path: UnsafePointer<CChar>?,
                       _ fourcc: UInt32, _ width: UInt32, _ height: UInt32,
                       _ numerator: UInt32, _ denominator: UInt32, _ range: Int32) -> UnsafeMutablePointer<CChar>? {
    startVideoBackend(pointer, path, fourcc, width, height, numerator, denominator, range, 1, nil)
}

@_cdecl("mgf_start_video_backend")
public func startVideoBackend(_ pointer: UInt64, _ path: UnsafePointer<CChar>?,
                              _ fourcc: UInt32, _ width: UInt32, _ height: UInt32,
                              _ numerator: UInt32, _ denominator: UInt32, _ range: Int32,
                              _ backend: Int32, _ fallback: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let path else { return strdup("An engine and video device path are required.") }
    guard width > 0, height > 0, width <= 16384, height <= 16384,
          numerator > 0, denominator > 0, (0...2).contains(range), (0...2).contains(backend) else {
        return strdup("Invalid video dimensions, frame interval, color range, or backend.")
    }
    do {
        try EngineRegistry.withState(pointer) { state in try state.startVideo(VideoConfiguration(path: String(cString: path), fourcc: fourcc,
            width: width, height: height, intervalNumerator: numerator,
            intervalDenominator: denominator, colorRange: range, backend: backend,
            fallbackReason: fallback.map { String(cString: $0) } ?? "")) }
        return nil
    } catch { return strdup(String(describing: error)) }
}

@_cdecl("mgf_stop_video")
public func stopVideo(_ pointer: UInt64) {
    try? EngineRegistry.withState(pointer) { $0.stopVideo() }
}

// The C caller lends this output only until the synchronous registry operation returns.
// The lock excludes concurrent writers, and no session stores this pointer.
private struct BorrowedFFIOutput<Value>: @unchecked Sendable {
    let pointer: UnsafeMutablePointer<Value>
}

@_cdecl("mgf_acquire_frame")
public func acquireFrame(_ pointer: UInt64, _ sequence: UInt64,
                         _ output: UnsafeMutablePointer<mgf_frame_view>?) -> Int32 {
    guard let output else { return 0 }
    let borrowed = BorrowedFFIOutput(pointer: output)
    return (try? EngineRegistry.withState(pointer) { $0.acquire(after: sequence, output: borrowed.pointer) }) ?? 0
}

@_cdecl("mgf_acquire_dma_frame")
public func acquireDMAFrame(_ pointer: UInt64, _ sequence: UInt64,
                            _ output: UnsafeMutablePointer<mgfr_dma_frame_view>?) -> Int32 {
    guard let output else { return 0 }
    let borrowed = BorrowedFFIOutput(pointer: output)
    return (try? EngineRegistry.withState(pointer) { $0.acquireDMA(after: sequence, output: borrowed.pointer) }) ?? 0
}

@_cdecl("mgf_release_dma_frame")
public func releaseDMAFrame(_ pointer: UnsafeMutableRawPointer?) {
    mgfr_dma_video_release(pointer)
}

@_cdecl("mgf_release_frame")
public func releaseFrame(_ pointer: UnsafeMutableRawPointer?) {
    mgfr_video_release(pointer)
}

@_cdecl("mgf_status_json")
public func statusJSON(_ pointer: UInt64) -> UnsafeMutablePointer<CChar>? {
    do {
        let snapshot = try EngineRegistry.withState(pointer) { $0.status() }
        return strdup(String(decoding: try JSONEncoder().encode(snapshot), as: UTF8.self))
    } catch { return jsonString(["error": String(describing: error)]) }
}

@_cdecl("mgf_start_audio")
public func startAudio(_ pointer: UInt64, _ source: UnsafePointer<CChar>?,
                       _ output: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let source else { return strdup("An engine and audio source are required.") }
    do {
        try EngineRegistry.withState(pointer) { try $0.startAudio(source: String(cString: source), output: output.map { String(cString: $0) } ?? "") }
        return nil
    } catch { return strdup(String(describing: error)) }
}

@_cdecl("mgf_stop_audio")
public func stopAudio(_ pointer: UInt64) {
    try? EngineRegistry.withState(pointer) { $0.stopAudio() }
}

@_cdecl("mgf_set_volume")
public func setVolume(_ pointer: UInt64, _ volume: Float) {
    try? EngineRegistry.withState(pointer) { $0.setVolume(volume) }
}

@_cdecl("mgf_set_audio_controls")
public func setAudioControls(_ handle: UInt64, _ volume: Float, _ muted: Int32) {
    try? EngineRegistry.withState(handle) { $0.configureAudio(volume: volume, muted: muted != 0) }
}

@_cdecl("mgf_report_gpu_failure")
public func reportGPUFailure(_ handle: UInt64, _ generation: UInt64, _ reason: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    let message = reason.map { String(cString: $0) } ?? "GPU presentation failed."
    do {
        _ = try EngineRegistry.withState(handle) { try $0.fallback(expectedGeneration: generation, reason: message) }
        return nil
    } catch { return strdup(String(describing: error)) }
}

@_cdecl("mgf_refresh_devices_json")
public func refreshDevicesJSON(_ handle: UInt64, _ path: UnsafePointer<CChar>?, _ source: UnsafePointer<CChar>?, _ output: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    let selectedPath = path.map { String(cString: $0) } ?? ""
    let selectedSource = source.map { String(cString: $0) } ?? ""
    let selectedOutput = output.map { String(cString: $0) } ?? ""
    do {
        return try encodeResponse(EngineRegistry.withState(handle) {
            $0.refreshDevices(path: selectedPath, source: selectedSource, output: selectedOutput)
        })
    } catch { return errorResponse(error) }
}

@_cdecl("mgf_frame_events_fd")
public func frameEventsDescriptor(_ handle: UInt64) -> Int32 {
    (try? EngineRegistry.withState(handle) { $0.duplicateEvents() }) ?? -1
}

@_cdecl("mgf_api_version")
public func apiVersion() -> UInt32 { 2 }
