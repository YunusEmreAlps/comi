package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/YunusEmreAlps/comi/internal/config"
	"github.com/YunusEmreAlps/comi/internal/git"
)

const projectsUsage = "usage: comi projects add <namespace> <directory> | comi projects list"

var namespaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func cmdProjects(args []string) error {
	switch {
	case len(args) == 3 && args[0] == "add":
		name := args[1]
		if !namespaceName.MatchString(name) {
			return fmt.Errorf("invalid namespace %q: use letters, digits, hyphens and underscores, starting with a letter or digit", name)
		}
		root, err := filepath.Abs(args[2])
		if err != nil {
			return err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		info, err := os.Stat(root)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", root)
		}
		if err := config.SaveNamespace(name, root); err != nil {
			return err
		}
		fmt.Printf("namespace %s -> %s\n", name, root)
		return nil
	case len(args) == 1 && args[0] == "list":
		roots, err := config.LoadNamespaces()
		if err != nil {
			return err
		}
		if len(roots) == 0 {
			fmt.Println("No namespaces yet. Use comi projects add <namespace> <directory>.")
			return nil
		}
		names := make([]string, 0, len(roots))
		for name := range roots {
			names = append(names, name)
		}
		sort.Strings(names)
		out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(out, "NAMESPACE\tDIRECTORY")
		for _, name := range names {
			fmt.Fprintf(out, "%s\t%s\n", name, roots[name])
		}
		return out.Flush()
	default:
		return errors.New(projectsUsage)
	}
}

// projectPath expands known namespace prefixes. A leading ./ or an absolute
// path continues to select a filesystem path.
func projectPath(key string) (path, base string, err error) {
	if name, rest, ok := strings.Cut(key, "/"); ok && namespaceName.MatchString(name) {
		roots, err := config.LoadNamespaces()
		if err != nil {
			return "", "", err
		}
		if base := roots[name]; base != "" {
			rel := filepath.FromSlash(rest)
			if !filepath.IsLocal(rel) {
				return "", "", fmt.Errorf("%q must name a path inside namespace %q", key, name)
			}
			path := filepath.Join(base, rel)
			// Keep an exact registered path usable for status or removal even
			// after the repository has been moved or deleted.
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				path = resolved
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", "", err
			}
			if !insideNamespace(base, path) {
				return "", "", fmt.Errorf("%q points outside namespace %q", key, name)
			}
			return path, base, nil
		}
	}
	path, err = filepath.Abs(key)
	return path, "", err
}

func insideNamespace(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && filepath.IsLocal(rel)
}

// resolveProject returns the repository root and the index of the matching
// managed project, if there is one.
func resolveProject(cfg *config.Config, key string) (string, int, error) {
	if i := cfg.FindName(key); i >= 0 {
		return cfg.Projects[i].Path, i, nil
	}
	path, base, err := projectPath(key)
	if err != nil {
		return "", -1, err
	}
	if i := cfg.FindPath(path); i >= 0 {
		return cfg.Projects[i].Path, i, nil
	}
	root, err := repositoryRoot(key, path, base)
	if err != nil {
		return "", -1, err
	}
	return root, cfg.FindPath(root), nil
}

func repositoryRoot(key, path, base string) (string, error) {
	root, err := git.Root(path)
	if err != nil {
		return "", fmt.Errorf("%q: %w", key, err)
	}
	if base != "" && !insideNamespace(base, root) {
		return "", fmt.Errorf("the repository for %q is outside its namespace", key)
	}
	return root, nil
}

func registeredProject(cfg *config.Config, key string) (int, error) {
	_, i, err := resolveProject(cfg, key)
	if err != nil {
		return -1, err
	}
	if i < 0 {
		return -1, fmt.Errorf("project %q is not registered. Add it with comi add %q", key, key)
	}
	return i, nil
}
