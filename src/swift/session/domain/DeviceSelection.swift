import Foundation
struct DeviceCatalog: Codable, Sendable {
    var videoAvailable = false
    var audioAvailable = false
    var modesAvailable = false
    var devices: [VideoDevice] = []
    var audioDevices = AudioDevices()
    var modes: [VideoMode] = []
    var selectedPath = ""
    var source = ""
    var output = ""
    var error = ""
}

struct DeviceSelection: Sendable {
    var path: String
    var source: String
    var output: String
    var stopAudio: Bool

    static func resolve(devices: [VideoDevice], audio: AudioDevices, path: String, source: String, output: String) -> DeviceSelection {
        let sourcePresent = source.isEmpty || audio.sources.contains { $0.id == source }
        let outputPresent = output.isEmpty || audio.outputs.contains { $0.id == output }
        return DeviceSelection(path: devices.first { $0.path == path }?.path ?? devices.first?.path ?? "",
            source: sourcePresent && outputPresent ? source : "", output: outputPresent ? output : "",
            stopAudio: !sourcePresent || !outputPresent)
    }
}

