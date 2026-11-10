import Foundation
struct VideoConfiguration: Codable, Sendable {
    let path: String
    let fourcc: UInt32
    let width: UInt32
    let height: UInt32
    let intervalNumerator: UInt32
    let intervalDenominator: UInt32
    let colorRange: Int32
    let backend: Int32
    let fallbackReason: String
}

