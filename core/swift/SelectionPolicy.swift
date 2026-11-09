import Foundation
import NativeCore

struct VideoMode: Codable, Sendable, Equatable {
    var fourcc: UInt32
    var format: String
    var width: UInt32
    var height: UInt32
    var intervalNumerator: UInt32
    var intervalDenominator: UInt32
}

struct ModeRequest: Codable {
    var modes: [VideoMode]
    var format: String
    var width: UInt32
    var height: UInt32
    var intervalNumerator: UInt32
    var intervalDenominator: UInt32
}

struct ModeSelection: Codable, Sendable {
    var formats: [String]
    var resolutions: [VideoMode]
    var frameRates: [VideoMode]
    var selected: VideoMode?

    static func resolve(_ request: ModeRequest) -> ModeSelection {
        let modes = request.modes.filter {
            $0.width > 0 && $0.height > 0 && $0.width <= 16384 && $0.height <= 16384 &&
                $0.intervalNumerator > 0 && $0.intervalDenominator > 0
        }
        var formats: [String] = []
        for mode in modes where !formats.contains(mode.format) { formats.append(mode.format) }
        let format = formats.contains(request.format) ? request.format : (formats.first ?? "")
        let matchingFormat = modes.filter { $0.format == format }
        var resolutions: [VideoMode] = []
        for mode in matchingFormat where !resolutions.contains(where: { $0.width == mode.width && $0.height == mode.height }) {
            resolutions.append(mode)
        }
        let resolution = resolutions.first { $0.width == request.width && $0.height == request.height } ?? resolutions.first
        var frameRates: [VideoMode] = []
        for mode in matchingFormat where mode.width == resolution?.width && mode.height == resolution?.height {
            if !frameRates.contains(where: { $0.intervalNumerator == mode.intervalNumerator && $0.intervalDenominator == mode.intervalDenominator }) {
                frameRates.append(mode)
            }
        }
        let selected = frameRates.first {
            $0.intervalNumerator == request.intervalNumerator && $0.intervalDenominator == request.intervalDenominator
        } ?? frameRates.first
        return ModeSelection(formats: formats, resolutions: resolutions, frameRates: frameRates, selected: selected)
    }
}

struct VideoDevice: Codable, Sendable { var path: String; var name: String }
struct AudioDevice: Codable, Sendable { var id: String; var name: String }
struct AudioDevices: Codable, Sendable { var sources: [AudioDevice] = []; var outputs: [AudioDevice] = [] }
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

func decodeRustJSON<Value: Decodable>(_ pointer: UnsafeMutablePointer<CChar>?, as type: Value.Type) throws -> Value {
    guard let pointer else { throw CoreError.message("The media core returned no response.") }
    defer { mgfr_string_free(pointer) }
    let data = Data(String(cString: pointer).utf8)
    if let failure = try? JSONDecoder().decode([String: String].self, from: data), let message = failure["error"] {
        throw CoreError.message(message)
    }
    return try JSONDecoder().decode(type, from: data)
}

@_cdecl("mgf_resolve_modes_json")
public func resolveModesJSON(_ input: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let input else { return strdup("{\"error\":\"A mode selection request is required.\"}") }
    do {
        let request = try JSONDecoder().decode(ModeRequest.self, from: Data(String(cString: input).utf8))
        return try encodeResponse(ModeSelection.resolve(request))
    } catch { return errorResponse(error) }
}

func encodeResponse<Value: Encodable>(_ value: Value) throws -> UnsafeMutablePointer<CChar>? {
    strdup(String(decoding: try JSONEncoder().encode(value), as: UTF8.self))
}

func errorResponse(_ error: Error) -> UnsafeMutablePointer<CChar>? {
    (try? encodeResponse(["error": String(describing: error)])) ?? nil
}
