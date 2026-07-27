// Premiere Pro Production support.
//
// A Production is a folder, not a file: it holds exactly one .prodset settings
// file plus any number of .prproj files. Membership is implicit — every .prproj
// under the folder belongs to the Production.
//
// A Production downgrade mirrors the whole folder to a sibling
// "<name>_downgraded" and rewrites the project files as it copies. Everything
// else in the folder is copied verbatim, so project links that are relative to
// the Production folder keep resolving.
//
// Premiere identifies a Production by the .prodset whose basename matches the
// containing folder, so the settings file is renamed to the new folder name as
// it is written ("<name>_downgraded.prodset").
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package premdown

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PrprojExt is the extension of a Premiere project file and ProdsetExt is the
// extension of a Production's settings file. Exported because the OS
// file-manager integration registers its context-menu entry against exactly the
// file types prem-down can convert.
const (
	PrprojExt  = ".prproj"
	ProdsetExt = ".prodset"
)

const (
	// Productions were introduced on 14 April 2020. Older releases have no
	// concept of a Production at all, so stamping one down to them produces a
	// folder no Premiere will ever open — refused rather than written.
	firstProductionProjectVersion = 38
)

// A .prodset is plain JSON. Two keys carry the version: mProjectVersion, the
// release that wrote the Production, and mMinCompatibleProjectVersion, the
// oldest release allowed to open it.
//
// The rewrite is textual, for the same reason the .prproj path uses regexes.
// Re-encoding through encoding/json would reorder nothing (Go sorts map keys
// and Adobe already writes them sorted) but it would re-escape the embedded XML
// blobs — Go escapes <, > and & as < and friends — producing a file that is
// semantically equal and byte-wise unrecognisable. Parsing is still used, to
// validate that the input really is a .prodset and to verify the result.
var (
	prodsetVersionRe   = regexp.MustCompile(`("mProjectVersion"\s*:\s*)(\d+)`)
	prodsetMinCompatRe = regexp.MustCompile(`("mMinCompatibleProjectVersion"\s*:\s*)(\d+)`)
)

// prodsetVersions holds the two version keys. Pointers, so a key that is absent
// is distinguishable from one that is present and zero.
type prodsetVersions struct {
	Project       *int `json:"mProjectVersion"`
	MinCompatible *int `json:"mMinCompatibleProjectVersion"`
}

func parseProdset(raw []byte) (prodsetVersions, error) {
	var p prodsetVersions
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("does not look like a Production settings file: %w", err)
	}
	if p.Project == nil {
		return p, errors.New(`does not look like a Production settings file: no "mProjectVersion" key`)
	}
	return p, nil
}

// setProdsetKey rewrites one integer-valued JSON key in place, leaving every
// other byte of the document untouched. It insists on exactly one match: the
// keys are top-level and written once, so a different count means the file is
// not the shape we understand and is safer refused than guessed at.
//
// The `"` in each pattern is what keeps "mProjectVersion" from matching inside
// "mMinCompatibleProjectVersion" (preceded by `e`, not a quote) or inside the
// escaped `\"` of an embedded XML string.
func setProdsetKey(js string, re *regexp.Regexp, name string, value int) (string, error) {
	if n := len(re.FindAllStringIndex(js, -1)); n != 1 {
		return "", fmt.Errorf("expected exactly one %q key, found %d", name, n)
	}
	return re.ReplaceAllString(js, fmt.Sprintf("${1}%d", value)), nil
}

// verifyProdset is the self-check run on the rewritten settings before it is
// written: the result must still parse as JSON, must read back as the requested
// target, and must not leave a compatibility floor above that target (which
// would bar the very release we are downgrading for). A failure means we would
// otherwise write a Production that cannot open, so nothing is written.
func verifyProdset(js string, wantVersion int) error {
	v, err := parseProdset([]byte(js))
	if err != nil {
		return fmt.Errorf("verify: re-parse of the output failed: %w", err)
	}
	if *v.Project != wantVersion {
		return fmt.Errorf("verify: output mProjectVersion is %d, want %d", *v.Project, wantVersion)
	}
	if v.MinCompatible != nil && *v.MinCompatible > wantVersion {
		return fmt.Errorf("verify: output mMinCompatibleProjectVersion is %d, above the target %d",
			*v.MinCompatible, wantVersion)
	}
	return nil
}

// downgradeProdset rewrites one Production settings file. Mirrors downgrade's
// contract: every failure is returned, never fatal, so a batch keeps going.
func (d *Downgrader) downgradeProdset(src, dst string, projectVersion int, verbose bool) error {
	raw, err := readMaybeGzip(src)
	if err != nil {
		return err
	}
	v, err := parseProdset(raw)
	if err != nil {
		return err
	}
	projectVersion, err = d.resolveTarget(*v.Project, projectVersion, verbose)
	if err != nil {
		return err
	}
	if err := checkProductionTarget(projectVersion); err != nil {
		return err
	}

	js, err := setProdsetKey(string(raw), prodsetVersionRe, "mProjectVersion", projectVersion)
	if err != nil {
		return err
	}
	// Lower the compatibility floor to the target so the target release is let
	// in, but never raise it: a Production whose floor is already older than the
	// target keeps that wider compatibility.
	if v.MinCompatible != nil && *v.MinCompatible > projectVersion {
		js, err = setProdsetKey(js, prodsetMinCompatRe, "mMinCompatibleProjectVersion", projectVersion)
		if err != nil {
			return err
		}
	}
	if verbose {
		_, _ = fmt.Fprintf(d.out(), "  set mProjectVersion -> %d\n", projectVersion)
		if v.MinCompatible != nil && *v.MinCompatible > projectVersion {
			_, _ = fmt.Fprintf(d.out(), "  set mMinCompatibleProjectVersion -> %d\n", projectVersion)
		}
	}
	if err := verifyProdset(js, projectVersion); err != nil {
		return err
	}
	return writeNew(dst, []byte(js), 0o644)
}

// checkProductionTarget refuses a target older than the first release that
// understood Productions. Reached two ways: an explicit --to naming a
// pre-Productions release, and — since the auto target is one release below the
// source — a Production already at the floor (2020.1), whose auto target lands
// one step below it. That second case is a Production that simply cannot be
// downgraded: it is already the oldest release Productions exist in.
func checkProductionTarget(projectVersion int) error {
	if projectVersion >= firstProductionProjectVersion {
		return nil
	}
	_, name, _ := previousRelease(firstProductionProjectVersion + 1)
	return fmt.Errorf("target version %d predates Premiere Pro %s, the first release with "+
		"Productions (April 2020); a Production cannot be downgraded below it",
		projectVersion, name)
}

// findProdset returns the single .prodset at the top level of dir. A Production
// has exactly one; zero means the folder is not a Production, and more than one
// is ambiguous. Neither is something to guess at, so both are refused.
func findProdset(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ProdsetExt) {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("not a Premiere Production: no %s file in %s", ProdsetExt, dir)
	default:
		return "", fmt.Errorf("not a Premiere Production: found %d %s files in %s, expected one",
			len(found), ProdsetExt, dir)
	}
}

// copyFile reproduces one file verbatim at dst. Used for everything in a
// Production that is not a project file — media, previews, autosaves, caches —
// so links stored relative to the Production folder still resolve in the copy.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // G304: src is inside the user-supplied Production folder; copying it is the tool's purpose
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// O_EXCL for the same reason writeNew uses it: dst is a path we believe to
	// be free, and a name claimed since must fail rather than be overwritten.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) //nolint:gosec // G302,G304: dst mirrors a file in the source Production; its mode is copied from the original
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst) //nolint:gosec // G703: dst is the O_EXCL path we just created above; removing our own partial copy
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst) //nolint:gosec // G703: dst is the O_EXCL path we just created above; removing our own partial copy
		return err
	}
	return nil
}

// productionTarget resolves the one version every file in the Production will
// be stamped with. It comes from the .prodset, not from each project: a
// Production is only coherent if its settings file and all its projects name
// the same release, so the projects must not each pick their own auto target.
func (d *Downgrader) productionTarget(prodset string, requested int, verbose bool) (int, error) {
	raw, err := readMaybeGzip(prodset)
	if err != nil {
		return 0, err
	}
	v, err := parseProdset(raw)
	if err != nil {
		return 0, err
	}
	target, err := d.resolveTarget(*v.Project, requested, verbose)
	if err != nil {
		return 0, err
	}
	return target, checkProductionTarget(target)
}

// DowngradeProduction mirrors the Production at srcDir into the new folder
// dstDir, downgrading the .prodset and every .prproj on the way and copying
// everything else verbatim.
//
// Per-file failures are reported and the walk continues, matching how a
// multi-file batch behaves: one unreadable project should not cost the user the
// other forty. If anything did fail the function returns an error, because the
// resulting folder is an incomplete Production and must not be presented as a
// finished one. The partial folder is deliberately left on disk rather than
// deleted — it may hold gigabytes of successfully copied media, and it is
// clearly named, so what to do with it is the user's call.
func (d *Downgrader) DowngradeProduction(srcDir, dstDir string, projectVersion int, verbose bool) error {
	prodset, err := findProdset(srcDir)
	if err != nil {
		return err
	}
	target, err := d.productionTarget(prodset, projectVersion, verbose)
	if err != nil {
		return err
	}

	// Exclusive: the output folder must be one we create, so nothing already on
	// disk is merged into or overwritten.
	if err := os.Mkdir(dstDir, 0o755); err != nil { //nolint:gosec // G301: the output folder mirrors a user Production meant to be opened and shared
		return err
	}

	var projects, copied, failed int
	fail := func(rel string, err error) {
		_, _ = fmt.Fprintf(d.errw(), "error: %s: %v\n", rel, err)
		failed++
	}

	walkErr := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return relErr
		}
		if err != nil {
			fail(rel, err)
			// An unreadable directory yields no entries; nothing to skip.
			return nil
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(dstDir, rel)

		// Premiere identifies a Production by the .prodset whose basename
		// matches the containing folder. The output folder is renamed
		// ("<name>_downgraded"), so its settings file has to be renamed to
		// match (only a top-level .prodset is renamed).
		if path == prodset {
			dst = filepath.Join(dstDir, filepath.Base(dstDir)+ProdsetExt)
		}

		info, infoErr := entry.Info()
		perm := fs.FileMode(0o644)
		if entry.IsDir() {
			perm = 0o755
		}
		if infoErr == nil {
			perm = info.Mode().Perm()
		}

		switch {
		case entry.IsDir():
			if err := os.Mkdir(dst, perm); err != nil {
				fail(rel, err)
				return fs.SkipDir // no destination to copy this subtree into
			}
			return nil

		case entry.Type()&fs.ModeSymlink != 0:
			// Recreated as a symlink rather than followed, so a link pointing
			// outside the Production is not silently inlined and a link inside it
			// still resolves within the copy.
			link, err := os.Readlink(path)
			if err != nil {
				fail(rel, err)
				return nil
			}
			if err := os.Symlink(link, dst); err != nil { //nolint:gosec // G122: dst is under dstDir, an output folder created fresh this run; the walk recreates links rather than following them, and this is a single-user desktop tool operating on the user's own Production, so a mid-walk symlink-swap TOCTOU is out of the threat model
				fail(rel, err)
			}
			return nil

		case !entry.Type().IsRegular():
			// Sockets, pipes, devices: nothing a Production legitimately contains
			// and nothing meaningful to copy.
			_, _ = fmt.Fprintf(d.errw(), "warning: skipped %s (not a regular file)\n", rel)
			return nil
		}

		switch strings.ToLower(filepath.Ext(path)) {
		case ProdsetExt:
			if err := d.downgradeProdset(path, dst, target, verbose); err != nil {
				fail(rel, err)
				return nil
			}
			if verbose && filepath.Base(dst) != filepath.Base(path) {
				_, _ = fmt.Fprintf(d.out(), "  renamed settings file %s -> %s \n",
					filepath.Base(path), filepath.Base(dst))
			}
			projects++
		case PrprojExt:
			err := d.Downgrade(path, dst, target, verbose)
			var notOlder *notOlderError
			if errors.As(err, &notOlder) {
				// Already at or below the Production's target, so there is nothing
				// to downgrade; copy it through so the Production stays complete.
				if err := copyFile(path, dst, perm); err != nil {
					fail(rel, err)
					return nil
				}
				if verbose {
					_, _ = fmt.Fprintf(d.out(), "  %s: already version %d, copied unchanged\n",
						rel, notOlder.source)
				}
				copied++
				return nil
			}
			if err != nil {
				fail(rel, err)
				return nil
			}
			projects++
		default:
			if err := copyFile(path, dst, perm); err != nil {
				fail(rel, err)
				return nil
			}
			copied++
			return nil
		}
		if verbose {
			outRel, _ := filepath.Rel(dstDir, dst)
			_, _ = fmt.Fprintf(d.out(), "  wrote %s\n", outRel)
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	if failed > 0 {
		return fmt.Errorf("%d of the Production's files could not be written; %s is incomplete",
			failed, dstDir)
	}
	_, _ = fmt.Fprintf(d.out(), "wrote %s (%d project files, %d other files copied)\n",
		dstDir, projects, copied)
	return nil
}
