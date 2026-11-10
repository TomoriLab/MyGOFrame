import Foundation
struct CapturePreset: Codable, Sendable, Equatable {
    var name: String
    var devicePath: String
    var deviceName: String
    var mode: VideoMode
    var colorRange: String
    var backend: String
    var audioSource: String
    var audioOutput: String
    var volume: Double
    var muted: Bool
    var captureWindow: Bool
    var recording: RecordingConfiguration? = nil
    var monitorEnabled: Bool? = nil

    func validated() throws -> CapturePreset {
        var result = self
        result.name = try PresetName.validated(name)
        if let recording { result.recording = try recording.validated() }
        guard !devicePath.isEmpty, devicePath.utf8.count <= 4096, !devicePath.contains("\0"),
              deviceName.utf8.count <= 4096, !deviceName.contains("\0"),
              audioSource.utf8.count <= 4096, !audioSource.contains("\0"),
              audioOutput.utf8.count <= 4096, !audioOutput.contains("\0"),
              mode.width > 0, mode.height > 0, mode.width <= 32768, mode.height <= 32768,
              mode.intervalNumerator > 0, mode.intervalDenominator > 0,
              mode.format.utf8.count == 4, mode.format.utf8.allSatisfy({ $0 >= 32 && $0 <= 126 }),
              mode.fourcc == mode.format.utf8.enumerated().reduce(UInt32(0), { $0 | UInt32($1.element) << ($1.offset * 8) }),
              ["Auto", "Limited", "Full"].contains(colorRange), ["auto", "cpu", "dma"].contains(backend),
              volume.isFinite, (0...1).contains(volume) else {
            throw CoreError.message("The preset contains invalid capture settings.")
        }
        return result
    }

    func requireDevice(in devices: [VideoDevice]) throws {
        guard devices.contains(where: { $0.path == devicePath && ($0.name == deviceName || deviceName.isEmpty) }) else {
            throw CoreError.message("The preset's capture device is unavailable. Reconnect it and reload devices.")
        }
    }
}

struct PresetDocument: Codable, Sendable {
    var schemaVersion = 2
    var presets: [CapturePreset] = []
    var selectedName: String?
}

struct PresetRequest: Decodable {
    var operation: String
    var name: String?
    var preset: CapturePreset?
}

struct PresetResolution: Codable, Sendable {
    var preset: CapturePreset
    var devices: [VideoDevice]
    var audioDevices: AudioDevices
    var modes: [VideoMode]
    var mode: VideoMode

    static func resolve(_ value: CapturePreset, devices: [VideoDevice], audio: AudioDevices, modes: [VideoMode]) throws -> PresetResolution {
        let preset = try value.validated()
        try preset.requireDevice(in: devices)
        guard let selected = modes.first(where: {
            $0.fourcc == preset.mode.fourcc && $0.format == preset.mode.format &&
            $0.intervalNumerator > 0 && $0.intervalDenominator > 0 &&
            $0.width == preset.mode.width && $0.height == preset.mode.height &&
            UInt64($0.intervalNumerator) * UInt64(preset.mode.intervalDenominator) == UInt64(preset.mode.intervalNumerator) * UInt64($0.intervalDenominator)
        }) else { throw CoreError.message("The preset's video mode is unavailable on this capture device.") }
        guard preset.audioSource.isEmpty || audio.sources.contains(where: { $0.id == preset.audioSource }),
              preset.audioOutput.isEmpty || audio.outputs.contains(where: { $0.id == preset.audioOutput }) else {
            throw CoreError.message("The preset's audio device is unavailable. Reconnect it and reload devices.")
        }
        return PresetResolution(preset: preset, devices: devices, audioDevices: audio, modes: modes, mode: selected)
    }
}

