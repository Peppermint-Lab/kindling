package builder

import (
	"fmt"
	"os"
	"path/filepath"
)

// replaceWorkspaceFromSource removes all entries in dstDir (which must exist) and copies srcDir tree into it.
func replaceWorkspaceFromSource(srcDir, dstDir string) error {
	entries, err := os.ReadDir(dstDir)
	if err != nil {
		return fmt.Errorf("read workspace dir: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dstDir, e.Name())); err != nil {
			return fmt.Errorf("clear workspace: %w", err)
		}
	}
	if err := os.CopyFS(dstDir, os.DirFS(srcDir)); err != nil {
		return fmt.Errorf("copy workspace: %w", err)
	}
	return nil
}
