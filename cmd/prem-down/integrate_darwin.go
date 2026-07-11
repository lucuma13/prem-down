// macOS implementation of the "integrate" subcommand: a Finder Quick Action.
//
// A Quick Action is an Automator .workflow bundle in ~/Library/Services — two
// plists plus a template-image TIFF, no compiled code — so it can be written
// directly and needs no code signing. Its shell step resolves prem-down
// through the Homebrew bin dirs at run time (not an absolute Cellar/Caskroom
// path), so it survives upgrades of the cask untouched.
//
// NSIconName is resolved against the workflow bundle's own Resources, so a
// template TIFF named workflowCustomImageTemplate.tiff there is drawn as the
// menu icon.
//
// The service accepts any file (public.data) rather than a .prproj UTI:
// without Premiere installed the extension maps to a dynamic UTI, and with it
// installed to whatever UTI Adobe declares, so no fixed UTI list is reliable.
// The shell step filters by extension instead and explains itself via a
// dialog when handed the wrong file.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// quickActionIcon is the template TIFF the Quick Action shows in Finder's
// right-click menu — a committed asset (a multi-resolution, "Template"-named
// TIFF, so macOS tints it to match native icons).
//
//go:embed workflowCustomImageTemplate.tiff
var quickActionIcon []byte

const (
	quickActionMenuTitle = "Downgrade for older Premiere"

	// quickActionIconName is the NSIconName value and the basename (minus
	// extension) of the TIFF in the bundle's Resources. The "Template" suffix
	// makes AppKit treat it as a template image (tinted to match the OS).
	quickActionIconName = "workflowCustomImageTemplate"

	integrationInstalledMessage = `Installed the Finder Quick Action: right-click a .prproj file and pick
Quick Actions > ` + quickActionMenuTitle + `.
If it doesn't appear, enable it with Quick Actions > Customise…`
	integrationRemovedMessage = "Removed the Finder Quick Action."

	// serviceEnabledStatus is the NSServicesStatus value that marks the Quick
	// Action as enabled in every Finder context. It carries both the modern
	// presentation_modes dictionary and the legacy enabled_* booleans so the
	// entry reads as "on" across macOS versions.
	serviceEnabledStatus = `{"enabled_context_menu":true,"enabled_services_menu":true,` +
		`"presentation_modes":{"ContextMenu":true,"ServicesMenu":true,"FinderPreview":true,"TouchBar":false}}`
)

// quickActionScript is the Quick Action's shell step. Finder hands it the
// selected files as arguments; results surface as a notification (success) or
// a dialog (failure) since there is no terminal to print to. osascript
// receives strings via argv, never by splicing them into the AppleScript
// source, so paths and messages cannot break quoting.
const quickActionScript = `export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
dialog() { /usr/bin/osascript -e 'on run argv' -e 'display dialog (item 1 of argv) buttons {"OK"} default button 1 with title "prem-down" with icon caution' -e 'end run' "$1" >/dev/null; }
for f in "$@"; do
  case "$f" in
  *.prproj) ;;
  *)
    dialog "Not a Premiere project (.prproj): $f"
    continue
    ;;
  esac
  if out=$(prem-down "$f" 2>&1); then
    /usr/bin/osascript -e 'on run argv' -e 'display notification (item 1 of argv) with title "prem-down"' -e 'end run' "$out" >/dev/null
  else
    dialog "$out"
  fi
done`

// quickActionInfoPlist declares the Services entry Finder reads: the menu
// title, that it takes files, and that it runs the workflow.
const quickActionInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>NSServices</key>
	<array>
		<dict>
			<key>NSBackgroundSystemColorName</key>
			<string>systemPurpleColor</string>
			<key>NSIconName</key>
			<string>` + quickActionIconName + `</string>
			<key>NSMenuItem</key>
			<dict>
				<key>default</key>
				<string>` + quickActionMenuTitle + `</string>
			</dict>
			<key>NSMessage</key>
			<string>runWorkflowAsService</string>
			<key>NSRequiredContext</key>
			<dict>
				<key>NSApplicationIdentifier</key>
				<string>com.apple.finder</string>
			</dict>
			<key>NSSendFileTypes</key>
			<array>
				<string>public.data</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
`

// quickActionDocumentPlist is the Automator document: a single Run Shell
// Script action receiving the files as arguments ("inputMethod 1"). %s is the
// XML-escaped shell script.
const quickActionDocumentPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>AMApplicationBuild</key>
	<string>528</string>
	<key>AMApplicationVersion</key>
	<string>2.10</string>
	<key>AMDocumentVersion</key>
	<string>2</string>
	<key>actions</key>
	<array>
		<dict>
			<key>action</key>
			<dict>
				<key>AMAccepts</key>
				<dict>
					<key>Container</key>
					<string>List</string>
					<key>Optional</key>
					<true/>
					<key>Types</key>
					<array>
						<string>com.apple.cocoa.string</string>
					</array>
				</dict>
				<key>AMActionVersion</key>
				<string>2.0.3</string>
				<key>AMApplication</key>
				<array>
					<string>Automator</string>
				</array>
				<key>AMParameterProperties</key>
				<dict>
					<key>COMMAND_STRING</key>
					<dict/>
					<key>CheckedForUserDefaultShell</key>
					<dict/>
					<key>inputMethod</key>
					<dict/>
					<key>shell</key>
					<dict/>
					<key>source</key>
					<dict/>
				</dict>
				<key>AMProvides</key>
				<dict>
					<key>Container</key>
					<string>List</string>
					<key>Types</key>
					<array>
						<string>com.apple.cocoa.string</string>
					</array>
				</dict>
				<key>ActionBundlePath</key>
				<string>/System/Library/Automator/Run Shell Script.action</string>
				<key>ActionName</key>
				<string>Run Shell Script</string>
				<key>ActionParameters</key>
				<dict>
					<key>COMMAND_STRING</key>
					<string>%s</string>
					<key>CheckedForUserDefaultShell</key>
					<false/>
					<key>inputMethod</key>
					<integer>1</integer>
					<key>shell</key>
					<string>/bin/bash</string>
					<key>source</key>
					<string></string>
				</dict>
				<key>BundleIdentifier</key>
				<string>com.apple.RunShellScript</string>
				<key>CFBundleVersion</key>
				<string>2.0.3</string>
				<key>CanShowSelectedItemsWhenRun</key>
				<false/>
				<key>CanShowWhenRun</key>
				<true/>
				<key>Category</key>
				<array>
					<string>AMCategoryUtilities</string>
				</array>
				<key>Class Name</key>
				<string>RunShellScriptAction</string>
				<key>InputUUID</key>
				<string>9A6E9A2A-0002-4E4C-89E4-77B1B7B7A001</string>
				<key>Keywords</key>
				<array>
					<string>Shell</string>
				</array>
				<key>OutputUUID</key>
				<string>9A6E9A2A-0003-4E4C-89E4-77B1B7B7A002</string>
				<key>UUID</key>
				<string>9A6E9A2A-0001-4E4C-89E4-77B1B7B7A000</string>
				<key>UnlocalizedApplications</key>
				<array>
					<string>Automator</string>
				</array>
				<key>arguments</key>
				<dict>
					<key>0</key>
					<dict>
						<key>default value</key>
						<integer>0</integer>
						<key>name</key>
						<string>inputMethod</string>
						<key>required</key>
						<string>0</string>
						<key>type</key>
						<string>enumeration</string>
						<key>uuid</key>
						<string>0</string>
					</dict>
					<key>1</key>
					<dict>
						<key>default value</key>
						<false/>
						<key>name</key>
						<string>CheckedForUserDefaultShell</string>
						<key>required</key>
						<string>0</string>
						<key>type</key>
						<string>boolean</string>
						<key>uuid</key>
						<string>1</string>
					</dict>
					<key>2</key>
					<dict>
						<key>default value</key>
						<string></string>
						<key>name</key>
						<string>source</string>
						<key>required</key>
						<string>0</string>
						<key>type</key>
						<string>string</string>
						<key>uuid</key>
						<string>2</string>
					</dict>
					<key>3</key>
					<dict>
						<key>default value</key>
						<string></string>
						<key>name</key>
						<string>COMMAND_STRING</string>
						<key>required</key>
						<string>0</string>
						<key>type</key>
						<string>string</string>
						<key>uuid</key>
						<string>3</string>
					</dict>
					<key>4</key>
					<dict>
						<key>default value</key>
						<string>/bin/sh</string>
						<key>name</key>
						<string>shell</string>
						<key>required</key>
						<string>0</string>
						<key>type</key>
						<string>string</string>
						<key>uuid</key>
						<string>4</string>
					</dict>
				</dict>
				<key>isViewVisible</key>
				<integer>1</integer>
				<key>location</key>
				<string>309.000000:305.000000</string>
				<key>nibPath</key>
				<string>/System/Library/Automator/Run Shell Script.action/Contents/Resources/Base.lproj/main.nib</string>
			</dict>
			<key>isViewVisible</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>connectors</key>
	<dict/>
	<key>workflowMetaData</key>
	<dict>
		<key>applicationBundleID</key>
		<string>com.apple.finder</string>
		<key>applicationBundleIDsByPath</key>
		<dict>
			<key>/System/Library/CoreServices/Finder.app</key>
			<string>com.apple.finder</string>
		</dict>
		<key>applicationPath</key>
		<string>/System/Library/CoreServices/Finder.app</string>
		<key>applicationPaths</key>
		<array>
			<string>/System/Library/CoreServices/Finder.app</string>
		</array>
		<key>serviceApplicationBundleID</key>
		<string>com.apple.finder</string>
		<key>serviceApplicationPath</key>
		<string>/System/Library/CoreServices/Finder.app</string>
		<key>inputTypeIdentifier</key>
		<string>com.apple.Automator.fileSystemObject</string>
		<key>outputTypeIdentifier</key>
		<string>com.apple.Automator.nothing</string>
		<key>presentationMode</key>
		<integer>15</integer>
		<key>processesInput</key>
		<integer>0</integer>
		<key>serviceInputTypeIdentifier</key>
		<string>com.apple.Automator.fileSystemObject</string>
		<key>serviceOutputTypeIdentifier</key>
		<string>com.apple.Automator.nothing</string>
		<key>serviceProcessesInput</key>
		<integer>0</integer>
		<key>systemImageName</key>
		<string>NSActionTemplate</string>
		<key>useAutomaticInputType</key>
		<integer>0</integer>
		<key>workflowTypeIdentifier</key>
		<string>com.apple.Automator.servicesMenu</string>
	</dict>
</dict>
</plist>
`

var xmlTextEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func quickActionPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Services", quickActionMenuTitle+".workflow"), nil
}

func installIntegration() error {
	bundle, err := quickActionPath()
	if err != nil {
		return err
	}
	contents := filepath.Join(bundle, "Contents")
	resources := filepath.Join(contents, "Resources")
	if err := os.MkdirAll(resources, 0o755); err != nil { //nolint:gosec // G301: a Services workflow bundle is world-traversable by convention
		return err
	}
	document := fmt.Sprintf(quickActionDocumentPlist, xmlTextEscaper.Replace(quickActionScript))
	for name, body := range map[string]string{
		"Info.plist":     quickActionInfoPlist,
		"document.wflow": document,
	} {
		if err := os.WriteFile(filepath.Join(contents, name), []byte(body), 0o644); err != nil { //nolint:gosec // G306: a Services workflow is world-readable by convention
			return err
		}
	}
	// The custom menu icon: NSIconName resolves this by basename from Resources.
	if err := os.WriteFile(filepath.Join(resources, quickActionIconName+".tiff"), quickActionIcon, 0o644); err != nil { //nolint:gosec // G306: a Services workflow resource is world-readable by convention
		return err
	}
	// Best-effort: newly registered Quick Actions default to off, so switch
	// ours on. If this fails, integrationInstalledMessage documents the manual
	// "Quick Actions > Customise" fallback.
	_ = enableServiceMenu()
	refreshServicesMenu()
	return nil
}

// enableServiceMenu is the seam installIntegration uses to switch the Quick
// Action on. It is a variable so tests can replace it: the real implementation
// writes the per-user `pbs` preference domain, which cfprefsd resolves by UID
// (ignoring $HOME), so it would otherwise flip the setting on the developer's
// own machine during `go test`.
var enableServiceMenu = enableQuickAction

// serviceStatusKey identifies the Quick Action in the Services database (the
// `pbs` NSServicesStatus dictionary). A workflow service carries no bundle
// identifier, so pbs keys it under "(null)"; the middle segment is the menu
// title and the suffix is the NSMessage declared in Info.plist.
func serviceStatusKey() string {
	return fmt.Sprintf("(null) - %s - runWorkflowAsService", quickActionMenuTitle)
}

// enableQuickAction turns the Quick Action on so it appears without the user
// first ticking it under Finder's "Quick Actions > Customise".
//
// The state lives in the per-user `pbs` preference domain, whose key for a
// workflow service begins with "(null)". `defaults -dict-add` cannot write a
// key starting with "(" — it parses it as an old-style plist array — so the
// edit is done on a temp copy: export the domain, splice our entry in with
// plutil (which addresses the parenthesized key fine), import it back. Export
// and import both go through cfprefsd, so there is no cache race with a direct
// file edit. A `pbs -flush` (via refreshServicesMenu) then re-reads it.
func enableQuickAction() error {
	f, err := os.CreateTemp("", "prem-down-pbs-*.plist")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(tmp) }()

	// Snapshot the current domain; an absent domain exports as an empty dict.
	if err := exec.Command("defaults", "export", "pbs", tmp).Run(); err != nil { //nolint:gosec // G204: constant args; tmp is our own temp path
		return err
	}
	// Ensure the NSServicesStatus container exists before addressing into it;
	// plutil -replace does not create intermediate dictionaries.
	if exec.Command("plutil", "-extract", "NSServicesStatus", "raw", tmp).Run() != nil { //nolint:gosec // G204: constant args; own temp path
		if err := exec.Command("plutil", "-insert", "NSServicesStatus", "-dictionary", tmp).Run(); err != nil { //nolint:gosec // G204: constant args; own temp path
			return err
		}
	}
	keyPath := "NSServicesStatus." + serviceStatusKey()
	if err := exec.Command("plutil", "-replace", keyPath, "-json", serviceEnabledStatus, tmp).Run(); err != nil { //nolint:gosec // G204: keyPath derives from a constant title; own temp path
		return err
	}
	if err := exec.Command("defaults", "import", "pbs", tmp).Run(); err != nil { //nolint:gosec // G204: constant args; own temp path
		return err
	}
	return nil
}

func removeIntegration() error {
	bundle, err := quickActionPath()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(bundle); err != nil {
		return err
	}
	refreshServicesMenu()
	return nil
}

// refreshServicesMenu asks the pasteboard server to rescan ~/Library/Services
// so the Quick Action (dis)appears without waiting for a re-login.
// Best-effort: the scan also happens on its own, just later.
func refreshServicesMenu() {
	_ = exec.Command("/System/Library/CoreServices/pbs", "-flush").Run()
}
