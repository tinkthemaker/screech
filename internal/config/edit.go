package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/tinkthemaker/screech/internal/core"
)

// AddStation appends st to config.toml as a [[stations]] table and returns
// the file's path and the station as written. It creates the file with its
// defaults if there isn't one yet and refuses a link that's already there.
// The rest of the file (comments, order, spacing) is left byte for byte:
// the TOML is edited as text, never re-encoded. A running Screech reads
// the change on its next launch.
func AddStation(st Station) (string, Station, error) { return addStationIn(configDir(), st) }

// RemoveStation deletes the [[stations]] table whose link or custom: id
// matches key, with the comment lines directly above it, and returns the
// file's path and the station removed.
func RemoveStation(key string) (string, Station, error) { return removeStationIn(configDir(), key) }

func addStationIn(dir string, st Station) (string, Station, error) {
	path := filepath.Join(dir, "config.toml")
	st, err := st.clean()
	if err != nil {
		return path, st, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return path, st, err
		}
		data, err = []byte(defaultFile), nil
	}
	if err != nil {
		return path, st, err
	}
	before, err := decode(data)
	if err != nil {
		return path, st, fmt.Errorf("%s is not valid TOML, so it was left alone: %w", path, err)
	}
	for _, have := range before.Stations {
		if have.ID() == st.ID() {
			return path, st, fmt.Errorf("%s is already in %s as %q", st.URL, path, have.Name)
		}
	}
	block, err := encodeStation(st)
	if err != nil {
		return path, st, err
	}
	text := string(data)
	nl := lineEnding(text)
	block = strings.ReplaceAll(block, "\n", nl)
	switch {
	case text == "":
		text = block
	case strings.HasSuffix(text, "\n"):
		text += nl + block
	default:
		text += nl + nl + block
	}
	want := ids(before.Stations)
	want[st.ID()] = true
	if err := checkEdit(before, text, want); err != nil {
		return path, st, err
	}
	return path, st, writeAtomic(path, text)
}

func removeStationIn(dir, key string) (string, Station, error) {
	path := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, Station{}, fmt.Errorf("there is no %s yet, so no stations to remove", path)
		}
		return path, Station{}, err
	}
	before, err := decode(data)
	if err != nil {
		return path, Station{}, fmt.Errorf("%s is not valid TOML, so it was left alone: %w", path, err)
	}
	key = strings.TrimSpace(key)
	id := key
	if !core.IsCustom(key) {
		id = core.CustomUUID(core.CleanText(key))
	}

	text := string(data)
	lines := strings.SplitAfter(text, "\n")
	var gone []span
	var removed Station
	for _, sp := range stationBlocks(lines) {
		st, ok := blockStation(strings.Join(lines[sp.header:sp.end], ""))
		if !ok || core.CustomUUID(core.CleanText(st.URL)) != id {
			continue
		}
		if len(gone) == 0 {
			removed = st
		}
		gone = append(gone, sp)
	}
	if len(gone) == 0 {
		for _, have := range before.Stations {
			if have.ID() == id {
				return path, have, fmt.Errorf("%q is in %s, but not as a [[stations]] table; remove it there by hand", have.Name, path)
			}
		}
		return path, Station{}, fmt.Errorf("no station in %s matches %q", path, key)
	}
	// Later spans first, so earlier indexes stay valid.
	for i := len(gone) - 1; i >= 0; i-- {
		lines = append(lines[:gone[i].start], lines[gone[i].end:]...)
	}
	text = strings.Join(lines, "")
	want := ids(before.Stations)
	delete(want, id)
	if err := checkEdit(before, text, want); err != nil {
		return path, removed, err
	}
	removed.Name = core.CleanText(removed.Name)
	removed.URL = core.CleanText(removed.URL)
	return path, removed, writeAtomic(path, text)
}

// encodeStation renders one [[stations]] table through the TOML encoder,
// so quoting and escaping are the library's, not ours.
func encodeStation(st Station) (string, error) {
	var b strings.Builder
	enc := toml.NewEncoder(&b)
	enc.Indent = ""
	err := enc.Encode(struct {
		Stations []Station `toml:"stations"`
	}{[]Station{st}})
	return strings.TrimLeft(b.String(), "\n"), err
}

// lineEnding keeps a file Notepad last saved in CRLF from growing LF lines.
func lineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// span is one [[stations]] table in a file's lines: lines[start:end] is
// what removing it deletes, and header is the [[stations]] line itself.
type span struct{ start, header, end int }

var stationsHeader = regexp.MustCompile(`^\[\[\s*stations\s*\]\]\s*(#.*)?$`)

// stationBlocks finds the [[stations]] tables in a file split by
// strings.SplitAfter. A table runs to the next table header; comment lines
// directly above a header describe that table, so they belong to it, and
// so does one blank line before them (the gap AddStation writes).
func stationBlocks(lines []string) []span {
	var out []span
	for h, l := range lines {
		if !stationsHeader.MatchString(strings.TrimSpace(l)) {
			continue
		}
		end := h + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
			end++
		}
		for end > h+1 && (isBlank(lines[end-1]) || isComment(lines[end-1])) {
			end--
		}
		start := h
		for start > 0 && isComment(lines[start-1]) {
			start--
		}
		if start > 0 && isBlank(lines[start-1]) {
			start--
		}
		out = append(out, span{start, h, end})
	}
	return out
}

func isBlank(l string) bool   { return strings.TrimSpace(l) == "" }
func isComment(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }

// blockStation reads a lone [[stations]] table's name and url without
// judging them, so an entry the loader skips can still be removed.
func blockStation(block string) (Station, bool) {
	var raw struct {
		Stations []map[string]any `toml:"stations"`
	}
	if _, err := toml.Decode(block, &raw); err != nil || len(raw.Stations) != 1 {
		return Station{}, false
	}
	name, _ := raw.Stations[0]["name"].(string)
	link, ok := raw.Stations[0]["url"].(string)
	return Station{Name: name, URL: link}, ok
}

func ids(sts []Station) map[string]bool {
	out := make(map[string]bool, len(sts))
	for _, st := range sts {
		out[st.ID()] = true
	}
	return out
}

var errEditCheck = errors.New("the edit did not read back as intended, so the file was left alone")

// checkEdit re-reads the edited text before it replaces the file: it must
// still be valid TOML, carry exactly the stations wanted, and leave every
// other setting as it was. Editing TOML as text is what keeps the
// listener's comments; this is what keeps it honest.
func checkEdit(before Config, text string, want map[string]bool) error {
	after, err := decode([]byte(text))
	if err != nil {
		return fmt.Errorf("%w: %v", errEditCheck, err)
	}
	if !reflect.DeepEqual(ids(after.Stations), want) {
		return errEditCheck
	}
	before.Stations, before.Warnings = nil, nil
	after.Stations, after.Warnings = nil, nil
	if !reflect.DeepEqual(before, after) {
		return errEditCheck
	}
	return nil
}

// writeAtomic replaces path through a temp file in the same directory, so
// a crash mid-write leaves the old config rather than half a new one.
func writeAtomic(path, text string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
