// +build !dragonfly,!linux,!netbsd,!openbsd,!solaris,!darwin,!freebsd

package postfix

import (
	"time"
)

//nolint:deadcode
func statCTime(_ interface{}) time.Time {
	return time.Time{}
}
