//go:build !darwin

package builder

import (
	"context"
	"fmt"
)

func SmokeTestAppleBuilderVM(ctx context.Context) error {
	_ = ctx
	return fmt.Errorf("builder VM smoke test is only supported on macOS")
}
