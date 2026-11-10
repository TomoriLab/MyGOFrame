import Foundation
enum RecordingOutputPath {
    static func resolve(_ configuration: RecordingConfiguration) -> String {
        if !configuration.path.isEmpty { return configuration.path }
        let date = ISO8601DateFormatter().string(from: Date()).replacingOccurrences(of: ":", with: "-")
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Videos/MyGOFrame/Recording-\(date)-\(UUID().uuidString.prefix(8)).mkv").path
    }
}
