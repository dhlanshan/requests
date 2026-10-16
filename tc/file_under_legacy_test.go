//go:build !go1.24

package tc

import (
	"context"
	"testing"
)

func TestFileUnderFailsClosedOnLegacyToolchain(t *testing.T) {
	if body, _, err := FileBodyFactoryUnder(".", "go.mod")(context.Background()); err == nil || body != nil {
		t.Fatal("unsafe fallback enabled")
	}
}
