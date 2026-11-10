import Foundation
struct NegotiatedVideo: Codable, Sendable {
    var path = ""
    var backend = "stopped"
    var fallbackReason = ""
    var fourcc: UInt32 = 0
    var format = ""
    var width: UInt32 = 0
    var height: UInt32 = 0
    var intervalNumerator: UInt32 = 0
    var intervalDenominator: UInt32 = 0
    var colorRange: Int32 = 0
    var bytesPerLine: UInt32 = 0
    var quantization: UInt32 = 0
    var encoding: UInt32 = 0
    var colorspace: UInt32 = 0
    var transferFunction: UInt32 = 0
    var captureBuffers: UInt32 = 0
}

