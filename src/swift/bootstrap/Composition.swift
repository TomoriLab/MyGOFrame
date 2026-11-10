enum Composition {
    static let media = NativeMediaRuntime(outputPath: RecordingOutputPath.resolve)
    static var presets: PresetService { PresetService(repository: FilePresetRepository()) }
    static var presetResolver: PresetResolver { PresetResolver(catalog: media) }
    static func makeState() throws -> CaptureState { try CaptureState(runtime: media) }
}
