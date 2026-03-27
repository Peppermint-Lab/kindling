package runtime

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

func PersistentVolumePath(id uuid.UUID) string {
	return filepath.Join(persistentVolumeDir(), id.String()+".qcow2")
}

func persistentVolumeDir() string {
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data/volumes"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "kindling-volumes")
	}
	return filepath.Join(home, ".kindling", "volumes")
}
