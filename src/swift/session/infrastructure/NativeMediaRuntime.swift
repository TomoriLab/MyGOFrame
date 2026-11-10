import Foundation
import NativeCore

final class NativeNotification: FrameNotification {
    private let owner: NativeHandle
    var handle: UnsafeMutableRawPointer { owner.pointer }
    init() throws {
        var failure: UnsafeMutablePointer<CChar>?
        guard let handle = mgfr_notification_create(&failure) else { throw CoreError.message(rustError(failure)) }
        owner = NativeHandle(pointer: handle, destroy: { mgfr_notification_destroy($0) })
    }
    func duplicateEvents() -> Int32 { mgfr_notification_duplicate(handle) }
}

final class VideoSession: VideoSessionPort {
    private let owner: NativeHandle
    var handle: UnsafeMutableRawPointer { owner.pointer }
    let usesDMA: Bool
    let details: NegotiatedVideo
    init(handle: UnsafeMutableRawPointer, usesDMA: Bool, details: NegotiatedVideo) {
        owner = NativeHandle(pointer: handle, destroy: usesDMA ? { mgfr_dma_video_destroy($0) } : { mgfr_video_destroy($0) })
        self.usesDMA = usesDMA
        self.details = details
    }
    func snapshot() -> VideoProgress? {
        var status = mgfr_video_status_data()
        guard (usesDMA ? mgfr_dma_video_status(handle, &status) : mgfr_video_status(handle, &status)) == 1 else { return nil }
        return VideoProgress(running: status.running != 0, frames: status.frames,
                             droppedFrames: status.dropped_frames, error: fixedString(status.error))
    }
}

final class AudioSession: AudioSessionPort {
    private let owner: NativeHandle
    var handle: UnsafeMutableRawPointer { owner.pointer }
    let source: String
    var output: String
    init(handle: UnsafeMutableRawPointer, source: String, output: String) {
        owner = NativeHandle(pointer: handle, destroy: { mgfr_audio_destroy($0) })
        self.source = source
        self.output = output
    }
    func updateMonitor(output: String?, enabled: Bool) throws {
        let failure: UnsafeMutablePointer<CChar>?
        if let output { failure = output.withCString { mgfr_audio_update_monitor(handle, $0, enabled ? 1 : 0) } }
        else { failure = mgfr_audio_update_monitor(handle, nil, enabled ? 1 : 0) }
        if let failure { throw CoreError.message(rustError(failure)) }
    }
    func setVolume(_ gain: Float) { mgfr_audio_set_volume(handle, gain) }
    func snapshot() -> AudioSnapshot {
        var status = mgfr_audio_status_data()
        guard mgfr_audio_status(handle, &status) == 0 else { return AudioSnapshot(error: "Unable to read audio status from the media core.") }
        return AudioSnapshot(inputPeak: mgfr_audio_input_peak(handle), running: status.running != 0, source: source, output: output,
            captureState: status.capture_state, playbackState: status.playback_state,
            queuedFrames: status.queued_frames, droppedFrames: status.dropped_frames,
            underrunFrames: status.underrun_frames, capturedFrames: status.captured_frames,
            playedFrames: status.played_frames, requestedLatencyFrames: status.requested_latency_frames,
            sampleRate: status.sample_rate, error: fixedString(status.error))
    }
}

final class NativeMediaRuntime: MediaRuntime, Sendable {
    private let outputPath: @Sendable (RecordingConfiguration) -> String
    init(outputPath: @escaping @Sendable (RecordingConfiguration) -> String) { self.outputPath = outputPath }
    func makeNotification() throws -> any FrameNotification { try NativeNotification() }
    func videoDevices() throws -> [VideoDevice] { try decodeRustJSON(mgfr_video_devices_json(), as: [VideoDevice].self) }
    func videoModes(_ path: String) throws -> [VideoMode] {
        try path.withCString { try decodeRustJSON(mgfr_video_modes_json($0), as: [VideoMode].self) }
    }
    func audioDevices() throws -> AudioDevices { try decodeRustJSON(mgfr_audio_devices_json(), as: AudioDevices.self) }
    func startVideo(_ configuration: VideoConfiguration, notification: any FrameNotification,
                    fallbackReason: inout String) throws -> any VideoSessionPort {
        guard let notification = notification as? NativeNotification else { throw CoreError.message("Invalid native frame notification owner.") }
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
        guard let handle else { throw CoreError.message(rustError(failure)) }
        if let failure { mgfr_string_free(failure) }
        if usesDMA { mgfr_dma_video_set_notification(handle, notification.handle) }
        else { mgfr_video_set_notification(handle, notification.handle) }
        let details = NegotiatedVideo(path: configuration.path, backend: usesDMA ? "dmabuf" : "cpu",
            fallbackReason: fallbackReason, fourcc: information.fourcc, format: formatName(information.fourcc),
            width: information.width, height: information.height,
            intervalNumerator: information.interval_numerator, intervalDenominator: information.interval_denominator,
            colorRange: configuration.colorRange, bytesPerLine: information.bytes_per_line,
            quantization: information.quantization, encoding: information.encoding, colorspace: information.colorspace,
            transferFunction: information.transfer_function, captureBuffers: information.capture_buffers)
        return VideoSession(handle: handle, usesDMA: usesDMA, details: details)
    }
    func startAudio(source: String, output: String, gain: Float, monitor: Bool) throws -> any AudioSessionPort {
        var failure: UnsafeMutablePointer<CChar>?
        let handle = source.withCString { sourcePointer in
            output.withCString { outputPointer in mgfr_audio_create_mode(sourcePointer, outputPointer, gain, monitor ? 1 : 0, &failure) }
        }
        guard let handle else { throw CoreError.message(rustError(failure)) }
        if let failure { mgfr_string_free(failure) }
        return AudioSession(handle: handle, source: source, output: output)
    }
    func startRecording(_ configuration: RecordingConfiguration, video: any VideoSessionPort,
                        audio: (any AudioSessionPort)?, details: NegotiatedVideo) throws -> any RecordingSessionPort {
        guard let video = video as? VideoSession else { throw CoreError.message("Invalid native video session owner.") }
        let audio = audio as? AudioSession
        var config = mgfr_recording_config()
        config.abi_version = 1
        config.codec = ["h264", "hevc", "av1"].firstIndex(of: configuration.codec).map(Int32.init) ?? 0
        config.rate_control = configuration.rateControl == "cbr" ? 1 : 0
        config.bitrate = UInt64((configuration.bitrateMbps * 1_000_000).rounded())
        config.color_range = details.colorRange
        config.record_audio = configuration.recordAudio ? 1 : 0
        config.video.fourcc = details.fourcc; config.video.width = details.width; config.video.height = details.height
        config.video.bytes_per_line = details.bytesPerLine
        config.video.interval_numerator = details.intervalNumerator; config.video.interval_denominator = details.intervalDenominator
        config.video.quantization = details.quantization; config.video.encoding = details.encoding
        config.video.colorspace = details.colorspace; config.video.transfer_function = details.transferFunction
        var failure: UnsafeMutablePointer<CChar>?
        let owner = outputPath(configuration).withCString { path in
            configuration.gpuID.withCString { device in
                config.path = path; config.gpu_id = device
                return mgfr_recording_create(video.handle, video.usesDMA ? 1 : 0, audio?.handle, &config, &failure)
            }
        }
        guard let owner else { throw CoreError.message(rustError(failure)) }
        if let failure { mgfr_string_free(failure) }
        return RecordingSession(handle: owner)
    }
}
