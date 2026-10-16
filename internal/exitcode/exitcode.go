// Package exitcode defines the exit codes hoex itself uses in sandbox mode.
// Any other code is the exit code of the command that ran on the host.
// The values follow sysexits.h.
package exitcode

const (
	// Usage: invalid command line.
	Usage = 64
	// Internal: protocol error or unexpected failure inside hoex.
	Internal = 70
	// Unavailable: the host daemon is not running or cannot be reached.
	Unavailable = 69
	// Denied: the host rejected the command.
	Denied = 77
)
