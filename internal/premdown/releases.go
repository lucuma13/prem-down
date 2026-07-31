// The map of Premiere releases prem-down knows about, and everything that reads
// it: resolving a release name a user typed, naming the release one step below a
// project's own, and the samples the CLI help prints.
//
// This is the file to edit when Adobe ships a new Premiere release: add the
// entry to releases, and every lookup, the "--to" help text and the
// unrecognised-source warning follow from it.

package premdown

import (
	"fmt"
	"strings"
)

// The last release that both wrote and required the dense serialisation.
// Releases above it write the sparse form; releases at or below it refuse to
// open a project that arrives in that form. So it is the boundary in BOTH
// directions, and which side each end of a conversion falls on is what decides
// whether field re-insertion is needed at all - see needsFieldReinsertion.
const lastDenseSerialisationProjectVersion = 43

// Map of Premiere release -> the XML <Project> Version that release uses
// natively.
// Sources for XML versions 23-40:
// https://www.reddit.com/r/premiere/comments/1nbtko2/premiere_pro_project_file_version_map_which/
// https://gist.github.com/mslinn/5d53c4ab21fe2fe6e5b8a66621502320
var releases = []struct {
	xmlProjectVersion int
	name              string
	aliases           []string
}{
	{22, "CS4", nil},                     // Assumed release
	{23, "CS5", nil},                     // Community consensus
	{24, "CS5.5", nil},                   // Community consensus
	{25, "CS6", nil},                     // Community consensus
	{26, "CC", nil},                      // Community consensus
	{27, "2014", []string{"CC2014"}},     // Community consensus
	{28, "2014.1", []string{"CC2014.1"}}, // Community verified
	{29, "2015", []string{"CC2015"}},     // Community consensus
	{30, "2015.1", []string{"CC2015.1"}}, // Community consensus
	{32, "2017", []string{"CC2017"}},     // Community consensus
	{33, "2018", []string{"CC2018"}},     // Community consensus
	{34, "2018.1", []string{"CC2018.1"}}, // Community consensus
	{35, "2019", []string{"CC2019"}},     // Community verified
	{36, "2019.1", []string{"CC2019.1"}}, // Community consensus
	{37, "2020", []string{"CC2020"}},     // Community verified
	{38, "2020.1", []string{"CC2020.1"}}, // Community verified
	{39, "2021", []string{"CC2021"}},     // Community verified
	{40, "2022", nil},
	{41, "2023", nil},
	{42, "2024", nil},
	{43, "2025", nil},
	{45, "2026", nil},
}

// newestKnownProjectVersion is the last entry's: releases is stored
// oldest-first, so the highest XML <Project> version in the map is the newest
// release prem-down knows about. A source above it is from a Premiere this
// build has never been told about.
func newestKnownProjectVersion() int {
	return releases[len(releases)-1].xmlProjectVersion
}

// releaseNames lists the known releases newest-first (releases is stored
// oldest-first).
func releaseNames() string {
	names := make([]string, len(releases))
	for i, r := range releases {
		names[len(releases)-1-i] = r.name
	}
	return strings.Join(names, ", ")
}

// ReleaseExamples gives a short "E.g." sample for help text: the two releases
// just below the latest (releases is stored oldest-first, so the last entry is
// the newest and we skip it), each single-quoted, trailed by "..." to signal
// there are more.
func ReleaseExamples() string {
	var names []string
	for i := len(releases) - 2; i >= 0 && len(names) < 2; i-- {
		names = append(names, "'"+releases[i].name+"'")
	}
	return strings.Join(names, ", ") + "..."
}

// ResolveRelease returns the XML <Project> Version for a release name
// (case-insensitive, aliases accepted), or an error naming the known releases.
func ResolveRelease(name string) (int, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, r := range releases {
		if strings.ToLower(r.name) == want {
			return r.xmlProjectVersion, nil
		}
		for _, a := range r.aliases {
			if strings.ToLower(a) == want {
				return r.xmlProjectVersion, nil
			}
		}
	}
	return 0, fmt.Errorf("unknown release %q. Known releases: %s", name, releaseNames())
}

// previousRelease returns the known release one step below XML <Project>
// Version v: the entry with the highest xmlProjectVersion strictly less than v.
// releases is sorted ascending, so the last match wins. This reads the "N-1
// release" positionally, which means gaps in the map (there is no v31 or v44
// entry) are skipped for free: a v45 (2026) source resolves to v43 (2025), a
// v32 (2017) source to v30 (2015.1).
func previousRelease(v int) (xmlProjectVersion int, name string, ok bool) {
	for _, r := range releases {
		if r.xmlProjectVersion < v {
			xmlProjectVersion, name, ok = r.xmlProjectVersion, r.name, true
		}
	}
	return
}
