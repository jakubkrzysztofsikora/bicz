package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set at release build time:
//
//	go build -ldflags "-X main.version=v1.2.3 -X main.commit=<sha> -X main.date=<rfc3339>"
//
// An unstamped build (go build / go run) reports "dev", except that a binary
// built by `go install module@vX.Y.Z` reports that module version.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

type buildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func currentBuildInfo() buildInfo {
	info := buildInfo{Version: version, Commit: commit, Date: date, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if info.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	if info.Commit == "" {
		for _, setting := range bi.Settings {
			if setting.Key == "vcs.revision" {
				info.Commit = setting.Value
			}
		}
	}
	return info
}

// runVersion prints the build identity. It reads no ledger and touches no
// config, so installers can call it to verify a freshly downloaded binary.
func runVersion(_ context.Context, args []string) error {
	fs := newFlagSet("version")
	jsonOut := fs.Bool("json", false, "print machine-readable build info")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info := currentBuildInfo()
	if *jsonOut {
		encoded, err := json.Marshal(info)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	line := fmt.Sprintf("bicz %s (%s/%s, %s)", info.Version, info.OS, info.Arch, info.Go)
	if info.Commit != "" {
		line += " commit " + info.Commit
	}
	fmt.Println(line)
	return nil
}
