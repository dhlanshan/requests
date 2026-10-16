//go:build !unix

package tc

import "os"

const regularFileOpenFlags = os.O_RDONLY
