protocol PresetRepository {
    func withDocument(_ mutation: (inout PresetDocument) throws -> Bool) throws -> PresetDocument
}
