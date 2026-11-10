struct AudioPolicy {
    private(set) var volume: Float = 1
    private(set) var muted = false
    var gain: Float { muted ? 0 : volume }

    mutating func setVolume(_ value: Float) {
        volume = value.isFinite ? max(0, min(1, value)) : 0
    }

    mutating func configure(volume: Float, muted: Bool) {
        setVolume(volume)
        self.muted = muted
    }
}
