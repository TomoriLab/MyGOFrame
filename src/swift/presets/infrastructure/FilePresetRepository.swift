import Foundation
import Glibc
// The process lock complements the file lock because POSIX record locks are process-scoped.
struct FilePresetRepository: PresetRepository {
    let location: URL?
    init(directory: URL? = nil) { location = directory }
    static let maximumBytes = 1024 * 1024
    private static let mutex = NSLock()

    static func directory(environment: [String: String] = ProcessInfo.processInfo.environment) throws -> URL {
        let base: String
        if let configured = environment["XDG_CONFIG_HOME"], configured.hasPrefix("/"), !configured.contains("\0") {
            base = configured
        } else if let home = environment["HOME"], home.hasPrefix("/"), !home.contains("\0") {
            base = home + "/.config"
        } else {
            throw CoreError.message("An absolute HOME or XDG_CONFIG_HOME is required to store presets.")
        }
        return URL(fileURLWithPath: base, isDirectory: true).appendingPathComponent("mygoframe", isDirectory: true)
    }


    func withDocument(_ mutation: (inout PresetDocument) throws -> Bool) throws -> PresetDocument {
        let directory = try location ?? Self.directory()
        Self.mutex.lock()
        defer { Self.mutex.unlock() }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        let descriptor = open(directory.appendingPathComponent("presets.lock").path, O_CREAT | O_RDWR | O_CLOEXEC | O_NOFOLLOW, 0o600)
        guard descriptor >= 0 else { throw CoreError.message("Unable to open the preset lock file.") }
        defer { _ = close(descriptor) }
        while lockf(descriptor, F_LOCK, 0) != 0 {
            if errno != EINTR { throw CoreError.message("Unable to lock the preset file.") }
        }
        let path = directory.appendingPathComponent("presets.json")
        var document = try Self.read(path)
        guard try mutation(&document) else { return document }
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(document)
        guard data.count <= Self.maximumBytes else { throw CoreError.message("The preset file exceeds the size limit.") }
        // Atomic replacement preserves the previous document when a write fails before rename.
        try data.write(to: path, options: .atomic)
        return document
    }

    private static func read(_ path: URL) throws -> PresetDocument {
        do {
            let attributes = try FileManager.default.attributesOfItem(atPath: path.path)
            guard attributes[.type] as? FileAttributeType == .typeRegular else {
                throw CoreError.message("The preset file must be a regular file.")
            }
        } catch let error as CocoaError where error.code == .fileReadNoSuchFile || error.code == .fileNoSuchFile {
            return PresetDocument()
        }
        let handle = try FileHandle(forReadingFrom: path)
        defer { try? handle.close() }
        var data = Data()
        while data.count <= maximumBytes {
            let part = try handle.read(upToCount: maximumBytes + 1 - data.count) ?? Data()
            if part.isEmpty { break }
            data.append(part)
        }
        guard data.count <= maximumBytes else { throw CoreError.message("The preset file exceeds the size limit.") }
        var document: PresetDocument
        do { document = try JSONDecoder().decode(PresetDocument.self, from: data) }
        catch { throw CoreError.message("The preset file is invalid. Restore or repair presets.json before saving changes.") }
        guard document.schemaVersion == 1 || document.schemaVersion == 2 else { throw CoreError.message("The preset file version is not supported.") }
        document.schemaVersion = 2
        guard document.presets.count <= 64 else { throw CoreError.message("The preset file contains too many presets.") }
        var names = Set<String>()
        for index in document.presets.indices {
            document.presets[index] = try document.presets[index].validated()
            guard names.insert(document.presets[index].name).inserted else { throw CoreError.message("The preset file contains duplicate names.") }
        }
        document.presets.sort { $0.name < $1.name }
        return document
    }
}

