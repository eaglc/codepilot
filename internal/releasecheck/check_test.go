package releasecheck

import (
	"reflect"
	"testing"
)

func TestMetadataValidate(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		wantErr  bool
	}{
		{name: "stable", metadata: Metadata{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567", BuildDate: "2026-08-24T12:30:00Z"}},
		{name: "prerelease", metadata: Metadata{Version: "1.2.3-rc.1", Commit: "abcdef0123456789abcdef0123456789abcdef01", BuildDate: "2026-08-24T20:30:00+08:00"}},
		{name: "leading v", metadata: Metadata{Version: "v1.2.3", Commit: "abcdef0123456789abcdef0123456789abcdef01", BuildDate: "2026-08-24T12:30:00Z"}, wantErr: true},
		{name: "leading zero prerelease", metadata: Metadata{Version: "1.2.3-01", Commit: "abcdef0123456789abcdef0123456789abcdef01", BuildDate: "2026-08-24T12:30:00Z"}, wantErr: true},
		{name: "unsafe version", metadata: Metadata{Version: "1.2.3 bad", Commit: "abcdef0123456789abcdef0123456789abcdef01", BuildDate: "2026-08-24T12:30:00Z"}, wantErr: true},
		{name: "short commit", metadata: Metadata{Version: "1.2.3", Commit: "abcdef", BuildDate: "2026-08-24T12:30:00Z"}, wantErr: true},
		{name: "invalid date", metadata: Metadata{Version: "1.2.3", Commit: "abcdef0123456789abcdef0123456789abcdef01", BuildDate: "today"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.metadata.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestTargetsAreTheSupportedReleaseMatrix(t *testing.T) {
	want := []Target{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "windows", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
	}
	if got := Targets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Targets() = %#v, want %#v", got, want)
	}
	first := Targets()
	first[0].GOOS = "changed"
	if Targets()[0].GOOS != "linux" {
		t.Fatal("Targets exposed shared mutable state")
	}
}

func TestBuildEnvironmentReplacesTargetVariables(t *testing.T) {
	t.Setenv("GOOS", "plan9")
	t.Setenv("GOARCH", "386")
	t.Setenv("CGO_ENABLED", "1")
	environment := buildEnvironment(Target{GOOS: "linux", GOARCH: "arm64"})
	want := map[string]string{"GOOS": "linux", "GOARCH": "arm64", "CGO_ENABLED": "0"}
	counts := map[string]int{}
	for _, entry := range environment {
		for name, value := range want {
			if entry == name+"="+value {
				counts[name]++
			}
		}
	}
	for name := range want {
		if counts[name] != 1 {
			t.Fatalf("%s target environment count = %d", name, counts[name])
		}
	}
}
