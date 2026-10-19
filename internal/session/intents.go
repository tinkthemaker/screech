package session

import (
	"fmt"
	"time"
)

// love toggles love on the current station and, when one is parsed, the
// playing track. loveAt's pulse is a face concern.
func (s State) love() (State, []Cmd) {
	if !s.HaveStation || s.Phase == Stopped {
		return s, nil
	}
	_, track, lovedNow, err := s.core.Love(time.Now())
	if err != nil {
		s.Note = "could not save love " + s.dot() + " " + err.Error()
		return s, nil
	}
	s.LovedTrack = lovedNow
	if lovedNow {
		if track {
			s.setFeedback("LOVED  Track and station")
		} else {
			s.setFeedback("LOVED  Station")
		}
	} else {
		s.setFeedback("UNLOVED  Love returned")
	}
	return s, nil
}

func (s State) togglePreset() (State, []Cmd) {
	if !s.HaveStation || s.Phase == Stopped {
		return s, nil
	}
	slot, saved, full, err := s.core.TogglePreset(time.Now())
	if err != nil {
		s.Note = "could not save preset " + s.dot() + " " + err.Error()
		return s, nil
	}
	s.Presets = s.core.Presets()
	// Feedback only — the WHY row still describes the pick.
	switch {
	case full:
		s.setFeedback("PRESETS  All nine full " + s.dot() + " F on a saved one frees a slot")
	case saved:
		s.setFeedback(fmt.Sprintf("SAVED  Preset %d "+s.dot()+" press %d to return", slot, slot))
	case slot > 0:
		s.setFeedback(fmt.Sprintf("CLEARED  Preset %d", slot))
	}
	return s, nil
}

// recallPreset is preset recall: an empty slot narrates itself, the
// current station is a no-op, anything else tunes with the slot's reason.
func (s State) recallPreset(msg RecallPreset) (State, []Cmd) {
	uuid, ok := s.Presets[msg.Slot]
	if !ok {
		s.setFeedback(fmt.Sprintf("PRESET  %d is empty "+s.dot()+" F saves", msg.Slot))
		return s, nil
	}
	if s.HaveStation && uuid == s.Station.UUID {
		return s, nil // already playing it
	}
	pick, err := s.core.TuneTo(uuid, time.Now())
	if err != nil {
		s.Note = err.Error()
		return s, nil
	}
	pick.Reason = fmt.Sprintf("preset %d", msg.Slot)
	return s.applyPick(pick)
}

// adjustVolume is the single volume path: clamp 0–100, persist, then tell
// the player. The slider's open/linger is a face concern.
func (s State) adjustVolume(delta int) (State, []Cmd) {
	return s.volumeTo(s.Volume + delta)
}

func (s State) volumeTo(v int) (State, []Cmd) {
	s.Volume = v
	if s.Volume < 0 {
		s.Volume = 0
	}
	if s.Volume > 100 {
		s.Volume = 100
	}
	if err := s.core.SetVolume(s.Volume); err != nil {
		s.Note = "could not save volume " + s.dot() + " " + err.Error()
	}
	return s, []Cmd{s.volumeCmd()}
}

// refreshPresets re-reads the preset map for a face that knows the slots
// changed outside a toggle.
func (s State) refreshPresets() (State, []Cmd) {
	s.Presets = s.core.Presets()
	return s, nil
}

// touch is any sign of listener life; the startup notice shows once.
func (s State) touch() (State, []Cmd) {
	s.Notice = ""
	return s, nil
}

// notify lands face-originated text on the session's note/feedback lines.
func (s State) notify(msg Notify) (State, []Cmd) {
	if msg.Feedback != "" {
		s.setFeedback(msg.Feedback)
	}
	if msg.Note != "" {
		s.Note = msg.Note
	}
	return s, nil
}

// synced absorbs a finished directory refresh: presets may have changed
// under us, and the listener hears the outcome once.
func (s State) synced(msg Synced) (State, []Cmd) {
	s.Syncing = false
	s.Presets = s.core.Presets()
	if msg.Err != nil {
		s.setFeedback("DIRECTORY  Refresh failed; using cache")
	} else {
		s.setFeedback(fmt.Sprintf("DIRECTORY  %d stations refreshed", msg.N))
	}
	return s, nil
}

func (s State) volumeSet(msg VolumeSet) (State, []Cmd) {
	if msg.Err != nil {
		s.Note = "volume control failed " + s.dot() + " " + msg.Err.Error()
	}
	return s, nil
}
