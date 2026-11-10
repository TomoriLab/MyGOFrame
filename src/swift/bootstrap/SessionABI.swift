import Foundation
import Glibc
import NativeCore
private func jsonString(_ value: Any) -> UnsafeMutablePointer<CChar>? {
    do {
        let data = try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
        return strdup(String(decoding: data, as: UTF8.self))
    } catch { return strdup("{\"error\":\"Unable to encode the core response.\"}") }
}

@_cdecl("mgf_engine_create")
public func createEngine() -> UInt64 { EngineRegistry.create(makeState: Composition.makeState) }

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

@_cdecl("mgf_resolve_modes_json")
public func resolveModesJSON(_ input: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let input else { return strdup("{\"error\":\"A mode selection request is required.\"}") }
    do {
        let request = try JSONDecoder().decode(ModeRequest.self, from: Data(String(cString: input).utf8))
        return try encodeResponse(ModeSelection.resolve(request))
    } catch { return errorResponse(error) }
}


func decodePresetRequest<Value: Decodable>(_ input: UnsafePointer<CChar>?, as type: Value.Type) throws -> Value {
    guard let input else { throw CoreError.message("A preset request is required.") }
    let count = strnlen(input, 64 * 1024 + 1)
    guard count <= 64 * 1024 else { throw CoreError.message("The preset request exceeds the size limit.") }
    return try JSONDecoder().decode(type, from: Data(bytes: input, count: count))
}

@_cdecl("mgf_presets_json")
public func presetsJSON(_ input: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    do { return try encodeResponse(Composition.presets.perform(decodePresetRequest(input, as: PresetRequest.self))) }
    catch { return errorResponse(error) }
}

@_cdecl("mgf_apply_preset_json")
public func applyPresetJSON(_ handle: UInt64, _ input: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    do {
        let preset = try decodePresetRequest(input, as: CapturePreset.self).validated()
        return try encodeResponse(EngineRegistry.withState(handle) { state in
            try state.requireStoppedVideoForPreset()
            let resolution = try Composition.presetResolver.resolve(preset)
            try state.applyPresetAudio(preset)
            return resolution
        })
    } catch { return errorResponse(error) }
}

struct PresetActivation: Decodable {
    var preset: CapturePreset
    var startVideo: Bool
}

@_cdecl("mgf_activate_preset_json")
public func activatePresetJSON(_ handle: UInt64, _ input: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    do {
        let request = try decodePresetRequest(input, as: PresetActivation.self)
        let preset = try request.preset.validated()
        return try encodeResponse(EngineRegistry.withState(handle) { state in
            try state.requirePresetActivation()
            // Device and mode validation precede any change to a running session.
            let resolution = try Composition.presetResolver.resolve(preset)
            try state.activatePreset(resolution, start: request.startVideo)
            return resolution
        })
    } catch { return errorResponse(error) }
}

@_cdecl("mgf_recording_devices_json")
public func recordingDevicesJSON() -> UnsafeMutablePointer<CChar>? {
    let pointer = mgfr_recording_devices_json()
    guard let pointer else { return nil }
    defer { mgfr_string_free(pointer) }
    return strdup(pointer)
}

@_cdecl("mgf_start_recording_json")
public func startRecordingJSON(_ handle: UInt64, _ input: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    do {
        let config = try decodePresetRequest(input, as: RecordingConfiguration.self).validated()
        try EngineRegistry.withState(handle) { try $0.startRecording(config) }
        return nil
    } catch { return strdup(String(describing: error)) }
}

@_cdecl("mgf_stop_recording")
public func stopRecording(_ handle: UInt64) { try? EngineRegistry.withState(handle) { $0.stopRecording() } }

@_cdecl("mgf_set_monitoring")
public func setMonitoring(_ handle: UInt64, _ enabled: Int32) -> UnsafeMutablePointer<CChar>? {
    do {
        try EngineRegistry.withState(handle) { try $0.setMonitoring(enabled != 0) }
        return nil
    } catch { return strdup(String(describing: error)) }
}

extension CaptureState {
    mutating func acquire(after sequence: UInt64, output: UnsafeMutablePointer<mgf_frame_view>) -> Int32 {
        return withVideo { session in
            guard let video = session as? VideoSession, !video.usesDMA else { return 0 }
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
    }

    mutating func acquireDMA(after sequence: UInt64, output: UnsafeMutablePointer<mgfr_dma_frame_view>) -> Int32 {
        return withVideo { session in
            guard let video = session as? VideoSession, video.usesDMA else { return 0 }
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
    }

}
