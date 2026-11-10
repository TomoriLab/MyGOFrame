import Foundation
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

