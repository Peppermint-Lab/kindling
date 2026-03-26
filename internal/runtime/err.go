package runtime

import "errors"

// ErrInstanceNotRunning is returned when ResourceStats is requested for an unknown or stopped instance.
var ErrInstanceNotRunning = errors.New("instance not running")

// ErrProcStatsUnsupported is returned when OS-level process stats are not available (e.g. crun on non-Linux).
var ErrProcStatsUnsupported = errors.New("process stats not supported on this platform")
