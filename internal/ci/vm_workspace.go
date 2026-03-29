package ci

import (
	"fmt"
	"os"
	"path/filepath"
)

func prepareVMWorkspace(src string) (string, func(), error) {
	root, err := os.MkdirTemp("", "kindling-ci-vm-*")
	if err != nil {
		return "", nil, err
	}
	workspaceRoot := filepath.Join(root, "workspace")
	if err := copyPath(src, workspaceRoot); err != nil {
		_ = os.RemoveAll(root)
		return "", nil, fmt.Errorf("copy workspace: %w", err)
	}
	return workspaceRoot, func() { _ = os.RemoveAll(root) }, nil
}
