// Copyright (C) 2020 Storj Labs, Inc.
// See LICENSE for copying information.

package version

import _ "unsafe" // needed for go:linkname

//go:linkname buildTimestamp storj.io/private/version.buildTimestamp
var buildTimestamp string = "1610364781"

//go:linkname buildCommitHash storj.io/private/version.buildCommitHash
var buildCommitHash string = "35c61bd0e2c38a8853c2bcc8c280364f6b6e01d0"

//go:linkname buildVersion storj.io/private/version.buildVersion
var buildVersion string = "v1.20.1"

//go:linkname buildRelease storj.io/private/version.buildRelease
var buildRelease string = "true"

// ensure that linter understands that the variables are being used.
func init() { use(buildTimestamp, buildCommitHash, buildVersion, buildRelease) }

func use(...interface{}) {}
