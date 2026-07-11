// Package main implements prem-down, which downgrades an Adobe Premiere Pro
// project file so an older version of Premiere can open it.
//
// Operation runs completely **offline and local** to your machine – no data is
// ever uploaded to the internet.
//
// It fully supports the breaking changes introduced with **Premiere Pro 2026**.
// The well-known method (gunzip the `.prproj`, lower the top-level project
// version, re-gzip) no longer works reliably on Premiere 2026 files. The cause
// is that 2026 uses sparser serialisation — it drops fields that older releases
// expect present (and report the project as damaged if they are absent). So the
// fix is bifold: re-insert those required fields, and set the project version
// to the target release.
//
// Usage example:
//
//	```
//	prem-down myproject.prproj
//	prem-down a.prproj b.prproj c.prproj   # batch: each file downgraded independently
//	```
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// prem-down version; overridden at build time via
// -ldflags "-X main.version=1.2.3"
var version = "0.1"

const (
	minConvertibleProjectVersion         = 22 // Premiere Pro 2025 converter floor
	lastDenseSerialisationProjectVersion = 43 // sources above it need field re-insertion
)

// Map of Premiere release -> the XML <Project> Version that release uses
// natively.
// Source for XML versions 23-40:
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

// Fields that 2026 drops but that Premiere 2025 REQUIRES to be present for a
// 2026 project to open at all. 2025 reads these by name (field order is
// irrelevant); it is their *absence* that makes 2025 report the project as
// damaged, so we re-insert them.
//
// This set is deliberately narrow and was established by ablation. Only
// VideoComponentParam's LowerBound and UpperBound turned out to be required —
// removing either crashes 2025. Every other field 2026 omits was verified
// presence-optional: all IsTimeVarying, every ParameterControlType, the
// VideoTransition and AudioTransition defaults, and the TimeComponentParam
// booleans.
//
// reconstructFields lists the required fields per class; reconstructDefaults
// gives the value to insert. Order within each list only affects the order
// appended (cosmetic).
var reconstructFields = []struct {
	tag    string
	fields []string
}{
	{"VideoComponentParam", []string{"LowerBound", "UpperBound"}},
}

var reconstructFieldsByTag = func() map[string][]string {
	m := make(map[string][]string, len(reconstructFields))
	for _, c := range reconstructFields {
		m[c.tag] = c.fields
	}
	return m
}()

type fieldKey struct{ tag, field string }

// The values inserted for the required fields.
//
// These are NOT the parameter's real bounds. The true bounds vary per parameter
// and 2026 has already discarded them; no fixed constant could reproduce them.
// false/true are sentinels meaning "no explicit override": on load, 2025 reads
// them and repopulates each parameter's real default bound from its effect
// definition. This was verified by round-tripping two independent projects
// through 2025 (downgrade -> open -> re-save) and diffing the re-saved bounds
// against natively-saved 2025 output — they match exactly.
//
// The specific tokens are load-bearing: do NOT "sanitise" them to 0 or any
// number. 2025 treats a numeric value as an explicit override and keeps it,
// silently corrupting the parameter's range; only false/true trigger
// repopulation.
var reconstructDefaults = map[fieldKey]string{
	{"VideoComponentParam", "LowerBound"}: "false",
	{"VideoComponentParam", "UpperBound"}: "true",
}

type classField struct{ classID, field string }

// Per-ClassID overrides for the value inserted, consulted before
// reconstructDefaults. Needed for the rare parameter class whose real bound is
// NOT one 2025 recomputes from the false/true sentinel — there the sentinel
// would round-trip to the wrong value, so we insert the literal instead.
//
// The only known case is the Lumetri Color selector param class, whose
// UpperBound is the "unbounded" marker (2^64-1). Inserting true there makes
// 2025 collapse it to 1; inserting the literal marker is preserved by 2025 and
// the project still opens (both verified by round-trip). The color value itself
// lives in the keyframes and is unaffected either way — this only restores the
// declared range. ClassID identifies the parameter's type and is stable across
// projects.
var reconstructClassOverrides = map[classField]string{
	{"0fde4e9f-f895-4ba3-b0fe-9a6feafda583", "UpperBound"}: "18446744073709551615",
}

var classIDRe = regexp.MustCompile(`\bClassID="([^"]+)"`)

// guiMode is set by --gui, passed by the OS context-menu wiring (see
// integrate.go): the shell opens a console window that closes the instant the
// process exits, so wait for Enter before exiting to keep the result
// readable. Not shown in --help; it is plumbing, not a user-facing option.
var guiMode bool

func pauseIfGUI() {
	if !guiMode {
		return
	}
	fmt.Fprint(os.Stderr, "\nPress Enter to close this window...")
	_, _ = bufio.NewReader(os.Stdin).ReadBytes('\n')
}

// osExit is the process-exit seam used by fatal (and main). Tests replace it so
// a fatal path can be exercised in-process — aborting the caller via panic —
// instead of killing the test binary.
var osExit = os.Exit

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	pauseIfGUI()
	osExit(1)
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

// releaseExamples gives a short "E.g." sample for help text: the three releases
// just below the latest (releases is stored oldest-first, so the last entry is
// the newest and we skip it), each single-quoted, trailed by "..." to signal
// there are more.
func releaseExamples() string {
	var names []string
	for i := len(releases) - 2; i >= 0 && len(names) < 3; i-- {
		names = append(names, "'"+releases[i].name+"'")
	}
	return strings.Join(names, ", ") + "..."
}

// resolveRelease returns the XML <Project> Version for a release name
// (case-insensitive, aliases accepted).
func resolveRelease(name string) int {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, r := range releases {
		if strings.ToLower(r.name) == want {
			return r.xmlProjectVersion
		}
		for _, a := range r.aliases {
			if strings.ToLower(a) == want {
				return r.xmlProjectVersion
			}
		}
	}
	fatal("error: unknown release %q. Known releases: %s", name, releaseNames())
	return 0
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

// warnTarget prints the same downgrade caveats whether the target was chosen
// explicitly (--to) or derived as the previous release.
func warnTarget(version int) {
	if version < minConvertibleProjectVersion {
		fmt.Fprintf(os.Stderr,
			"warning: project version %d is below converter floor (%d); "+
				"newer Premiere may report it as damaged (it should still open in its own "+
				"native release).\n", version, minConvertibleProjectVersion)
	}
}

// --------------------------------------------------------------------------
// Formatting-preserving mini-DOM, used only to rebuild the reconstruct
// classes. Each element keeps its exact opening tag and a content list (raw
// text + child elements), so re-serialising an untouched node is
// byte-identical to the input.
// --------------------------------------------------------------------------

type el struct {
	tag         string
	openTag     string
	selfClosing bool
	content     []any // string or *el
	closeTag    string
}

func (e *el) children() []*el {
	var out []*el
	for _, c := range e.content {
		if ce, ok := c.(*el); ok {
			out = append(out, ce)
		}
	}
	return out
}

func (e *el) render(b *strings.Builder) {
	if e.selfClosing {
		b.WriteString(e.openTag)
		return
	}
	b.WriteString(e.openTag)
	for _, c := range e.content {
		switch v := c.(type) {
		case string:
			b.WriteString(v)
		case *el:
			v.render(b)
		}
	}
	b.WriteString(e.closeTag)
}

var (
	tagRe     = regexp.MustCompile(`<[^>]+>`)
	tagNameRe = regexp.MustCompile(`^<([\w.\-]+)`)
)

func parseXML(s string) []any {
	var roots []any
	var stack []*el
	appendItem := func(item any) {
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			top.content = append(top.content, item)
		} else {
			roots = append(roots, item)
		}
	}
	pos := 0
	for _, loc := range tagRe.FindAllStringIndex(s, -1) {
		if loc[0] > pos {
			appendItem(s[pos:loc[0]])
		}
		tok := s[loc[0]:loc[1]]
		pos = loc[1]
		switch {
		case strings.HasPrefix(tok, "<?") || strings.HasPrefix(tok, "<!"):
			appendItem(tok)
		case strings.HasPrefix(tok, "</"):
			if len(stack) == 0 {
				fatal("error: unbalanced XML near %q", tok)
			}
			top := stack[len(stack)-1]
			if name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tok, "</"), ">")); name != top.tag {
				fatal("error: mismatched XML: %q closes <%s>", tok, top.tag)
			}
			stack = stack[:len(stack)-1]
			top.closeTag = tok
		default:
			m := tagNameRe.FindStringSubmatch(tok)
			if m == nil {
				appendItem(tok)
				continue
			}
			e := &el{tag: m[1], openTag: tok, selfClosing: strings.HasSuffix(tok, "/>")}
			appendItem(e)
			if !e.selfClosing {
				stack = append(stack, e)
			}
		}
	}
	if pos < len(s) {
		appendItem(s[pos:])
	}
	if len(stack) > 0 {
		fatal("error: unbalanced XML: <%s> never closed", stack[len(stack)-1].tag)
	}
	return roots
}

func leaf(tag, value string) *el {
	return &el{
		tag:      tag,
		openTag:  "<" + tag + ">",
		content:  []any{value},
		closeTag: "</" + tag + ">",
	}
}

// rebuild re-inserts the default fields 2026 dropped, so a reconstruct class
// has every field 2025 requires present. Recurses into children first
// (depth-first).
//
// Order is deliberately NOT changed: these classes are read by name, so field
// order is irrelevant. Existing fields are left exactly where Premiere wrote
// them; we only append the handful of missing leaves before the closing tag.
func rebuild(e *el, stats map[fieldKey]int) {
	for _, child := range e.children() {
		rebuild(child, stats)
	}
	if e.selfClosing {
		return
	}
	fields, ok := reconstructFieldsByTag[e.tag]
	if !ok {
		return
	}

	presentEls := map[string]*el{}
	for _, c := range e.children() {
		presentEls[c.tag] = c
	}

	classID := ""
	if m := classIDRe.FindStringSubmatch(e.openTag); m != nil {
		classID = m[1]
	}

	sep := "\n"
	bestLen := 0
	for _, c := range e.content {
		if s, isStr := c.(string); isStr && strings.TrimSpace(s) == "" &&
			strings.Contains(s, "\n") && len(s) > bestLen {
			sep, bestLen = s, len(s)
		}
	}

	var missing []*el
	for _, field := range fields {
		if _, has := presentEls[field]; has {
			continue
		}
		value, hasDefault := reconstructDefaults[fieldKey{e.tag, field}]
		if v, ok := reconstructClassOverrides[classField{classID, field}]; ok {
			value, hasDefault = v, true
		}
		if !hasDefault {
			continue
		}
		missing = append(missing, leaf(field, value))
		stats[fieldKey{e.tag, field}]++
	}

	if len(missing) == 0 {
		return // leave content untouched -> byte-identical to what Premiere wrote
	}

	content := e.content
	trailing := ""
	if len(content) > 0 {
		if s, isStr := content[len(content)-1].(string); isStr && strings.TrimSpace(s) == "" {
			trailing = s
			content = content[:len(content)-1]
		}
	}
	for _, l := range missing {
		content = append(content, sep, l)
	}
	if trailing != "" {
		content = append(content, trailing)
	}
	e.content = content
}

// reconstructPositionalClasses rebuilds each reconstruct class so every field
// required is present. Operates on one class instance at a time (they
// don't self-nest) to avoid parsing the whole multi-hundred-MB document at
// once.
func reconstructPositionalClasses(xml string) (string, map[fieldKey]int) {
	stats := map[fieldKey]int{}
	for _, c := range reconstructFields {
		// The Python original uses <tag(?=[ >]); RE2 has no lookahead, but a
		// definition tag always carries attributes (Version= at minimum), so a
		// whitespace char after the name is equivalent.
		re := regexp.MustCompile(`(?s)<` + c.tag + `[ \t\r\n][^>]*\bVersion="\d+"[^>]*>.*?</` + c.tag + `>`)
		xml = re.ReplaceAllStringFunc(xml, func(m string) string {
			roots := parseXML(m)
			var b strings.Builder
			b.Grow(len(m) + 256)
			for _, r := range roots {
				switch v := r.(type) {
				case string:
					b.WriteString(v)
				case *el:
					rebuild(v, stats)
					v.render(&b)
				}
			}
			return b.String()
		})
	}
	return xml, stats
}

var projectVersionRe = regexp.MustCompile(`(<Project ObjectID="1"[^>]*\bVersion=")(\d+)(")`)

func setProjectVersion(xml string, version int) string {
	n := len(projectVersionRe.FindAllStringIndex(xml, -1))
	if n != 1 {
		fatal(`error: expected exactly one <Project ObjectID="1"> tag, found %d`, n)
	}
	return projectVersionRe.ReplaceAllString(xml, fmt.Sprintf("${1}%d${3}", version))
}

func getProjectVersion(xml string) int {
	m := projectVersionRe.FindStringSubmatch(xml)
	if m == nil {
		fatal(`error: could not find the <Project ObjectID="1"> version`)
	}
	v, err := strconv.Atoi(m[2])
	if err != nil {
		fatal("error: invalid <Project> version %q", m[2])
	}
	return v
}

// uniquePath returns path if free, else the same name with a -1/-2/-3...
// suffix. Only a successful Stat counts as taken: any Stat error (not just
// not-exist) treats the path as free, so an unreadable directory surfaces as
// a write error later instead of looping here forever.
func uniquePath(path string) string {
	taken := func(p string) bool {
		_, err := os.Stat(p) //nolint:gosec // G703: p derives from a user-supplied CLI path; stat-ing it is the tool's purpose
		return err == nil
	}
	if !taken(path) {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		if !taken(candidate) {
			return candidate
		}
	}
}

// downgrade converts one project file and returns an error rather than exiting,
// so a caller processing several files can report a failure and move on to the
// rest. The returned errors are operational (unreadable/unrecognised file,
// out-of-range target, write failure); genuinely malformed XML still fails hard
// inside the structural helpers (getProjectVersion, parseXML), since that means
// a corrupt input rather than an ordinary skip.
func downgrade(src, dst string, projectVersion int, verbose bool) error {
	raw, err := os.ReadFile(src) //nolint:gosec // G304: src is the user-supplied input path; reading it is the tool's purpose
	if err != nil {
		return err
	}
	var xml string
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		data, err := io.ReadAll(zr)
		if err != nil {
			return err
		}
		xml = string(data)
	} else {
		xml = string(raw)
	}
	if !strings.Contains(xml, "<PremiereData") {
		return fmt.Errorf("does not look like a Premiere project")
	}

	sourceVersion := getProjectVersion(xml)

	// This is a downgrader: an explicit --to at or above the source release is
	// almost certainly user error, so refuse rather than stamp a higher version.
	if projectVersion >= sourceVersion {
		return fmt.Errorf("target version %d is not below the source version %d; "+
			"--to must name an older release", projectVersion, sourceVersion)
	}

	// projectVersion == 0 means "auto": target the release one step below the
	// source (the default when no --to is given).
	if projectVersion == 0 {
		pv, name, ok := previousRelease(sourceVersion)
		if !ok {
			return fmt.Errorf("source is version %d; no known earlier release to "+
				"downgrade to (use --to to force one)", sourceVersion)
		}
		projectVersion = pv
		if verbose {
			fmt.Printf("  auto target: source version %d -> %s (version %d)\n",
				sourceVersion, name, pv)
		}
		warnTarget(projectVersion)
	}

	needsNormalize := sourceVersion > lastDenseSerialisationProjectVersion
	stats := map[fieldKey]int{}
	if needsNormalize {
		xml, stats = reconstructPositionalClasses(xml)
	}
	xml = setProjectVersion(xml, projectVersion)
	if verbose {
		if needsNormalize {
			keys := make([]fieldKey, 0, len(stats))
			for k := range stats {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				if keys[i].tag != keys[j].tag {
					return keys[i].tag < keys[j].tag
				}
				return keys[i].field < keys[j].field
			})
			for _, k := range keys {
				fmt.Printf("  inserted %s/%s (%dx)\n", k.tag, k.field, stats[k])
			}
		} else {
			fmt.Printf("  source is version %d (<= %d); class formats already compatible, "+
				"only re-gating <Project> version\n", sourceVersion, lastDenseSerialisationProjectVersion)
		}
		fmt.Printf("  set Project version -> %d\n", projectVersion)
	}

	var out bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if _, err := zw.Write([]byte(xml)); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(dst, out.Bytes(), 0o644); err != nil { //nolint:gosec // G306: output is a project file meant to be opened/shared; 0644 is deliberate
		return err
	}
	fmt.Printf("wrote %s\n", dst)
	return nil
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: prem-down input.prproj [input2.prproj ...] [--to RELEASE]
       prem-down integrate [--remove]

Downgrade one or more Premiere Pro projects to open with an older version, each
saved next to its original project.

Options:
  --to RELEASE    target Premiere release (e.g. %s default: one version older).
  -v, --verbose   print what was changed
      --version   show version and exit
  -h, --help      show this help

Subcommands:
  integrate       add a right-click "Downgrade for older Premiere" action to
                  %s (--remove undoes it)
`, releaseExamples(), fileManagerName)
}

func main() {
	osExit(run(os.Args[1:]))
}

// run holds main's logic, split out so it can be tested: it returns the process
// exit code instead of calling os.Exit, and user-error paths still abort through
// fatal (which calls the osExit seam). main is then a one-line shim.
func run(args []string) int {
	// When Explorer activates prem-down as the Drop Target COM server (Windows
	// only; "-Embedding"), it takes over completely: it collects the selected
	// files and relaunches prem-down --gui on them. See droptarget_windows.go.
	if maybeRunCOMServer(args) {
		return 0
	}
	if len(args) > 0 && args[0] == "integrate" {
		integrateMain(args[1:])
		return 0
	}

	var positionals []string
	to := "" // empty => auto: one release below the source
	verbose := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			usage(os.Stdout)
			return 0
		case a == "--version":
			fmt.Printf("prem-down %s\n", version)
			return 0
		case a == "--to":
			i++
			if i >= len(args) {
				fatal("error: --to requires a value")
			}
			to = args[i]
		case strings.HasPrefix(a, "--to="):
			to = strings.TrimPrefix(a, "--to=")
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "--gui":
			guiMode = true
		case strings.HasPrefix(a, "-") && a != "-":
			usage(os.Stderr)
			fatal("error: unknown option %s", a)
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) == 0 {
		usage(os.Stderr)
		return 2
	}

	// Explicit --to is resolved and validated up front; auto (empty) is deferred
	// to downgrade, which needs each source's version to pick the previous
	// release. Resolving once here also means a bad --to fails before any file is
	// touched.
	targetVersion := 0
	if to != "" {
		targetVersion = resolveRelease(to)
		warnTarget(targetVersion)
	}

	// Each file is converted independently: a failure on one is reported and the
	// rest still run, so a batch (a multi-file selection from the context menu, or
	// a shell glob) isn't aborted by a single bad input. Exit non-zero if any
	// failed.
	failed := false
	for _, input := range positionals {
		if _, err := os.Stat(input); err != nil { //nolint:gosec // G703: input is the user-supplied CLI path; stat-ing it is the tool's purpose
			fmt.Fprintf(os.Stderr, "error: %s not found\n", input)
			failed = true
			continue
		}
		ext := filepath.Ext(input)
		dst := uniquePath(strings.TrimSuffix(input, ext) + "_downgraded.prproj")
		if err := downgrade(input, dst, targetVersion, verbose); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", input, err)
			failed = true
		}
	}
	pauseIfGUI()
	if failed {
		return 1
	}
	return 0
}
