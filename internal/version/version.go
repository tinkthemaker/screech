// Package version holds screech's one version string.
//
// It lives in its own package because three separate places need it and
// they must never drift: the `screech version` output, the radio-browser
// User-Agent (their etiquette asks for a real one), and the User-Agent mpv
// sends upstream. The release workflow overwrites Current with the tag via
// -ldflags "-X screech/internal/version.Current=...", which only reaches
// all three if all three read from here.
package version

// Current is the running build's version. Overwritten at release time.
var Current = "0.6.1"

// UserAgent is what screech calls itself to other people's servers.
func UserAgent() string { return "screech/" + Current }
