// The Windows executable embeds the prem-down icon so Explorer's right-click
// "Downgrade for older Premiere" entry shows it: both integrate_windows.go and
// the MSI (packaging/windows/prem-down.wxs) set that menu's Icon to the exe
// itself, and Windows lifts the icon out of the binary's resources.
//
// The icon is carried by rsrc_windows_amd64.syso, which the Go linker embeds
// into windows/amd64 builds automatically; other platforms ignore it via the
// _windows_amd64 filename suffix, so macOS builds and `go test` are untouched.
// The .syso is committed so GoReleaser needs no extra build step, but it is
// generated from the PNG files in packaging/windows/winres/. Regenerate it after
// changing the artwork with:
//
//	go generate ./cmd/prem-down
//
// macOS has no equivalent: a Finder Quick Action can only show a system
// template symbol (see NSIconName in integrate_darwin.go), not custom artwork.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64 --in ../../packaging/windows/winres/winres.json --out rsrc
