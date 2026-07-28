// Package premdown downgrades Adobe Premiere Pro projects and productions so an
// older release of Premiere can open them.
//
// A downgrade is two things: stamping the target release into the document, and
// — for 2026 sources — re-inserting the fields 2026's sparser serialisation
// drops but older releases require present (see reconstruct.go).
//
// The second half is what the well-known trick (gunzip the .prproj, lower the
// top-level project version, re-gzip) misses, and why it stopped working with
// Premiere Pro 2026: 2026 omits fields that earlier releases expect to be
// present, and a project missing them is reported as damaged rather than
// opened. Stamping the version alone is therefore not enough.
//
// The package is CLI-agnostic: it takes paths and io.Writers, never touches
// os.Args or the process streams, and returns every failure as an error so a
// caller processing a batch can report one bad file and continue.
//
// This file holds the Downgrader type, <Project> version stamping, the file IO
// helpers, and Downgrade itself. The release map it resolves against lives in
// releases.go.
package premdown

import (
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

// Downgrader converts project files and Productions.
//
// Out receives progress and the detail printed under verbose; Err receives the
// per-file diagnostics a Production walk emits when one file fails and the rest
// continue. They are injected rather than taken from os.Stdout/os.Stderr so the
// engine never reaches for process globals — the CLI wires the real streams in,
// and a test can read back exactly what was written.
//
// A nil writer discards, so the zero Downgrader is usable and silent, which is
// what tests asserting on written files rather than printed output want.
type Downgrader struct {
	Out io.Writer
	Err io.Writer

	// unrecognised records that a source carried a <Project> version above the
	// newest release in the version map, and doubles as the warn-once latch.
	// One Downgrader is one warning.
	unrecognised bool
}

func (d *Downgrader) out() io.Writer {
	if d.Out == nil {
		return io.Discard
	}
	return d.Out
}

func (d *Downgrader) errw() io.Writer {
	if d.Err == nil {
		return io.Discard
	}
	return d.Err
}

// SawUnrecognisedRelease reports whether anything this Downgrader converted
// came from a Premiere release newer than any in the version map. Exported for
// the CLI, which turns it into the one thing this package cannot know: whether
// a newer prem-down exists that would recognise it.
func (d *Downgrader) SawUnrecognisedRelease() bool { return d.unrecognised }

// projectVersionRe matches the top-level project tag. It bakes in Premiere's
// stable attribute order: ObjectID="1" is written before Version= in the
// <Project> tag (true of every release in the version map). If Adobe ever
// reordered those attributes this would stop matching and the file would be
// reported as unrecognised — a hard refusal, never a silently incorrect rewrite.
var projectVersionRe = regexp.MustCompile(`(<Project ObjectID="1"[^>]*\bVersion=")(\d+)(")`)

func setProjectVersion(xml string, version int) (string, error) {
	n := len(projectVersionRe.FindAllStringIndex(xml, -1))
	if n != 1 {
		return "", fmt.Errorf(`expected exactly one <Project ObjectID="1"> tag, found %d`, n)
	}
	return projectVersionRe.ReplaceAllString(xml, fmt.Sprintf("${1}%d${3}", version)), nil
}

func getProjectVersion(xml string) (int, error) {
	m := projectVersionRe.FindStringSubmatch(xml)
	if m == nil {
		return 0, fmt.Errorf(`could not find the <Project ObjectID="1"> version`)
	}
	v, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, fmt.Errorf("invalid <Project> version %q", m[2])
	}
	return v, nil
}

// uniqueName returns stem+ext if that path is free, else the same name with a
// -1/-2/-3... suffix inserted before the extension. Only a successful Stat
// counts as taken: any Stat error (not just not-exist) treats the path as free,
// so an unreadable directory surfaces as a write error later instead of looping
// here forever. This check is advisory — the O_EXCL open in writeNew (and the
// exclusive os.Mkdir for a Production's output folder) is what actually
// guarantees nothing existing is overwritten if something claims the name in
// between.
func uniqueName(stem, ext string) string {
	taken := func(p string) bool {
		_, err := os.Stat(p) //nolint:gosec // G703: p derives from a user-supplied CLI path; stat-ing it is the tool's purpose
		return err == nil
	}
	if !taken(stem + ext) {
		return stem + ext
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		if !taken(candidate) {
			return candidate
		}
	}
}

// UniquePath is uniqueName for a file path.
func UniquePath(path string) string {
	ext := filepath.Ext(path)
	return uniqueName(strings.TrimSuffix(path, ext), ext)
}

// UniqueDir is uniqueName for a directory path.
func UniqueDir(path string) string {
	return uniqueName(path, "")
}

// readMaybeGzip reads a project file, transparently decompressing it when it
// carries the gzip magic. A .prproj is always gzipped and a .prodset never is,
// but sniffing the bytes rather than trusting the extension means either form
// is accepted for either file.
func readMaybeGzip(src string) ([]byte, error) {
	raw, err := os.ReadFile(src) //nolint:gosec // G304: src is the user-supplied input path; reading it is the tool's purpose
	if err != nil {
		return nil, err
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// writeNew creates dst and writes data to it, refusing to touch an existing
// file.
//
// O_EXCL: the caller picked a free name, but something else may have claimed it
// since; fail with "file exists" rather than overwrite it. Because O_EXCL means
// we created dst, it holds nothing but our own partial output, so on any
// failure we remove it rather than leave a truncated project sitting next to
// the original where it could be opened by mistake.
func writeNew(dst string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) //nolint:gosec // G302,G304: dst sits next to the user-supplied input; a project file is meant to be opened and shared, so 0644 is deliberate
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(dst) //nolint:gosec // G703: dst is the O_EXCL path we just created above; removing our own partial output
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dst) //nolint:gosec // G703: dst is the O_EXCL path we just created above; removing our own partial output
		return err
	}
	return nil
}

// notOlderError reports that the requested target is not below the source's own
// version. On a lone file this is user error and the message says so. Inside a
// Production it is routine — the target is resolved once from the .prodset and
// applied to every project, so a project already at or below it simply needs
// copying through — which is why the caller has to be able to tell this apart
// from a real failure.
type notOlderError struct{ target, source int }

func (e *notOlderError) Error() string {
	return fmt.Sprintf("target version %d is not below the source version %d; "+
		"--to must name an older release", e.target, e.source)
}

// warnUnrecognised reports a source from a release newer than any in the
// version map, and proceeds.
//
// It warns rather than refuses because the map's newest entry is a statement
// about what has been tested, not about what works. On the working assumption
// that such a release only bumps the version number the conversion is already
// the right one.
func (d *Downgrader) warnUnrecognised(src string, sourceVersion int) {
	if sourceVersion <= newestKnownProjectVersion() {
		return
	}
	if d.unrecognised {
		return // already warned for this Downgrader; the flag stays set
	}
	d.unrecognised = true
	_, _ = fmt.Fprintf(d.errw(),
		"warning: %s: unrecognised Premiere release (too new). Attempting to convert anyway.\n", src)
}

// resolveTarget turns the requested target into a concrete XML <Project>
// Version for a source at sourceVersion. A request of 0 means "auto": take the
// release one step below the source, which is the default when no --to is
// given. Anything else is checked against the source, because this is a
// downgrader — an explicit --to at or above the source release is almost
// certainly user error, so refuse rather than stamp a higher version.
//
// src names the file being resolved, for the unrecognised-release warning only.
func (d *Downgrader) resolveTarget(src string, sourceVersion, requested int, verbose bool) (int, error) {
	// Before the branch: an unrecognised source is worth saying regardless of
	// whether the target was chosen automatically or given with --to.
	d.warnUnrecognised(src, sourceVersion)
	if requested != 0 {
		if requested >= sourceVersion {
			return 0, &notOlderError{target: requested, source: sourceVersion}
		}
		return requested, nil
	}
	pv, name, ok := previousRelease(sourceVersion)
	if !ok {
		return 0, fmt.Errorf("source is version %d; no known earlier release to "+
			"downgrade to (use --to to force one)", sourceVersion)
	}
	if verbose {
		_, _ = fmt.Fprintf(d.out(), "  auto target: source version %d -> %s (version %d)\n",
			sourceVersion, name, pv)
	}
	return pv, nil
}

// needsFieldReinsertion reports whether this conversion has to re-insert the
// fields the sparse serialisation omits.
func needsFieldReinsertion(sourceVersion, targetVersion int) bool {
	return sourceVersion > lastDenseSerialisationProjectVersion &&
		targetVersion <= lastDenseSerialisationProjectVersion
}

// verifyDowngraded is the self-check run on the finished document before it is
// written. It refuses (returns an error, so nothing is written) unless every
// invariant a correct downgrade must satisfy holds:
//
//   - the stamped <Project> version reads back as the requested target,
//   - the finished document still parses, and the render-fidelity guard inside
//     reconstructPositionalClasses passes on every class instance, and
//   - when this conversion was supposed to re-insert fields, a second
//     reconstruction pass is a no-op: it inserts nothing (every reconstruct class
//     already carries all fields the target requires) and renders byte-for-byte
//     identical output.
//
// Reaching a fixpoint is the whole invariant for the third check: if a second
// pass would add a field, the first pass left one missing; if it would render
// different bytes, the round-trip is lossy. Either way this turns a would-be
// silent corruption into a hard refusal.
func verifyDowngraded(xml string, wantVersion int, reinserted bool) error {
	got, err := getProjectVersion(xml)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if got != wantVersion {
		return fmt.Errorf("verify: output <Project> version is %d, want %d", got, wantVersion)
	}
	reXML, stats, err := reconstructPositionalClasses(xml)
	if err != nil {
		return fmt.Errorf("verify: re-parse of the output failed: %w", err)
	}
	if !reinserted {
		return nil
	}
	if reXML != xml {
		return fmt.Errorf("verify: a second reconstruction pass changed the output; the first pass was not a fixpoint")
	}
	for k, n := range stats {
		if n > 0 {
			return fmt.Errorf("verify: reconstruction still inserted %s/%s (%dx); required fields were missing after downgrade", k.tag, k.field, n)
		}
	}
	return nil
}

// Downgrade converts one project file and returns an error rather than exiting,
// so a caller processing several files can report a failure and move on to the
// rest. Every failure is per-file — operational ones (unreadable file,
// out-of-range target, write failure) and genuinely malformed XML alike — so
// one corrupt project in a batch never aborts the remaining files.
func (d *Downgrader) Downgrade(src, dst string, projectVersion int, verbose bool) error {
	raw, err := readMaybeGzip(src)
	if err != nil {
		return err
	}
	xml := string(raw)
	if !strings.Contains(xml, "<PremiereData") {
		return fmt.Errorf("does not look like a Premiere project")
	}

	sourceVersion, err := getProjectVersion(xml)
	if err != nil {
		return err
	}
	projectVersion, err = d.resolveTarget(src, sourceVersion, projectVersion, verbose)
	if err != nil {
		return err
	}

	reinsert := needsFieldReinsertion(sourceVersion, projectVersion)
	stats := map[fieldKey]int{}
	if reinsert {
		xml, stats, err = reconstructPositionalClasses(xml)
		if err != nil {
			return err
		}
	}
	xml, err = setProjectVersion(xml, projectVersion)
	if err != nil {
		return err
	}
	if verbose {
		switch {
		case reinsert:
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
				_, _ = fmt.Fprintf(d.out(), "  inserted %s/%s (%dx)\n", k.tag, k.field, stats[k])
			}
		case sourceVersion > lastDenseSerialisationProjectVersion:
			_, _ = fmt.Fprintf(d.out(), "  target version %d omits the same fields as the source; "+
				"nothing to re-insert, only re-gating <Project> version\n", projectVersion)
		default:
			_, _ = fmt.Fprintf(d.out(), "  source is version %d (<= %d); class formats already compatible, "+
				"only re-gating <Project> version\n", sourceVersion, lastDenseSerialisationProjectVersion)
		}
		_, _ = fmt.Fprintf(d.out(), "  set Project version -> %d\n", projectVersion)
	}

	// Prove the transform before committing it to disk: re-gated version, every
	// reconstruct class complete, parse/render round-trip lossless. A failure
	// here means we would otherwise write a corrupt project, so refuse instead.
	if err := verifyDowngraded(xml, projectVersion, reinsert); err != nil {
		return err
	}

	var out bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if _, err := zw.Write([]byte(xml)); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return writeNew(dst, out.Bytes(), 0o644)
}
