import Foundation
import Dispatch

func runPresetTests() throws {
    func expect(_ condition: Bool) { precondition(condition) }
    let root = FileManager.default.temporaryDirectory.appendingPathComponent("mygoframe-presets-" + UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    let mode = VideoMode(fourcc: 0x3231564e, format: "NV12", width: 1920, height: 1080,
                         intervalNumerator: 1001, intervalDenominator: 60000)
    let original = CapturePreset(name: "  Console 한글  ", devicePath: "/dev/video0", deviceName: "Capture",
        mode: mode, colorRange: "Full", backend: "dma", audioSource: "capture", audioOutput: "",
        volume: 0.37, muted: true, captureWindow: true)
    @Sendable func request(_ operation: String, _ preset: CapturePreset? = nil, name: String? = nil) throws -> PresetDocument {
        try PresetService(repository: FilePresetRepository(directory: root)).perform(PresetRequest(operation: operation, name: name, preset: preset))
    }
    func rejected(_ action: () throws -> Void) {
        do { try action(); preconditionFailure("An invalid preset operation succeeded.") } catch {}
    }
    expect(try request("list").presets.isEmpty)
    let saved = try request("save", original)
    expect(saved.presets.count == 1 && saved.presets[0].name == "Console 한글")
    let path = root.appendingPathComponent("presets.json")
    let bytes = try Data(contentsOf: path)
    rejected { _ = try request("save", original) }
    expect(try Data(contentsOf: path) == bytes)
    var changed = original
    changed.volume = 0.83
    changed.mode.intervalNumerator = 1
    changed.mode.intervalDenominator = 60
    expect(try request("update", changed).presets[0].volume == 0.83)
    expect(try request("list").presets[0].mode.intervalDenominator == 60)
    changed.name = "missing"
    rejected { _ = try request("update", changed) }
    for name in ["", " \n ", "bad\0name", String(repeating: "a", count: 81)] {
        changed = original; changed.name = name
        rejected { _ = try request("save", changed) }
    }
    changed = original; changed.volume = .nan
    rejected { _ = try changed.validated() }
    changed = original; changed.mode.intervalNumerator = 0
    rejected { _ = try changed.validated() }
    changed = original; changed.mode.fourcc = 1
    rejected { _ = try changed.validated() }
    let devices = [VideoDevice(path: "/dev/video0", name: "Capture")]
    let audio = AudioDevices(sources: [AudioDevice(id: "capture", name: "Card")], outputs: [])
    let resolution = try PresetResolution.resolve(original, devices: devices, audio: audio, modes: [mode])
    expect(resolution.mode == mode)
    var invalid = mode
    invalid.intervalNumerator = 0; invalid.intervalDenominator = 0
    rejected { _ = try PresetResolution.resolve(original, devices: devices, audio: audio, modes: [invalid]) }
    var equivalent = mode
    equivalent.intervalNumerator *= 2; equivalent.intervalDenominator *= 2
    expect(try PresetResolution.resolve(original, devices: devices, audio: audio, modes: [equivalent]).mode == equivalent)
    rejected { _ = try PresetResolution.resolve(original, devices: [], audio: audio, modes: [mode]) }
    rejected { _ = try PresetResolution.resolve(original, devices: devices, audio: audio, modes: []) }
    rejected { _ = try PresetResolution.resolve(original, devices: devices, audio: AudioDevices(sources: [], outputs: []), modes: [mode]) }
    expect(try request("delete", name: "Console 한글").presets.isEmpty)
    // Independent writes must retain every entry even when they reach the store concurrently.
    let failures = LockedState(0)
    DispatchQueue.concurrentPerform(iterations: 24) { index in
        var preset = original; preset.name = "Console \(index)"
        do { _ = try request("save", preset) }
        catch { failures.withLock { $0 += 1 } }
    }
    expect(failures.withLock { $0 } == 0)
    expect(try request("list").presets.count == 24)
    _ = try request("select", name: "Console 7")
    expect(try request("list").selectedName == "Console 7")
    rejected { _ = try request("select", name: "missing preset") }
    expect(try request("list").selectedName == "Console 7")
    var legacy = PresetDocument(presets: [original])
    legacy.schemaVersion = 1
    try JSONEncoder().encode(legacy).write(to: path)
    let migrated = try request("list")
    expect(migrated.schemaVersion == 2 && migrated.presets[0].recording == nil)
    expect(try JSONDecoder().decode(PresetDocument.self, from: Data(contentsOf: path)).schemaVersion == 1)
    changed = original
    changed.recording = RecordingConfiguration(gpuID: "nvenc:missing-device", codec: "av1", bitrateMbps: 80, recordAudio: false)
    changed.monitorEnabled = false
    let recorded = try request("update", changed)
    expect(recorded.schemaVersion == 2 && recorded.presets[0].recording?.gpuID == "nvenc:missing-device")
    expect(recorded.presets[0].monitorEnabled == false)
    changed.recording?.bitrateMbps = .infinity
    rejected { _ = try changed.validated() }
    for damaged in [Data("{".utf8), Data("{\"schemaVersion\":99,\"presets\":[]}".utf8), Data(repeating: 32, count: FilePresetRepository.maximumBytes + 1)] {
        try damaged.write(to: path)
        rejected { _ = try request("save", original) }
        expect(try Data(contentsOf: path) == damaged)
    }
    expect(try FilePresetRepository.directory(environment: ["XDG_CONFIG_HOME": "/config", "HOME": "/home/test"]).path == "/config/mygoframe")
    expect(try FilePresetRepository.directory(environment: ["XDG_CONFIG_HOME": "relative", "HOME": "/home/test"]).path == "/home/test/.config/mygoframe")
    var state = try Composition.makeState()
    state.configureAudio(volume: 0.61, muted: true)
    try state.setMonitoring(false)
    var missing = original
    missing.devicePath = "/dev/mygoframe-missing-preset-device"
    missing.audioSource = ""; missing.audioOutput = ""
    missing.volume = 0.2; missing.muted = false; missing.monitorEnabled = true
    let unresolved = PresetResolution(preset: missing, devices: [], audioDevices: AudioDevices(sources: [], outputs: []), modes: [mode], mode: mode)
    do { try state.activatePreset(unresolved, start: true); preconditionFailure("Missing preset device opened.") } catch {}
    let restored = state.status()
    expect(!restored.videoRunning && restored.volume == 0.61 && restored.muted && !restored.monitorEnabled)
    state.shutdown()
    print("Preset persistence, validation, concurrency, corruption, and device resolution tests passed.")
}
