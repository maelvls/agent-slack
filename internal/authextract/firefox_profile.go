package authextract

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// firefoxProfile is a Firefox profile directory.
type firefoxProfile struct {
	Name      string // from profiles.ini; empty for a directory found by scanning
	Path      string
	IsDefault bool
}

// firefoxBaseDirs lists the directories that may hold profiles.ini.
func (h *host) firefoxBaseDirs() ([]string, error) {
	switch h.goos {
	case "darwin":
		return []string{filepath.Join(h.home, "Library", "Application Support", "Firefox")}, nil
	case "linux":
		dirs := []string{filepath.Join(h.home, ".mozilla", "firefox")}
		// Firefox 147+ follows the XDG base directory specification.
		xdg := h.getenv("XDG_CONFIG_HOME")
		if xdg == "" || !filepath.IsAbs(xdg) {
			xdg = filepath.Join(h.home, ".config")
		}
		return append(dirs,
			filepath.Join(xdg, "mozilla", "firefox"),
			filepath.Join(h.home, "snap", "firefox", "common", ".mozilla", "firefox"),          // Snap (Ubuntu's default)
			filepath.Join(h.home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox"), // Flatpak
		), nil
	}
	return nil, notFoundf("Firefox extraction is not supported on %s", h.goos)
}

// parseProfilesINI reads the profiles declared in a profiles.ini located
// in baseDir.
func parseProfilesINI(raw, baseDir string) []firefoxProfile {
	type section struct {
		name, path string
		isRelative bool
		isDefault  bool
	}
	var sections []section
	// Firefox marks defaults either with Default=1 in a [Profile*] section
	// or with Default=<path> in an [Install*] section.
	installDefaults := map[string]bool{}

	var current *section
	inInstall := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := line[1 : len(line)-1]
			current, inInstall = nil, strings.HasPrefix(name, "Install")
			if strings.HasPrefix(name, "Profile") {
				sections = append(sections, section{isRelative: true})
				current = &sections[len(sections)-1]
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case current != nil:
			switch key {
			case "Name":
				current.name = value
			case "Path":
				current.path = value
			case "IsRelative":
				current.isRelative = value != "0"
			case "Default":
				current.isDefault = value == "1"
			}
		case inInstall && key == "Default" && value != "":
			installDefaults[value] = true
		}
	}

	var profiles []firefoxProfile
	for _, s := range sections {
		if s.path == "" {
			continue
		}
		path := s.path
		if s.isRelative {
			path = filepath.Join(baseDir, filepath.FromSlash(s.path))
		}
		profiles = append(profiles, firefoxProfile{
			Name:      s.name,
			Path:      path,
			IsDefault: s.isDefault || installDefaults[s.path],
		})
	}
	return profiles
}

// firefoxProfiles lists the existing profile directories, default profiles
// first.
func (h *host) firefoxProfiles() ([]firefoxProfile, error) {
	baseDirs, err := h.firefoxBaseDirs()
	if err != nil {
		return nil, err
	}
	var profiles []firefoxProfile
	known := map[string]bool{}
	add := func(p firefoxProfile) {
		if !known[p.Path] && isDir(p.Path) {
			known[p.Path] = true
			profiles = append(profiles, p)
		}
	}
	for _, baseDir := range baseDirs {
		if raw, err := os.ReadFile(filepath.Join(baseDir, "profiles.ini")); err == nil {
			for _, p := range parseProfilesINI(string(raw), baseDir) {
				add(p)
			}
		}
		// profiles.ini can be stale: also scan the directory profiles are
		// created in.
		scanDir := baseDir
		if h.goos == "darwin" {
			scanDir = filepath.Join(baseDir, "Profiles")
		}
		entries, _ := os.ReadDir(scanDir)
		for _, e := range entries {
			if e.IsDir() {
				add(firefoxProfile{Path: filepath.Join(scanDir, e.Name())})
			}
		}
	}
	slices.SortStableFunc(profiles, func(a, b firefoxProfile) int {
		switch {
		case a.IsDefault == b.IsDefault:
			return 0
		case a.IsDefault:
			return -1
		}
		return 1
	})
	return profiles, nil
}
