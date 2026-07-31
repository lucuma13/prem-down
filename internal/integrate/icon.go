// The Windows executable embeds the prem-down icon so Explorer's right-click
// "Downgrade for older Premiere" entry shows it: both integrate_windows.go
// and the MSI
// (packaging/windows/prem-down.wxs) set that menu's Icon to the exe itself, and
// Windows lifts the icon out of the binary's resources.
//
// The icon is carried by rsrc_windows_amd64.syso, which the Go linker embeds
// into windows/amd64 builds automatically. The .syso is committed so GoReleaser
// needs no extra build step, but it is generated from the PNG files in
// packaging/windows/winres/. Regenerate it after changing the artwork with:
//
//  go generate ./internal/integrate
//
// macOS has its own equivalent artwork: integrate_darwin.go embeds a committed
// template TIFF (workflowCustomImageTemplate.tiff) and writes it into the Quick
// Action bundle's Resources, where NSIconName references it - so the Finder
// right-click entry shows the prem-down glyph, tinted to match the OS.

package integrate

//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64 --in ../../packaging/windows/winres/winres.json --out rsrc
