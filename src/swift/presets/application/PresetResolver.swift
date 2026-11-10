struct PresetResolver {
    let catalog: any MediaRuntime
    func resolve(_ preset: CapturePreset) throws -> PresetResolution {
        let devices = try catalog.videoDevices()
        try preset.requireDevice(in: devices)
        let modes = try catalog.videoModes(preset.devicePath)
        let audio = try catalog.audioDevices()
        return try PresetResolution.resolve(preset, devices: devices, audio: audio, modes: modes)
    }
}
