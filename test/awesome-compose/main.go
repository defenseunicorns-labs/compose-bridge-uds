package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"defenseunicorns/uds-compose-bridge/internal/compose"
	"defenseunicorns/uds-compose-bridge/internal/model"
	"defenseunicorns/uds-compose-bridge/internal/render"
	yamlv3 "gopkg.in/yaml.v3"
)

type manifest struct {
	Repository repository `yaml:"repository"`
	Samples    []sample   `yaml:"samples"`
}

type repository struct {
	URL string `yaml:"url"`
	Ref string `yaml:"ref"`
}

type sample struct {
	Name        string   `yaml:"name"`
	Expected    string   `yaml:"expected"`
	Diagnostics []string `yaml:"diagnostics,omitempty"`
	Smoke       bool     `yaml:"smoke,omitempty"`
}

type report struct {
	Repository string   `json:"repository"`
	Ref        string   `json:"ref"`
	Mode       string   `json:"mode"`
	Generated  string   `json:"generated"`
	Results    []result `json:"results"`
}

type result struct {
	Name        string   `json:"name"`
	Expected    string   `json:"expected"`
	Actual      string   `json:"actual"`
	Diagnostics []string `json:"diagnostics,omitempty"`
	Message     string   `json:"message,omitempty"`
	Duration    string   `json:"duration"`
	Passed      bool     `json:"passed"`
}

type options struct {
	Mode           string
	ManifestPath   string
	RepoPath       string
	ComposeCommand string
	TransformImage string
	HelmCommand    string
	ZarfCommand    string
	JSONPath       string
	MarkdownPath   string
}

func main() {
	opts := parseFlags()
	if err := run(context.Background(), opts); err != nil {
		fmt.Fprintf(os.Stderr, "awesome-compose: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	var opts options
	flag.StringVar(&opts.Mode, "mode", "static", "Test mode: static, full, or smoke")
	flag.StringVar(&opts.ManifestPath, "manifest", "test/awesome-compose/manifest.yaml", "Compatibility manifest")
	flag.StringVar(&opts.RepoPath, "repo", "", "Existing awesome-compose checkout")
	flag.StringVar(&opts.ComposeCommand, "compose-command", "docker compose", "Compose command")
	flag.StringVar(&opts.TransformImage, "transform-image", "compose-bridge-uds:test", "Transformation image used in full mode")
	flag.StringVar(&opts.HelmCommand, "helm-command", "helm", "Helm command used in full mode")
	flag.StringVar(&opts.ZarfCommand, "zarf-command", "", "Optional Zarf command, for example `uds zarf`")
	flag.StringVar(&opts.JSONPath, "json", "awesome-compose-report.json", "JSON report path")
	flag.StringVar(&opts.MarkdownPath, "markdown", "awesome-compose-report.md", "Markdown report path")
	flag.Parse()
	return opts
}

func run(ctx context.Context, opts options) error {
	if opts.Mode != "static" && opts.Mode != "full" && opts.Mode != "smoke" {
		return fmt.Errorf("invalid mode %q", opts.Mode)
	}
	data, err := os.ReadFile(opts.ManifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var cfg manifest
	if err := yamlv3.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateManifest(cfg); err != nil {
		return err
	}

	repoPath := opts.RepoPath
	if repoPath == "" {
		repoPath, err = cloneRepository(ctx, cfg.Repository)
		if err != nil {
			return err
		}
	}
	repoPath, err = filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}
	if err := validateCorpus(repoPath, cfg.Samples); err != nil {
		return err
	}

	r := report{
		Repository: cfg.Repository.URL,
		Ref:        cfg.Repository.Ref,
		Mode:       opts.Mode,
		Generated:  time.Now().UTC().Format(time.RFC3339),
	}
	for _, item := range cfg.Samples {
		if opts.Mode == "smoke" && !item.Smoke {
			continue
		}
		started := time.Now()
		res := runSample(ctx, opts, repoPath, item)
		res.Duration = time.Since(started).Round(time.Millisecond).String()
		r.Results = append(r.Results, res)
		status := "PASS"
		if !res.Passed {
			status = "FAIL"
		}
		fmt.Printf("%s %-31s expected=%s actual=%s\n", status, res.Name, res.Expected, res.Actual)
	}

	if err := writeReports(opts, r); err != nil {
		return err
	}
	for _, res := range r.Results {
		if !res.Passed {
			return fmt.Errorf("compatibility expectations failed; see %s", opts.MarkdownPath)
		}
	}
	return nil
}

func validateManifest(cfg manifest) error {
	if cfg.Repository.URL == "" || cfg.Repository.Ref == "" {
		return fmt.Errorf("manifest repository URL and ref are required")
	}
	seen := map[string]struct{}{}
	for _, item := range cfg.Samples {
		if item.Name == "" {
			return fmt.Errorf("manifest sample name is required")
		}
		if _, ok := seen[item.Name]; ok {
			return fmt.Errorf("duplicate manifest sample %q", item.Name)
		}
		seen[item.Name] = struct{}{}
		if item.Expected != "supported" && item.Expected != "unsupported" {
			return fmt.Errorf("sample %q has invalid expected result %q", item.Name, item.Expected)
		}
		if item.Expected == "unsupported" && len(item.Diagnostics) == 0 {
			return fmt.Errorf("unsupported sample %q must declare diagnostics", item.Name)
		}
	}
	return nil
}

func validateCorpus(repoPath string, samples []sample) error {
	discovered := map[string]struct{}{}
	err := filepath.WalkDir(repoPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isComposeFilename(entry.Name()) {
			return nil
		}
		relative, err := filepath.Rel(repoPath, filepath.Dir(path))
		if err != nil {
			return err
		}
		discovered[filepath.ToSlash(relative)] = struct{}{}
		return nil
	})
	if err != nil {
		return fmt.Errorf("discover Compose samples: %w", err)
	}
	expected := map[string]struct{}{}
	for _, item := range samples {
		expected[item.Name] = struct{}{}
	}
	missing := []string{}
	extra := []string{}
	for name := range discovered {
		if _, ok := expected[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range expected {
		if _, ok := discovered[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("corpus does not match manifest (unclassified=%v, absent=%v)", missing, extra)
	}
	return nil
}

func cloneRepository(ctx context.Context, source repository) (string, error) {
	dir, err := os.MkdirTemp("", "awesome-compose-")
	if err != nil {
		return "", fmt.Errorf("create clone directory: %w", err)
	}
	if _, err := runCommand(ctx, "", []string{"git", "clone", "--quiet", "--no-checkout", source.URL, dir}); err != nil {
		return "", err
	}
	if _, err := runCommand(ctx, dir, []string{"git", "checkout", "--quiet", source.Ref}); err != nil {
		return "", err
	}
	return dir, nil
}

func runSample(ctx context.Context, opts options, repoPath string, item sample) result {
	res := result{Name: item.Name, Expected: item.Expected}
	sampleDir := filepath.Join(repoPath, item.Name)
	composeFile := findComposeFile(sampleDir)
	if composeFile == "" {
		res.Actual = "missing"
		res.Message = "Compose file not found"
		return res
	}

	canonical, err := canonicalModel(ctx, opts.ComposeCommand, sampleDir, composeFile)
	if err != nil {
		res.Actual = "failed"
		res.Message = err.Error()
		return res
	}
	app, err := compose.LoadCanonicalYAML(canonical)
	if err != nil {
		var compatibilityErr *compose.CompatibilityError
		if errors.As(err, &compatibilityErr) {
			res.Actual = "unsupported"
			for _, issue := range compatibilityErr.Issues {
				res.Diagnostics = append(res.Diagnostics, issue.Code)
			}
			res.Diagnostics = uniqueSorted(res.Diagnostics)
			res.Message = err.Error()
			res.Passed = item.Expected == "unsupported" && sameStrings(res.Diagnostics, uniqueSorted(item.Diagnostics))
			return res
		}
		res.Actual = "failed"
		res.Message = err.Error()
		return res
	}

	res.Actual = "supported"
	if item.Expected != "supported" {
		res.Message = "sample unexpectedly became supported; update the manifest after review"
		return res
	}
	staticOut, err := os.MkdirTemp("", "awesome-compose-output-")
	if err != nil {
		res.Message = err.Error()
		return res
	}
	if err := render.WritePackage(staticOut, app); err != nil {
		res.Actual = "failed"
		res.Message = err.Error()
		return res
	}
	for _, path := range []string{"zarf.yaml", "chart/Chart.yaml", "chart/templates/uds-package.yaml"} {
		if _, err := os.Stat(filepath.Join(staticOut, filepath.FromSlash(path))); err != nil {
			res.Actual = "failed"
			res.Message = fmt.Sprintf("expected output %s: %v", path, err)
			return res
		}
	}

	if opts.Mode == "full" || opts.Mode == "smoke" {
		if err := runFull(ctx, opts, sampleDir, composeFile, app, opts.Mode == "smoke"); err != nil {
			res.Actual = "failed"
			res.Message = err.Error()
			return res
		}
	}
	res.Passed = true
	return res
}

func canonicalModel(ctx context.Context, composeCommand, dir, composeFile string) ([]byte, error) {
	command := append(strings.Fields(composeCommand), "-f", composeFile, "config")
	data, err := runCommand(ctx, dir, command)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	var raw map[string]any
	if err := yamlv3.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode canonical model: %w", err)
	}
	projectName, _ := raw["name"].(string)
	services, _ := raw["services"].(map[string]any)
	for name, value := range services {
		service, _ := value.(map[string]any)
		if _, builds := service["build"]; !builds {
			continue
		}
		if image, _ := service["image"].(string); strings.TrimSpace(image) == "" {
			service["image"] = projectName + "-" + name
		}
	}
	return yamlv3.Marshal(raw)
}

func runFull(ctx context.Context, opts options, dir, composeFile string, app model.App, deploy bool) error {
	compose := strings.Fields(opts.ComposeCommand)
	if err := runStreamingCommand(ctx, dir, append(append([]string{}, compose...), "-f", composeFile, "build")); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := runStreamingCommand(ctx, dir, append(append([]string{}, compose...), "-f", composeFile, "bridge", "convert", "-t", opts.TransformImage)); err != nil {
		return fmt.Errorf("bridge convert: %w", err)
	}
	if err := runStreamingCommand(ctx, dir, []string{opts.HelmCommand, "lint", filepath.Join(dir, "out", "chart")}); err != nil {
		return fmt.Errorf("helm lint: %w", err)
	}
	if err := runStreamingCommand(ctx, dir, []string{opts.HelmCommand, "template", filepath.Join(dir, "out", "chart")}); err != nil {
		return fmt.Errorf("helm template: %w", err)
	}
	if opts.ZarfCommand == "" {
		return nil
	}
	zarf := strings.Fields(opts.ZarfCommand)
	if err := runStreamingCommand(ctx, dir, append(append([]string{}, zarf...), "package", "create", "out", "--confirm")); err != nil {
		return fmt.Errorf("zarf package create: %w", err)
	}
	if !deploy {
		return nil
	}
	packages, err := filepath.Glob(filepath.Join(dir, "zarf-package-*.tar.zst"))
	if err != nil || len(packages) != 1 {
		return fmt.Errorf("expected one Zarf package after creation, found %d", len(packages))
	}
	deployCommand := append(append([]string{}, zarf...), "package", "deploy", packages[0], "--confirm")
	variableNames, err := packageVariableNames(filepath.Join(dir, "out", "zarf.yaml"))
	if err != nil {
		return err
	}
	for _, name := range variableNames {
		deployCommand = append(deployCommand, "--set-variables", name+"=awesome-compose-smoke-value")
	}
	if err := runStreamingCommand(ctx, dir, deployCommand); err != nil {
		return fmt.Errorf("zarf package deploy: %w", err)
	}
	if err := runStreamingCommand(ctx, dir, []string{"kubectl", "rollout", "status", "deployment", "--all", "--namespace", app.Package.Namespace, "--timeout", "10m"}); err != nil {
		return fmt.Errorf("wait for deployments: %w", err)
	}
	host := firstPublishedService(app)
	if host == "" {
		return fmt.Errorf("smoke sample has no published service")
	}
	if err := runStreamingCommand(ctx, dir, []string{"curl", "--fail", "--insecure", "--location", "--retry", "12", "--retry-all-errors", "--retry-delay", "5", "--max-time", "20", "https://" + host + ".uds.dev/"}); err != nil {
		return fmt.Errorf("HTTP smoke check: %w", err)
	}
	return nil
}

func packageVariableNames(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Zarf config variables: %w", err)
	}
	var doc struct {
		Variables []struct {
			Name string `yaml:"name"`
		} `yaml:"variables"`
	}
	if err := yamlv3.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode Zarf config variables: %w", err)
	}
	names := make([]string, 0, len(doc.Variables))
	for _, variable := range doc.Variables {
		if variable.Name != "" {
			names = append(names, variable.Name)
		}
	}
	return names, nil
}

func firstPublishedService(app model.App) string {
	for _, service := range app.Services {
		for _, port := range service.Ports {
			if port.Published {
				return service.Name
			}
		}
	}
	return ""
}

func findComposeFile(dir string) string {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}

func isComposeFilename(name string) bool {
	switch name {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	default:
		return false
	}
}

func runCommand(ctx context.Context, dir string, command []string) ([]byte, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "COMPOSE_ANSI=never")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(command, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func runStreamingCommand(ctx context.Context, dir string, command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	fmt.Printf("RUN  %s\n", strings.Join(command, " "))
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "COMPOSE_ANSI=never")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(command, " "), err)
	}
	return nil
}

func writeReports(opts options, r report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(opts.JSONPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	var markdown strings.Builder
	fmt.Fprintf(&markdown, "# Awesome Compose compatibility\n\n")
	fmt.Fprintf(&markdown, "Upstream: `%s`  \nMode: `%s`  \nGenerated: `%s`\n\n", r.Ref, r.Mode, r.Generated)
	markdown.WriteString("| Sample | Expected | Actual | Diagnostics | Result |\n")
	markdown.WriteString("|---|---|---|---|---|\n")
	for _, item := range r.Results {
		status := "pass"
		if !item.Passed {
			status = "fail"
		}
		fmt.Fprintf(&markdown, "| `%s` | %s | %s | `%s` | %s |\n", item.Name, item.Expected, item.Actual, strings.Join(item.Diagnostics, ", "), status)
	}
	return os.WriteFile(opts.MarkdownPath, []byte(markdown.String()), 0o644)
}

func uniqueSorted(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
