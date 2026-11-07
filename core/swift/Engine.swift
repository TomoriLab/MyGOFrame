import Foundation
import Glibc
import NativeCore

enum CoreError: Error, CustomStringConvertible {
    case message(String)

    var description: String {
        switch self { case .message(let message): return message }
    }
}

struct VideoConfiguration {
    let path: String
    let fourcc: UInt32
    let width: UInt32
    let height: UInt32
    let intervalNumerator: UInt32
    let intervalDenominator: UInt32
    let colorRange: Int32
}

private final class VideoSession {
    let handle: UnsafeMutableRawPointer

    init(handle: UnsafeMutableRawPointer) { self.handle = handle }
    deinit { mgfr_video_destroy(handle) }
}

private final class AudioSession {
    let handle: UnsafeMutableRawPointer
    let source: String
    let output: String

    init(handle: UnsafeMutableRawPointer, source: String, output: String) {
        self.handle = handle
        self.source = source
        self.output = output
    }

    deinit { mgfr_audio_destroy(handle) }
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

// The Go bridge serializes control calls; Rust owns the worker, devices, and frame storage.
final class CaptureEngine {
    private var video: VideoSession?
    private var audio: AudioSession?
    private var videoError = ""
    private var videoDetails: [String: Any] = [:]
    private var frames: UInt64 = 0
    private var droppedFrames: UInt64 = 0
    private var volume: Float = 1

    func startVideo(_ configuration: VideoConfiguration) throws {
        stopVideo()
        videoError = ""
        videoDetails = [:]
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
        let handle = configuration.path.withCString { path in
            nativeConfiguration.path = path
            return mgfr_video_create(&nativeConfiguration, &information, &failure)
        }
        guard let handle else {
            videoError = rustError(failure)
            throw CoreError.message(videoError)
        }
        if let failure { mgfr_string_free(failure) }
        video = VideoSession(handle: handle)
        videoDetails = [
            "path": configuration.path,
            "fourcc": information.fourcc,
            "format": formatName(information.fourcc),
            "width": information.width,
            "height": information.height,
            "intervalNumerator": information.interval_numerator,
            "intervalDenominator": information.interval_denominator,
            "colorRange": configuration.colorRange,
            "bytesPerLine": information.bytes_per_line,
            "quantization": information.quantization,
            "encoding": information.encoding,
            "colorspace": information.colorspace,
            "transferFunction": information.transfer_function,
            "captureBuffers": information.capture_buffers
        ]
    }

    func stopVideo() {
        if let video {
            var snapshot = mgfr_video_status_data()
            if mgfr_video_status(video.handle, &snapshot) == 1 {
                frames = snapshot.frames
                droppedFrames = snapshot.dropped_frames
                let message = fixedString(snapshot.error)
                if !message.isEmpty { videoError = message }
            }
        }
        video = nil
    }

    func acquire(after sequence: UInt64, output: UnsafeMutablePointer<mgf_frame_view>) -> Int32 {
        guard let video else { return 0 }
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

    func startAudio(source: String, output: String) throws {
        stopAudio()
        guard !source.isEmpty else { return }
        var failure: UnsafeMutablePointer<CChar>?
        let handle = source.withCString { sourcePointer in
            output.withCString { outputPointer in
                mgfr_audio_create(sourcePointer, outputPointer, volume, &failure)
            }
        }
        guard let handle else { throw CoreError.message(rustError(failure)) }
        if let failure { mgfr_string_free(failure) }
        audio = AudioSession(handle: handle, source: source, output: output)
    }

    func stopAudio() { audio = nil }

    func setVolume(_ value: Float) {
        volume = value.isFinite ? max(0, min(1, value)) : 0
        if let audio { mgfr_audio_set_volume(audio.handle, volume) }
    }

    func status() -> [String: Any] {
        var videoRunning = false
        if let video {
            var snapshot = mgfr_video_status_data()
            if mgfr_video_status(video.handle, &snapshot) == 1 {
                videoRunning = snapshot.running != 0
                frames = snapshot.frames
                droppedFrames = snapshot.dropped_frames
                let message = fixedString(snapshot.error)
                if !message.isEmpty { videoError = message }
            } else {
                videoError = "Unable to read video status from the media core."
            }
        }
        var audioStatus: [String: Any] = ["running": false]
        if let audio {
            var snapshot = mgfr_audio_status_data()
            if mgfr_audio_status(audio.handle, &snapshot) == 0 {
                audioStatus = [
                    "running": snapshot.running != 0,
                    "source": audio.source,
                    "output": audio.output,
                    "captureState": snapshot.capture_state,
                    "playbackState": snapshot.playback_state,
                    "queuedFrames": snapshot.queued_frames,
                    "droppedFrames": snapshot.dropped_frames,
                    "underrunFrames": snapshot.underrun_frames,
                    "capturedFrames": snapshot.captured_frames,
                    "playedFrames": snapshot.played_frames,
                    "requestedLatencyFrames": snapshot.requested_latency_frames,
                    "sampleRate": snapshot.sample_rate,
                    "error": fixedString(snapshot.error)
                ]
            } else {
                audioStatus["error"] = "Unable to read audio status from the media core."
            }
        }
        return [
            "videoRunning": videoRunning,
            "error": videoError,
            "frames": frames,
            "droppedFrames": droppedFrames,
            "video": videoDetails,
            "audio": audioStatus,
            "volume": volume
        ]
    }

    deinit {
        stopVideo()
        stopAudio()
    }
}

private func engine(_ pointer: UnsafeMutableRawPointer) -> CaptureEngine {
    Unmanaged<CaptureEngine>.fromOpaque(pointer).takeUnretainedValue()
}

private func jsonString(_ value: Any) -> UnsafeMutablePointer<CChar>? {
    do {
        let data = try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
        return strdup(String(decoding: data, as: UTF8.self))
    } catch { return strdup("{\"error\":\"Unable to encode the core response.\"}") }
}

@_cdecl("mgf_engine_create")
public func createEngine() -> UnsafeMutableRawPointer {
    Unmanaged.passRetained(CaptureEngine()).toOpaque()
}

@_cdecl("mgf_engine_destroy")
public func destroyEngine(_ pointer: UnsafeMutableRawPointer?) {
    guard let pointer else { return }
    engine(pointer).stopVideo()
    engine(pointer).stopAudio()
    Unmanaged<CaptureEngine>.fromOpaque(pointer).release()
}

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
public func startVideo(_ pointer: UnsafeMutableRawPointer?, _ path: UnsafePointer<CChar>?,
                       _ fourcc: UInt32, _ width: UInt32, _ height: UInt32,
                       _ numerator: UInt32, _ denominator: UInt32, _ range: Int32) -> UnsafeMutablePointer<CChar>? {
    guard let pointer, let path else { return strdup("An engine and video device path are required.") }
    guard width > 0, height > 0, width <= 16384, height <= 16384,
          numerator > 0, denominator > 0, (0...2).contains(range) else {
        return strdup("Invalid video dimensions, frame interval, or color range.")
    }
    do {
        try engine(pointer).startVideo(VideoConfiguration(path: String(cString: path), fourcc: fourcc,
            width: width, height: height, intervalNumerator: numerator,
            intervalDenominator: denominator, colorRange: range))
        return nil
    } catch { return strdup(String(describing: error)) }
}

@_cdecl("mgf_stop_video")
public func stopVideo(_ pointer: UnsafeMutableRawPointer?) {
    if let pointer { engine(pointer).stopVideo() }
}

@_cdecl("mgf_acquire_frame")
public func acquireFrame(_ pointer: UnsafeMutableRawPointer?, _ sequence: UInt64,
                         _ output: UnsafeMutablePointer<mgf_frame_view>?) -> Int32 {
    guard let pointer, let output else { return 0 }
    return engine(pointer).acquire(after: sequence, output: output)
}

@_cdecl("mgf_release_frame")
public func releaseFrame(_ pointer: UnsafeMutableRawPointer?) {
    mgfr_video_release(pointer)
}

@_cdecl("mgf_status_json")
public func statusJSON(_ pointer: UnsafeMutableRawPointer?) -> UnsafeMutablePointer<CChar>? {
    guard let pointer else { return jsonString(["error": "An engine is required."]) }
    return jsonString(engine(pointer).status())
}

@_cdecl("mgf_start_audio")
public func startAudio(_ pointer: UnsafeMutableRawPointer?, _ source: UnsafePointer<CChar>?,
                       _ output: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let pointer, let source else { return strdup("An engine and audio source are required.") }
    do {
        try engine(pointer).startAudio(source: String(cString: source), output: output.map { String(cString: $0) } ?? "")
        return nil
    } catch { return strdup(String(describing: error)) }
}

@_cdecl("mgf_stop_audio")
public func stopAudio(_ pointer: UnsafeMutableRawPointer?) {
    if let pointer { engine(pointer).stopAudio() }
}

@_cdecl("mgf_set_volume")
public func setVolume(_ pointer: UnsafeMutableRawPointer?, _ volume: Float) {
    if let pointer { engine(pointer).setVolume(volume) }
}
