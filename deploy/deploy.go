// Package deploy holds what the platform hands to a machine that joins a
// cell: the pinned versions and node.sh (E-44).
package deploy

import _ "embed"

// NodeScript is versions.env followed by node.sh.
//
//go:embed versions.env
var versions []byte

//go:embed node.sh
var node []byte

var NodeScript = append(append([]byte{}, versions...), node...)
