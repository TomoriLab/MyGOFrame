struct AudioDevice: Codable, Sendable { var id: String; var name: String }
struct AudioDevices: Codable, Sendable { var sources: [AudioDevice] = []; var outputs: [AudioDevice] = [] }
