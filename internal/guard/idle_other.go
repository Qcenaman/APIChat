//go:build !windows

package guard

import "time"

func idleFor() time.Duration { return -1 }
