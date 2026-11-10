import Foundation
import Glibc
import NativeCore
func decodeRustJSON<Value: Decodable>(_ pointer: UnsafeMutablePointer<CChar>?, as type: Value.Type) throws -> Value {
    guard let pointer else { throw CoreError.message("The media core returned no response.") }
    defer { mgfr_string_free(pointer) }
    let data = Data(String(cString: pointer).utf8)
    if let failure = try? JSONDecoder().decode([String: String].self, from: data), let message = failure["error"] {
        throw CoreError.message(message)
    }
    return try JSONDecoder().decode(type, from: data)
}

func encodeResponse<Value: Encodable>(_ value: Value) throws -> UnsafeMutablePointer<CChar>? {
    strdup(String(decoding: try JSONEncoder().encode(value), as: UTF8.self))
}

func errorResponse(_ error: Error) -> UnsafeMutablePointer<CChar>? {
    (try? encodeResponse(["error": String(describing: error)])) ?? nil
}

func rustError(_ pointer: UnsafeMutablePointer<CChar>?) -> String {
    guard let pointer else { return "The media core did not provide an error message." }
    defer { mgfr_string_free(pointer) }
    return String(cString: pointer)
}

func fixedString<Storage>(_ storage: Storage) -> String {
    withUnsafeBytes(of: storage) { bytes in
        String(decoding: bytes.prefix { $0 != 0 }, as: UTF8.self)
    }
}

func copyRustJSON(_ pointer: UnsafeMutablePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let pointer else { return strdup("{\"error\":\"The media core returned no response.\"}") }
    defer { mgfr_string_free(pointer) }
    return strdup(pointer)
}

func formatName(_ fourcc: UInt32) -> String {
    String(bytes: (0..<4).map { UInt8((fourcc >> ($0 * 8)) & 255) }, encoding: .ascii) ?? "Unknown"
}

