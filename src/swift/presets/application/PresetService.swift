struct PresetService {
    let repository: any PresetRepository
    func perform(_ request: PresetRequest) throws -> PresetDocument {
        try repository.withDocument { document in
            var selectedName = document.selectedName
            switch request.operation {
            case "list": return false
            case "save", "update":
                guard let preset = request.preset else { throw CoreError.message("Capture settings are required.") }
                let value = try preset.validated()
                selectedName = value.name
                if let index = document.presets.firstIndex(where: { $0.name == value.name }) {
                    guard request.operation == "update" else { throw CoreError.message("A preset with this name already exists. Use Update preset to replace it.") }
                    document.presets[index] = value
                } else {
                    guard request.operation == "save" else { throw CoreError.message("The preset no longer exists. Reload the preset list.") }
                    guard document.presets.count < 64 else { throw CoreError.message("Delete a preset before saving more than 64 presets.") }
                    document.presets.append(value)
                }
            case "select":
                let name = try PresetName.validated(request.name ?? "")
                guard document.presets.contains(where: { $0.name == name }) else { throw CoreError.message("The selected preset no longer exists.") }
                selectedName = name
            case "delete":
                let name = try PresetName.validated(request.name ?? "")
                guard let index = document.presets.firstIndex(where: { $0.name == name }) else {
                    throw CoreError.message("The preset no longer exists. Reload the preset list.")
                }
                document.presets.remove(at: index)
            default: throw CoreError.message("The preset operation is not supported.")
            }
            document.presets.sort { $0.name < $1.name }
            if !document.presets.contains(where: { $0.name == selectedName }) { selectedName = document.presets.first?.name }
            document.selectedName = selectedName
            return true
        }
    }
}
