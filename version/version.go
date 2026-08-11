package version

import (
	"runtime/debug"
)

// Info is the build identity split into its component parts.
type Info struct {
	// Module is the main module path.
	Module string
	// Version is the best available display version: the VCS revision when
	// present, otherwise the module version, or "unknown".
	Version string
	// Commit is the VCS revision, when available.
	Commit string
	// Date is the VCS commit time, when available.
	Date string
	// Dirty reports whether the source tree had uncommitted changes.
	Dirty bool
}

// Version returns the module path and build identity as separate parts.
func Version(name string) *Info {
	info := Info{Version: "unknown"}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		info.Module = name
		return &info
	}

	info.Module = bi.Main.Path
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.time":
			info.Date = s.Value
		case "vcs.modified":
			info.Dirty = s.Value == "true"
		}
	}

	switch {
	case info.Commit != "":
		info.Version = info.Commit
	case bi.Main.Version != "" && bi.Main.Version != "(devel)":
		info.Version = bi.Main.Version
	}

	return &info
}

// String reports the module path and best available version from
// embedded build info: VCS revision for source builds, module version for
// proxy installs, "unknown" when neither exists (e.g. tarball builds).
func (info *Info) String() string {
	version := info.Version
	if info.Commit != "" {
		if info.Date != "" {
			version += " (committed " + info.Date + ")"
		}
		if info.Dirty {
			version += " [dirty]"
		}
	}
	return info.Module + " " + version
}
