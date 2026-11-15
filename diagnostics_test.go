package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExternalVolumeIdentitySurvivesPathCaseVariants(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS volume metadata")
	}
	tools := t.TempDir()
	// Only disk metadata is stubbed; plutil parses the real plist format. No
	// real volume is mounted, formatted or changed by this regression test.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>MountPoint</key><string>/Volumes/External</string>
<key>VolumeUUID</key><string>FIXTURE-VOLUME-UUID</string>
</dict></plist>`
	script := "#!/bin/sh\ncat <<'PLIST'\n" + plist + "\nPLIST\n"
	if err := os.WriteFile(filepath.Join(tools, "diskutil"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, root := range []string{
		"/Volumes/External/share", "/volumes/external/share", "/VOLUMES/EXTERNAL/share",
	} {
		identity, err := localVolumeIdentity(root)
		if err != nil || identity != "FIXTURE-VOLUME-UUID" {
			t.Errorf("%q lost its volume binding: %q, %v", root, identity, err)
		}
	}
	if _, err := localVolumeIdentity("/Volumes/Other/share"); err == nil {
		t.Fatal("accepted metadata for a different mount point")
	}
	if identity, err := localVolumeIdentity(filepath.Join(tools, "ordinary-share")); err != nil || identity != "" {
		t.Fatal("ordinary Home directory should not require external-volume metadata")
	}
}
