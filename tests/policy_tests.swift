import Foundation

final class MemoryPresetRepository: PresetRepository {
    var document = PresetDocument()
    var transactions = 0
    var writes = 0
    var rejectWrite = false
    func withDocument(_ mutation: (inout PresetDocument) throws -> Bool) throws -> PresetDocument {
        transactions += 1
        var candidate = document
        if try mutation(&candidate) {
            if rejectWrite { throw CoreError.message("Injected persistence failure.") }
            document = candidate
            writes += 1
        }
        return document
    }
}

final class TestNotification: FrameNotification {
    func duplicateEvents() -> Int32 { -1 }
}

final class TestVideoSession: VideoSessionPort {
    let usesDMA: Bool
    let details: NegotiatedVideo
    init(_ configuration: VideoConfiguration) {
        usesDMA = configuration.backend != 1
        details = NegotiatedVideo(path: configuration.path, backend: usesDMA ? "dmabuf" : "cpu",
            fourcc: configuration.fourcc, format: "NV12", width: configuration.width, height: configuration.height,
            intervalNumerator: configuration.intervalNumerator, intervalDenominator: configuration.intervalDenominator)
    }
    func snapshot() -> VideoProgress? { VideoProgress(running: true, frames: 17, droppedFrames: 0, error: "") }
}

final class TestAudioSession: AudioSessionPort {
    let source: String
    var output: String
    var gain: Float
    var monitor: Bool
    init(source: String, output: String, gain: Float, monitor: Bool) {
        self.source = source; self.output = output; self.gain = gain; self.monitor = monitor
    }
    func updateMonitor(output: String?, enabled: Bool) throws {
        if let output { self.output = output }
        monitor = enabled
    }
    func setVolume(_ gain: Float) { self.gain = gain }
    func snapshot() -> AudioSnapshot { AudioSnapshot(running: true, source: source, output: output) }
}

final class TestRecordingSession: RecordingSessionPort {
    var active = true
    func stop() { active = false }
    func snapshot() -> RecordingSnapshot { RecordingSnapshot(phase: active ? "recording" : "completed") }
}

final class TestMediaRuntime: MediaRuntime {
    var videoStarts: [VideoConfiguration] = []
    var rejectNextVideo = false
    var recordings = 0
    weak var video: TestVideoSession?
    weak var audio: TestAudioSession?
    func makeNotification() throws -> any FrameNotification { TestNotification() }
    func videoDevices() throws -> [VideoDevice] { [VideoDevice(path: "/test/video", name: "Test capture")] }
    func videoModes(_ path: String) throws -> [VideoMode] { [Self.mode] }
    func audioDevices() throws -> AudioDevices { AudioDevices(sources: [AudioDevice(id: "input", name: "Test audio")]) }
    static var mode: VideoMode { VideoMode(fourcc: 0x3231564e, format: "NV12", width: 640, height: 480,
                                         intervalNumerator: 1, intervalDenominator: 60) }
    func startVideo(_ configuration: VideoConfiguration, notification: any FrameNotification,
                    fallbackReason: inout String) throws -> any VideoSessionPort {
        videoStarts.append(configuration)
        if rejectNextVideo {
            rejectNextVideo = false
            throw CoreError.message("Injected video startup failure.")
        }
        let session = TestVideoSession(configuration)
        video = session
        return session
    }
    func startAudio(source: String, output: String, gain: Float, monitor: Bool) throws -> any AudioSessionPort {
        let session = TestAudioSession(source: source, output: output, gain: gain, monitor: monitor)
        audio = session
        return session
    }
    func startRecording(_ configuration: RecordingConfiguration, video: any VideoSessionPort,
                        audio: (any AudioSessionPort)?, details: NegotiatedVideo) throws -> any RecordingSessionPort {
        recordings += 1
        return TestRecordingSession()
    }
}

@main
struct PolicyTests {
    static func rejected(_ action: () throws -> Void) {
        do { try action(); preconditionFailure("An invalid policy operation succeeded.") } catch {}
    }
    static func main() throws {
        let repository = MemoryPresetRepository()
        let service = PresetService(repository: repository)
        let preset = CapturePreset(name: " Console ", devicePath: "/test/video", deviceName: "Test capture",
            mode: TestMediaRuntime.mode, colorRange: "Auto", backend: "dma", audioSource: "input", audioOutput: "",
            volume: 0.37, muted: true, captureWindow: true)
        _ = try service.perform(PresetRequest(operation: "list"))
        precondition(repository.transactions == 1 && repository.writes == 0)
        let saved = try service.perform(PresetRequest(operation: "save", preset: preset))
        precondition(saved.selectedName == "Console" && repository.writes == 1)
        rejected { _ = try service.perform(PresetRequest(operation: "save", preset: preset)) }
        precondition(repository.writes == 1 && repository.document.presets.count == 1)
        repository.rejectWrite = true
        rejected { _ = try service.perform(PresetRequest(operation: "delete", name: "Console")) }
        precondition(repository.document.presets.count == 1 && repository.writes == 1)

        let runtime = TestMediaRuntime()
        var state = try CaptureState(runtime: runtime)
        let configuration = VideoConfiguration(path: "/test/video", fourcc: TestMediaRuntime.mode.fourcc,
            width: 640, height: 480, intervalNumerator: 1, intervalDenominator: 60,
            colorRange: 0, backend: 2, fallbackReason: "")
        try state.startVideo(configuration)
        let first = state.status()
        precondition(first.videoRunning && first.videoBackend == "dmabuf" && first.frames == 17)
        let staleFallback = try state.fallback(expectedGeneration: first.generation + 1, reason: "Stale failure")
        precondition(!staleFallback)
        precondition(runtime.videoStarts.count == 1)
        let activeFallback = try state.fallback(expectedGeneration: first.generation, reason: "Injected renderer failure")
        precondition(activeFallback)
        precondition(runtime.videoStarts.count == 2 && runtime.videoStarts.last?.backend == 1)
        precondition(state.status().requestedVideo?.backend == 2)
        state.configureAudio(volume: 0.37, muted: true)
        try state.startAudio(source: "input", output: "")
        precondition(runtime.audio?.gain == 0)
        state.configureAudio(volume: 0.37, muted: false)
        precondition(runtime.audio?.gain == 0.37)
        try state.setMonitoring(false)
        precondition(runtime.audio?.monitor == false && runtime.audio?.gain == 0.37)
        try state.startRecording(RecordingConfiguration())
        rejected { try state.requirePresetActivation() }
        rejected { try state.startAudio(source: "another input", output: "") }
        precondition(runtime.recordings == 1 && runtime.audio?.source == "input")
        state.stopRecording()
        let resolution = try PresetResolver(catalog: runtime).resolve(preset)
        runtime.rejectNextVideo = true
        rejected { try state.activatePreset(resolution, start: true) }
        precondition(state.status().videoRunning && runtime.videoStarts.last?.path == "/test/video")
        state.shutdown()
        precondition(state.closed && runtime.video == nil && runtime.audio == nil)
        print("Injected preset transactions, session rollback, audio policy, recording guards, and resource retirement passed.")
    }
}
