package core

// Seed stations: hand-picked, low ad-risk, stable streams. They cover first-run
// when radio-browser is unreachable and give cold start a decent floor.
// UUIDs are screech-local (seed: prefix), so a later radio-browser sync can
// coexist without colliding.
var seedStations = []Station{
	{
		UUID: "seed:somafm-groovesalad", Name: "SomaFM: Groove Salad",
		URL: "https://ice1.somafm.com/groovesalad-128-mp3", URLResolved: "https://ice1.somafm.com/groovesalad-128-mp3",
		Homepage: "https://somafm.com/groovesalad/", Tags: "ambient,downtempo,chillout,electronic",
		Country: "US", Codec: "MP3", Bitrate: 128, Votes: 1000, LastCheckOK: true, AdRisk: 0.05,
	},
	{
		UUID: "seed:somafm-spacestation", Name: "SomaFM: Space Station Soma",
		URL: "https://ice1.somafm.com/spacestation-128-mp3", URLResolved: "https://ice1.somafm.com/spacestation-128-mp3",
		Homepage: "https://somafm.com/spacestation/", Tags: "ambient,space,electronic,experimental",
		Country: "US", Codec: "MP3", Bitrate: 128, Votes: 900, LastCheckOK: true, AdRisk: 0.05,
	},
	{
		UUID: "seed:somafm-dronezone", Name: "SomaFM: Drone Zone",
		URL: "https://ice1.somafm.com/dronezone-128-mp3", URLResolved: "https://ice1.somafm.com/dronezone-128-mp3",
		Homepage: "https://somafm.com/dronezone/", Tags: "ambient,drone,atmospheric,space",
		Country: "US", Codec: "MP3", Bitrate: 128, Votes: 900, LastCheckOK: true, AdRisk: 0.05,
	},
	{
		UUID: "seed:somafm-defcon", Name: "SomaFM: DEF CON Radio",
		URL: "https://ice1.somafm.com/defcon-128-mp3", URLResolved: "https://ice1.somafm.com/defcon-128-mp3",
		Homepage: "https://somafm.com/defcon/", Tags: "electronic,techno,idm,hacker",
		Country: "US", Codec: "MP3", Bitrate: 128, Votes: 800, LastCheckOK: true, AdRisk: 0.05,
	},
	{
		UUID: "seed:radioparadise-main", Name: "Radio Paradise (Main Mix)",
		URL: "https://stream.radioparadise.com/aac-128", URLResolved: "https://stream.radioparadise.com/aac-128",
		Homepage: "https://radioparadise.com/", Tags: "eclectic,rock,world,electronic,listener-supported",
		Country: "US", Codec: "AAC", Bitrate: 128, Votes: 1200, LastCheckOK: true, AdRisk: 0.05,
	},
	{
		UUID: "seed:radioparadise-mellow", Name: "Radio Paradise Mellow Mix",
		URL: "https://stream.radioparadise.com/mellow-128", URLResolved: "https://stream.radioparadise.com/mellow-128",
		Homepage: "https://radioparadise.com/", Tags: "mellow,eclectic,chillout,listener-supported",
		Country: "US", Codec: "AAC", Bitrate: 128, Votes: 1000, LastCheckOK: true, AdRisk: 0.05,
	},
	{
		UUID: "seed:wfmu", Name: "WFMU",
		URL: "https://stream0.wfmu.org/freeform-128k", URLResolved: "https://stream0.wfmu.org/freeform-128k",
		Homepage: "https://wfmu.org/", Tags: "freeform,eclectic,independent,community",
		Country: "US", Codec: "MP3", Bitrate: 128, Votes: 800, LastCheckOK: true, AdRisk: 0.05,
	},
	{
		UUID: "seed:kexp", Name: "KEXP Seattle",
		URL: "https://kexp-mp3-128.streamguys1.com/kexp128.mp3", URLResolved: "https://kexp-mp3-128.streamguys1.com/kexp128.mp3",
		Homepage: "https://kexp.org/", Tags: "indie,alternative,eclectic,listener-supported",
		Country: "US", Codec: "MP3", Bitrate: 128, Votes: 1100, LastCheckOK: true, AdRisk: 0.08,
	},
	{
		UUID: "seed:nts1", Name: "NTS Radio 1",
		URL: "https://stream-relay-geo.ntslive.net/stream", URLResolved: "https://stream-relay-geo.ntslive.net/stream",
		Homepage: "https://www.nts.live/", Tags: "eclectic,experimental,electronic,underground",
		Country: "GB", Codec: "MP3", Bitrate: 128, Votes: 900, LastCheckOK: true, AdRisk: 0.08,
	},
	{
		UUID: "seed:wwoz", Name: "WWOZ New Orleans",
		URL: "https://wwoz-sc.streamguys1.com/wwoz-hi.mp3", URLResolved: "https://wwoz-sc.streamguys1.com/wwoz-hi.mp3",
		Homepage: "https://www.wwoz.org/", Tags: "jazz,blues,roots,community,listener-supported",
		Country: "US", Codec: "MP3", Bitrate: 128, Votes: 850, LastCheckOK: true, AdRisk: 0.05,
	},
}

// SeedStations returns a copy of the embedded seed list.
func SeedStations() []Station {
	out := make([]Station, len(seedStations))
	copy(out, seedStations)
	return out
}
