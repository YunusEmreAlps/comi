package config

import (
	"os"
	"path/filepath"

	"github.com/YunusEmreAlps/comi/internal/lock"
)

const namespaceFile = "namespaces.json"

func LoadNamespaces() (map[string]string, error) {
	roots := make(map[string]string)
	err := readJSON(namespaceFile, &roots)
	if roots == nil {
		roots = make(map[string]string)
	}
	return roots, err
}

// SaveNamespace sets a root without changing the saved paths of any projects.
func SaveNamespace(name, root string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	release, err := lock.AcquireFile(filepath.Join(dir, "comi-namespaces.lock"))
	if err != nil {
		return err
	}
	defer release()
	roots, err := LoadNamespaces()
	if err != nil {
		return err
	}
	roots[name] = root
	return writeJSON(namespaceFile, roots)
}
