// Package version reports the golab binary version and its upstream compatibility target.
package version

import "fmt"

// Number is the semantic version of this build.
const Number = "0.1.4"

// Upstream is the google-colab-cli release line this build targets.
const Upstream = "0.8.x+HEAD"

// String returns the one-line string printed by `golab version`.
func String() string {
	return fmt.Sprintf("golab %s (compatible with google-colab-cli %s)", Number, Upstream)
}

// ClientAgent is the honest X-Colab-Client-Agent value we send (ADR-013, DEC-03).
func ClientAgent() string { return "github.com/20Youssef10/golab/" + Number }
