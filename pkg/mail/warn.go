package mail

import (
	"fmt"
	"os"
)

// Warn receives every non-fatal warning this package would otherwise print
// to stderr (index fallback, content budget, journal cleanup). The CLI
// replaces it so warnings land in the JSON envelope's notices instead of
// interleaving with structured output.
var Warn = func(message string) {
	fmt.Fprintln(os.Stderr, "mail-app-cli: "+message)
}
