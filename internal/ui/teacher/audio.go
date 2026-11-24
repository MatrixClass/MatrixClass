package teacher

func (a *App) syncMicGain() {
	gain := float32(a.micVolume / 100.0)
	if a.micMuted || gain < 0 {
		gain = 0
	}
	a.micGainBits.Store(uint32(gain * 1000))
}

func (a *App) applySpeakerVolume() {
	vol := float32(a.volume / 100.0)
	if a.speakerMuted {
		vol = 0
	}
	if a.opts.Media != nil {
		a.opts.Media.SetVolume(vol)
	}
	if a.theatrePlayer != nil {
		a.theatrePlayer.SetVolume(float64(vol))
	}
	a.theatre.Volume = vol
}

func (a *App) EffectiveMicGain() float32 {
	return float32(a.micGainBits.Load()) / 1000.0
}

func (a *App) ToggleSpeakerMute() {
	a.speakerMuted = !a.speakerMuted
	a.applySpeakerVolume()
}

func (a *App) ToggleMicMute() {
	a.micMuted = !a.micMuted
	a.syncMicGain()
}

func (a *App) SpeakerMuted() bool { return a.speakerMuted }

func (a *App) MicMuted() bool { return a.micMuted }
