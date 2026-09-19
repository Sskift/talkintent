package cli

import (
	"fmt"
	"io"
	"runtime"
)

var (
	// Version is set at build time or defaults to 1.0.0.
	Version = "1.0.0"
	// BuildTime is set at build time or defaults to the implementation date.
	BuildTime = "2026-09-20"
)

// VersionInfo holds version metadata.
type VersionInfo struct {
	Version   string `json:"version"`
	BuildTime string `json:"build_time"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
}

// ExecuteVersion outputs binary version and platform information.
func ExecuteVersion(jsonOutput bool, stdout io.Writer) int {
	info := VersionInfo{
		Version:   Version,
		BuildTime: BuildTime,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		GoVersion: runtime.Version(),
	}

	if jsonOutput {
		_ = PrintJSON(stdout, info)
		return 0
	}

	fmt.Fprintf(stdout, "talkintent version %s (%s) %s/%s [%s]\n",
		info.Version, info.BuildTime, info.OS, info.Arch, info.GoVersion)
	return 0
}
