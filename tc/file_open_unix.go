//go:build unix

package tc

import (
	"os"
	"syscall"
)

const regularFileOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
