package boilerplates

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// MaxProjectFolderDepth is how many folder levels below the root are scanned for manifests.
const MaxProjectFolderDepth = 3

// MaxProjectFolders caps the subfolder projects analysed in one repository.
const MaxProjectFolders = 20

// projectLanguage names the manifest and lockfiles that make a folder a project.
type projectLanguage struct {
	Name      string
	Manifest  string
	Lockfiles []string
}

var projectLanguages = []projectLanguage{
	{Name: "javascript", Manifest: "package.json", Lockfiles: []string{"package-lock.json", "yarn.lock", "pnpm-lock.yaml"}},
	{Name: "php", Manifest: "composer.json", Lockfiles: []string{"composer.lock"}},
}

// ProjectFolder is a folder with a manifest for one language, relative to the root ("." for the root).
type ProjectFolder struct {
	Path     string
	Language string
}

// SkippedFolder is a folder that has a manifest but was not analysed, and why.
type SkippedFolder struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ProjectFolders is what a scan of a source tree found.
type ProjectFolders struct {
	Projects []ProjectFolder
	Skipped  []SkippedFolder
}

// Languages lists the languages of the projects found, in scan order, without repeats.
func (p ProjectFolders) Languages() []string {
	languages := []string{}
	for _, project := range p.Projects {
		if !slices.Contains(languages, project.Language) {
			languages = append(languages, project.Language)
		}
	}
	return languages
}

// For returns the project folders of one language, compared case-insensitively.
func (p ProjectFolders) For(language string) []string {
	paths := []string{}
	for _, project := range p.Projects {
		if strings.EqualFold(project.Language, language) {
			paths = append(paths, project.Path)
		}
	}
	return paths
}

// ScanProjectFolders finds the projects in a source tree. A root with any manifest or
// lockfile is the only project, as before. Otherwise each subfolder up to
// MaxProjectFolderDepth levels down with a manifest and a lockfile is a project, and one
// with a manifest but no lockfile is skipped. Dependency and hidden folders are not scanned.
func ScanProjectFolders(root string) ProjectFolders {
	found := ProjectFolders{Projects: []ProjectFolder{}, Skipped: []SkippedFolder{}}
	for _, language := range projectLanguages {
		if hasAny(root, append([]string{language.Manifest}, language.Lockfiles...)) {
			found.Projects = append(found.Projects, ProjectFolder{Path: ".", Language: language.Name})
		}
	}
	if len(found.Projects) > 0 {
		return found
	}

	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() || path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fs.SkipDir
		}
		name := entry.Name()
		if name == "node_modules" || name == "vendor" || strings.HasPrefix(name, ".") {
			return fs.SkipDir
		}
		rel = filepath.ToSlash(rel)
		isProject := false
		for _, language := range projectLanguages {
			if !hasAny(path, []string{language.Manifest}) {
				continue
			}
			switch {
			case !hasAny(path, language.Lockfiles):
				found.Skipped = append(found.Skipped, SkippedFolder{Path: rel, Reason: fmt.Sprintf("%s has no lockfile (%s)", language.Manifest, strings.Join(language.Lockfiles, ", "))})
			case len(found.Projects) >= MaxProjectFolders:
				found.Skipped = append(found.Skipped, SkippedFolder{Path: rel, Reason: fmt.Sprintf("more than %d project folders were found", MaxProjectFolders)})
			default:
				found.Projects = append(found.Projects, ProjectFolder{Path: rel, Language: language.Name})
				isProject = true
			}
		}
		// A project's own workspaces are its analyzer's job, and the scan stops at its depth limit.
		if isProject || strings.Count(rel, "/")+1 >= MaxProjectFolderDepth {
			return fs.SkipDir
		}
		return nil
	})
	return found
}

// hasAny reports whether the folder holds any of the named files.
func hasAny(folder string, names []string) bool {
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(folder, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
