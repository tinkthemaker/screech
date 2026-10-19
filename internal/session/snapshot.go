package session

import "time"

// Snapshot is everything a face needs to draw the now-playing area. It is
// comparable (no slices or maps), so a runner can publish only on change.
type Snapshot struct {
	Phase        string      `json:"phase"` // Phase.String()
	HaveStation  bool        `json:"haveStation"`
	Station      StationCard `json:"station"`
	Reason       string      `json:"reason"`
	Artist       string      `json:"artist"`
	Title        string      `json:"title"`
	HaveTrack    bool        `json:"haveTrack"`
	Loved        bool        `json:"loved"`
	Suspect      bool        `json:"suspect"`
	PlayStartMs  int64       `json:"playStartMs"` // unix ms; 0 until the stream locks
	PausedAtMs   int64       `json:"pausedAtMs"`
	RetryAtMs    int64       `json:"retryAtMs"` // Offline: when the next retry fires
	Reconnecting bool        `json:"reconnecting"`
	Resuming     bool        `json:"resuming"`
	Seeking      bool        `json:"seeking"`
	Syncing      bool        `json:"syncing"`
	Virgin       bool        `json:"virgin"`
	Volume       int         `json:"volume"`
	Slots        [9]bool     `json:"slots"` // Slots[i]: preset i+1 holds a station
	CurrentSlot  int         `json:"currentSlot"`
	SleepAtMs    int64       `json:"sleepAtMs"`
	Feedback     string      `json:"feedback"` // only while Now < FeedbackAt + FeedbackDuration
	Notice       string      `json:"notice"`
	Note         string      `json:"note"`
	Fatal        string      `json:"fatal"`
}

// StationCard is the station fields a face renders, in one comparable
// value.
type StationCard struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Codec   string `json:"codec"`
	Bitrate int    `json:"bitrate"`
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Snapshot flattens the state into the comparable value a face publishes.
// Feedback fades on the same clock the terminal's does.
func (s State) Snapshot() Snapshot {
	snap := Snapshot{
		Phase:        s.Phase.String(),
		HaveStation:  s.HaveStation,
		Station:      StationCard{UUID: s.Station.UUID, Name: s.Station.Name, Country: s.Station.Country, Codec: s.Station.Codec, Bitrate: s.Station.Bitrate},
		Reason:       s.Reason,
		Artist:       s.Artist,
		Title:        s.Title,
		HaveTrack:    s.HaveTrack,
		Loved:        s.LovedTrack,
		Suspect:      s.Suspect,
		PlayStartMs:  ms(s.PlayStart),
		PausedAtMs:   ms(s.PausedAt),
		RetryAtMs:    ms(s.RetryAt),
		Reconnecting: s.Reconnecting,
		Resuming:     s.Resuming,
		Seeking:      s.Seeking,
		Syncing:      s.Syncing,
		Virgin:       s.Virgin,
		Volume:       s.Volume,
		CurrentSlot:  s.CurrentSlot(),
		SleepAtMs:    ms(s.SleepAt),
		Notice:       s.Notice,
		Note:         s.Note,
		Fatal:        s.Fatal,
	}
	for i := range snap.Slots {
		_, ok := s.Presets[i+1]
		snap.Slots[i] = ok
	}
	if !s.FeedbackAt.IsZero() && s.Now.Before(s.FeedbackAt.Add(FeedbackDuration)) {
		snap.Feedback = s.Feedback
	}
	return snap
}
