package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunSmokeModeWithRelativeRepositoryPath(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "awesome-compose")
	samplePath := filepath.Join(repoPath, "demo")
	if err := os.MkdirAll(samplePath, 0o755); err != nil {
		t.Fatalf("create sample directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(samplePath, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	manifestPath := filepath.Join(root, "manifest.yaml")
	manifest := `repository:
  url: https://example.invalid/awesome-compose.git
  ref: test-ref
samples:
  - name: demo
    expected: supported
    smoke: true
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	composeCommand := writeExecutable(t, root, "compose", `#!/bin/sh
set -eu
case " $* " in
  *" config "*)
    printf '%s\n' 'name: demo' 'services:' '  web:' '    image: nginx:1.0' '    ports:' '      - target: 80' '        published: "80"'
    ;;
  *" bridge convert "*)
    mkdir -p out/chart
    touch out/chart/Chart.yaml
    printf '%s\n' 'variables: []' > out/zarf.yaml
    ;;
esac
`)
	helmCommand := writeExecutable(t, root, "helm", `#!/bin/sh
set -eu
test -f "$2/Chart.yaml"
`)
	zarfCommand := writeExecutable(t, root, "zarf", `#!/bin/sh
set -eu
case " $* " in
  *" package create "*)
    touch zarf-package-demo-amd64.tar.zst
    ;;
  *" package deploy "*)
    test -f "$3"
    ;;
esac
`)
	writeExecutable(t, root, "kubectl", "#!/bin/sh\nexit 0\n")
	writeExecutable(t, root, "curl", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	relativeRepoPath, err := filepath.Rel(workingDir, repoPath)
	if err != nil {
		t.Fatalf("make repository path relative: %v", err)
	}
	if filepath.IsAbs(relativeRepoPath) {
		t.Fatalf("expected a relative repository path, got %q", relativeRepoPath)
	}

	opts := options{
		Mode:           "smoke",
		ManifestPath:   manifestPath,
		RepoPath:       relativeRepoPath,
		ComposeCommand: composeCommand,
		TransformImage: "compose-bridge-uds:test",
		HelmCommand:    helmCommand,
		ZarfCommand:    zarfCommand,
		JSONPath:       filepath.Join(root, "report.json"),
		MarkdownPath:   filepath.Join(root, "report.md"),
	}
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func writeExecutable(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", name, err)
	}
	return path
}
