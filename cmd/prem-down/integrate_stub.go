//go:build !darwin && !windows

// Stub so the package still compiles (and tests run) on platforms without a
// file-manager integration — only darwin and windows binaries are released.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import "errors"

const (
	fileManagerName             = "your file manager"
	integrationKind             = "a file-manager entry"
	integrationInstalledMessage = ""
	integrationRemovedMessage   = ""
)

var errIntegrationUnsupported = errors.New("the right-click integration is only available on macOS and Windows")

func installIntegration() error { return errIntegrationUnsupported }
func removeIntegration() error  { return errIntegrationUnsupported }
