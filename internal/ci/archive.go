package ci

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func SnapshotWorkspaceBase64(root string) (string, error) {
	var buf strings.Builder
	enc := base64.NewEncoder(base64.StdEncoding, &buf)
	if err := writeWorkspaceArchive(root, enc); err != nil {
		enc.Close()
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func RepackGitHubTarballBase64(src io.Reader) (string, error) {
	var buf strings.Builder
	enc := base64.NewEncoder(base64.StdEncoding, &buf)
	rewriter := newTarRewriter(enc)
	if err := rewriteGitHubTarball(src, rewriter); err != nil {
		_ = rewriter.close()
		_ = enc.Close()
		return "", err
	}
	if err := rewriter.close(); err != nil {
		_ = enc.Close()
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func ExtractGitHubTarballToDir(src io.Reader, dst string) error {
	return rewriteGitHubTarball(src, &archiveExtractor{dst: dst})
}

func SaveArchiveFromBase64(encoded, dst string) error {
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	file, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, reader); err != nil {
		return err
	}
	return file.Close()
}

func ExtractArchiveToDir(archivePath, dst string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dst, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			if err := os.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		}
	}
}

func writeWorkspaceArchive(root string, w io.Writer) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	gzw := gzip.NewWriter(w)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()
	return filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipArchivePath(rel, info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel = filepath.ToSlash(rel)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		return func() error {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(tw, file)
			return err
		}()
	})
}

func shouldSkipArchivePath(rel string, info os.FileInfo) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		switch part {
		case ".git", "node_modules", ".turbo", ".next", "dist", "_deploy":
			return true
		}
	}
	if !info.IsDir() {
		name := info.Name()
		if strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".out") {
			return true
		}
	}
	return false
}

func archiveStorageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".kindling", "ci-jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir ci jobs dir: %w", err)
	}
	return dir, nil
}

type archiveExtractor struct {
	dst string
}

func (e *archiveExtractor) writeHeader(hdr *tar.Header) error {
	target := filepath.Join(e.dst, hdr.Name)
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		return os.MkdirAll(filepath.Dir(target), 0o755)
	default:
		return nil
	}
}

func (e *archiveExtractor) writeFile(hdr *tar.Header, r io.Reader) error {
	if hdr.Typeflag != tar.TypeReg {
		return nil
	}
	target := filepath.Join(e.dst, hdr.Name)
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(target, os.FileMode(hdr.Mode))
}

type tarballWriter interface {
	writeHeader(*tar.Header) error
	writeFile(*tar.Header, io.Reader) error
}

func rewriteGitHubTarball(src io.Reader, out tarballWriter) error {
	gzr, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer gzr.Close()
	return streamGitHubTarball(tar.NewReader(gzr), out)
}

func streamGitHubTarball(tr *tar.Reader, out tarballWriter) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := stripGitHubArchivePrefix(hdr.Name)
		if name == "" {
			continue
		}
		copyHdr := *hdr
		copyHdr.Name = name
		if err := out.writeHeader(&copyHdr); err != nil {
			return err
		}
		if err := out.writeFile(&copyHdr, tr); err != nil {
			return err
		}
	}
}

type tarRewriter struct {
	gzw *gzip.Writer
	tw  *tar.Writer
}

func newTarRewriter(w io.Writer) *tarRewriter {
	gzw := gzip.NewWriter(w)
	return &tarRewriter{
		gzw: gzw,
		tw:  tar.NewWriter(gzw),
	}
}

func (w *tarRewriter) writeHeader(hdr *tar.Header) error {
	return w.tw.WriteHeader(hdr)
}

func (w *tarRewriter) writeFile(_ *tar.Header, r io.Reader) error {
	_, err := io.Copy(w.tw, r)
	return err
}

func (w *tarRewriter) close() error {
	if err := w.tw.Close(); err != nil {
		return err
	}
	return w.gzw.Close()
}

func stripGitHubArchivePrefix(name string) string {
	name = filepath.ToSlash(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	parts := strings.SplitN(name, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
