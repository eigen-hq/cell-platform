// Package deploy holds what the platform hands to a machine that joins a
// cell: the pinned versions, the output helpers and node.sh (E-44).
package deploy

import _ "embed"

// NodeScript is versions.env, ui.sh (its output helpers) and node.sh.
//
//go:embed versions.env
var versions []byte

//go:embed ui.sh
var ui []byte

//go:embed node.sh
var node []byte

var NodeScript = append(append(append([]byte{}, versions...), ui...), node...)
