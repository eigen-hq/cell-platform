// Package catalog holds the apps that can be installed, one manifest per
// directory: <app>/manifest.yaml.
package catalog

import "embed"

//go:embed */manifest.yaml
var FS embed.FS
