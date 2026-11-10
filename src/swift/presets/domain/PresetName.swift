import Foundation
enum PresetName {
    static func validated(_ name: String) throws -> String {
        let result = name.trimmingCharacters(in: .whitespacesAndNewlines).precomposedStringWithCanonicalMapping
        guard !result.isEmpty, result.count <= 80, result.utf8.count <= 320,
              !result.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) else {
            throw CoreError.message("Use a preset name containing 1 to 80 characters without control characters.")
        }
        return result
    }

}
