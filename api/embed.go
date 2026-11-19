// Package api holds the platform API contract (openapi.yaml) and, in
// v1alpha1, the cell's own resources.
package api

import _ "embed"

// OpenAPI is the contract of the platform API.
//
//go:embed openapi.yaml
var OpenAPI []byte
