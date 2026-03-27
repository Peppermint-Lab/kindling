package deploy

import (
	"context"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/runtime"
)

func (d *Deployer) fillLiveMigrationMetadata(ctx context.Context, instanceID uuid.UUID, meta *instanceVMMetadata) {
	if d == nil || d.rt == nil || meta == nil || !d.rt.Supports(runtime.CapabilityLiveMigration) {
		return
	}
	info, err := d.rt.MigrationMetadata(ctx, instanceID)
	if err != nil {
		return
	}
	if meta.SharedRootfsRef == "" {
		meta.SharedRootfsRef = info.SharedRootfsRef
	}
}
