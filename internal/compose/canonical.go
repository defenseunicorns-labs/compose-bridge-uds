package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	yamlv3 "gopkg.in/yaml.v3"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

func LoadCanonicalFile(path string) (model.App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.App{}, fmt.Errorf("read canonical compose file: %w", err)
	}
	return loadCanonicalYAML(data, path)
}

func LoadCanonicalYAML(data []byte) (model.App, error) {
	return loadCanonicalYAML(data, "")
}

func loadCanonicalYAML(data []byte, sourcePath string) (model.App, error) {
	var raw map[string]any
	if err := yamlv3.Unmarshal(data, &raw); err != nil {
		return model.App{}, fmt.Errorf("decode canonical compose extensions: %w", err)
	}
	if raw == nil {
		raw = map[string]any{}
	}

	configPath := sourcePath
	if configPath == "" {
		configPath = "-"
	}
	configDetails := types.ConfigDetails{
		WorkingDir: loadWorkingDir(sourcePath),
		ConfigFiles: []types.ConfigFile{{
			Filename: configPath,
			Content:  data,
		}},
		Environment: types.Mapping{},
	}

	project, err := loader.LoadWithContext(
		context.Background(),
		configDetails,
		loader.WithSkipValidation,
		func(opts *loader.Options) {
			// Bridge passes env_file references into the transformation container, but the
			// source files themselves are not available there. Keep canonical env_file
			// decoding without attempting to read those host paths.
			opts.SkipResolveEnvironment = true
		},
	)
	if err != nil {
		return model.App{}, fmt.Errorf("decode canonical compose yaml: %w", err)
	}
	if err := validateCompatibility(*project, raw); err != nil {
		return model.App{}, err
	}

	return loadProject(*project, raw)
}

func loadWorkingDir(sourcePath string) string {
	if sourcePath != "" {
		return filepath.Dir(sourcePath)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func loadProject(project types.Project, raw map[string]any) (model.App, error) {
	if len(project.Services) == 0 {
		return model.App{}, fmt.Errorf("canonical compose model has no services")
	}

	projectNameRaw := strings.TrimSpace(project.Name)
	if projectNameRaw == "" {
		return model.App{}, fmt.Errorf("canonical compose model is missing name")
	}
	projectName, err := normalizeName(projectNameRaw)
	if err != nil {
		return model.App{}, fmt.Errorf("invalid compose project name: %w", err)
	}

	packageCfg, err := parsePackageConfig(projectName, raw)
	if err != nil {
		return model.App{}, err
	}

	networkAliases, err := normalizeTopLevelNetworks(project.Networks)
	if err != nil {
		return model.App{}, err
	}
	volumes, volumeAliases, err := normalizeTopLevelVolumes(project.Volumes)
	if err != nil {
		return model.App{}, err
	}
	secrets, secretAliases, err := normalizeTopLevelSecrets(project.Secrets)
	if err != nil {
		return model.App{}, err
	}
	configs, configAliases, err := normalizeTopLevelConfigs(project.Configs)
	if err != nil {
		return model.App{}, err
	}

	keys := make([]string, 0, len(project.Services))
	for key := range project.Services {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	serviceAliases := map[string]string{}
	for _, key := range keys {
		normalized, err := normalizeName(key)
		if err != nil {
			return model.App{}, fmt.Errorf("invalid service name %q: %w", key, err)
		}
		registerAlias(serviceAliases, key, normalized)
		registerAlias(serviceAliases, normalized, normalized)
	}
	canonicalServices, canonicalSecrets, err := canonicalBuildMaps(project)
	if err != nil {
		return model.App{}, err
	}

	services := make([]model.Service, 0, len(keys))
	buildSecrets := map[string]any{}

	for _, key := range keys {
		rawSvc := project.Services[key]
		serviceName, _ := resolveAlias(serviceAliases, key)

		ports, err := parsePorts(rawSvc.Ports, rawSvc.Expose)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q ports: %w", key, err)
		}
		networks, err := parseServiceNetworks(rawSvc.Networks, networkAliases)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q networks: %w", key, err)
		}
		volumeMounts, err := parseServiceVolumes(rawSvc.Volumes, serviceName, volumeAliases, volumes)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q volumes: %w", key, err)
		}
		secretRefs, err := parseServiceSecrets(rawSvc.Secrets, secretAliases)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q secrets: %w", key, err)
		}
		configRefs, err := parseServiceConfigs(rawSvc.Configs, configAliases)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q configs: %w", key, err)
		}
		dependsOn, err := parseDependsOn(rawSvc.DependsOn, serviceAliases)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q depends_on: %w", key, err)
		}

		image := strings.TrimSpace(rawSvc.Image)
		var build *model.BuildDefinition
		if rawSvc.Build != nil {
			buildConfig, err := canonicalBuildConfig(canonicalServices, key, serviceAliases)
			if err != nil {
				return model.App{}, err
			}
			image = builtImageReference(packageCfg, serviceName)
			build = &model.BuildDefinition{
				Config:    buildConfig,
				ReadPaths: buildReadPaths(rawSvc.Build, project.Secrets),
			}
			for _, secret := range rawSvc.Build.Secrets {
				source := strings.TrimSpace(secret.Source)
				if source == "" {
					continue
				}
				definition, ok := canonicalSecrets[source]
				if !ok {
					return model.App{}, fmt.Errorf("build secret %q is missing from the canonical Compose model", source)
				}
				buildSecrets[source] = definition
			}
		}

		services = append(services, model.Service{
			Name:         serviceName,
			Image:        image,
			Build:        build,
			Ports:        ports,
			Networks:     networks,
			Env:          parseEnvironment(rawSvc.Environment),
			User:         strings.TrimSpace(rawSvc.User),
			Privileged:   rawSvc.Privileged,
			CapAdd:       rawSvc.CapAdd,
			CapDrop:      rawSvc.CapDrop,
			SecurityOpts: rawSvc.SecurityOpt,
			Command:      copyCommand(rawSvc.Entrypoint),
			Args:         copyCommand(rawSvc.Command),
			Stdin:        rawSvc.StdinOpen,
			Hostname:     strings.TrimSpace(rawSvc.Hostname),
			Healthcheck:  parseHealthcheck(rawSvc.HealthCheck),
			Volumes:      volumeMounts,
			Secrets:      secretRefs,
			Configs:      configRefs,
			DependsOn:    dependsOn,
			Resources:    parseResources(rawSvc.Deploy),
			Profiles:     normalizeProfiles(rawSvc.Profiles),
		})
	}

	return model.App{
		Package:      packageCfg,
		Services:     services,
		Volumes:      volumes,
		Secrets:      secrets,
		Configs:      configs,
		BuildSecrets: buildSecrets,
	}, nil
}

func canonicalBuildMaps(project types.Project) (map[string]any, map[string]any, error) {
	data, err := project.MarshalYAML()
	if err != nil {
		return nil, nil, fmt.Errorf("marshal canonical Compose build model: %w", err)
	}
	var canonical map[string]any
	if err := yamlv3.Unmarshal(data, &canonical); err != nil {
		return nil, nil, fmt.Errorf("decode canonical Compose build model: %w", err)
	}
	services, _ := asMap(canonical["services"])
	secrets, _ := asMap(canonical["secrets"])
	return services, secrets, nil
}

func canonicalBuildConfig(services map[string]any, serviceName string, aliases map[string]string) (map[string]any, error) {
	service, ok := asMap(services[serviceName])
	if !ok {
		return nil, fmt.Errorf("service %q is missing from the canonical Compose build model", serviceName)
	}
	build, ok := asMap(service["build"])
	if !ok {
		return nil, fmt.Errorf("service %q has no canonical Compose build definition", serviceName)
	}

	// Compose build.tags would add extra, user-controlled references to the OCI
	// archive. Built services always use the bridge-controlled internal image.
	delete(build, "tags")
	if contexts, ok := asMap(build["additional_contexts"]); ok {
		for name, value := range contexts {
			reference, ok := value.(string)
			if !ok || !strings.HasPrefix(reference, "service:") {
				continue
			}
			if resolved, ok := resolveAlias(aliases, strings.TrimPrefix(reference, "service:")); ok {
				contexts[name] = "service:" + resolved
			}
		}
	}
	return build, nil
}

func builtImageReference(pkg model.Package, serviceName string) string {
	return fmt.Sprintf("zarf.internal/%s-%s:%s", pkg.Name, serviceName, sanitizeImageTag(pkg.Version))
}

func buildReadPaths(build *types.BuildConfig, secrets types.Secrets) []string {
	paths := map[string]struct{}{}
	addBuildReadPath(paths, build.Context, false)
	for _, context := range build.AdditionalContexts {
		addBuildReadPath(paths, context, false)
	}
	if build.Dockerfile != "" {
		switch {
		case filepath.IsAbs(build.Dockerfile):
			addBuildReadPath(paths, build.Dockerfile, true)
		case isLocalBuildPath(build.Context):
			addBuildReadPath(paths, filepath.Join(build.Context, build.Dockerfile), true)
		}
	}
	for _, secret := range build.Secrets {
		if definition, ok := secrets[secret.Source]; ok {
			addBuildReadPath(paths, definition.File, true)
		}
	}
	for _, ssh := range build.SSH {
		addBuildReadPath(paths, ssh.Path, true)
	}

	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func addBuildReadPath(paths map[string]struct{}, value string, useParent bool) {
	if !isLocalBuildPath(value) {
		return
	}
	path := filepath.Clean(strings.TrimSpace(value))
	if useParent {
		path = filepath.Dir(path)
	}
	paths[path] = struct{}{}
}

func isLocalBuildPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "://") {
		return false
	}
	for _, prefix := range []string{"service:", "target:", "docker-image:", "oci-layout:"} {
		if strings.HasPrefix(value, prefix) {
			return false
		}
	}
	return true
}

var invalidImageTagChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func sanitizeImageTag(value string) string {
	tag := invalidImageTagChars.ReplaceAllString(strings.TrimSpace(value), "-")
	tag = strings.TrimLeft(tag, ".-")
	if tag == "" {
		tag = "latest"
	}
	if len(tag) > 128 {
		tag = tag[:128]
	}
	return tag
}

func parsePackageConfig(projectName string, raw map[string]any) (model.Package, error) {
	config := model.Package{
		Name:      projectName,
		Namespace: projectName,
		Version:   model.DefaultVersion,
	}

	rootUDS, ok := asMap(raw["x-uds"])
	if !ok {
		return config, nil
	}

	if pkg, ok := asMap(rootUDS["package"]); ok {
		if value := strings.TrimSpace(asString(pkg["name"])); value != "" {
			normalized, err := normalizeName(value)
			if err != nil {
				return model.Package{}, fmt.Errorf("invalid x-uds.package.name: %w", err)
			}
			config.Name = normalized
		}
		if value := strings.TrimSpace(asString(pkg["namespace"])); value != "" {
			normalized, err := normalizeName(value)
			if err != nil {
				return model.Package{}, fmt.Errorf("invalid x-uds.package.namespace: %w", err)
			}
			config.Namespace = normalized
		}
		if value := strings.TrimSpace(asString(pkg["version"])); value != "" {
			config.Version = value
		}
	}

	if network, ok := asMap(rootUDS["network"]); ok {
		if expose, ok := asSlice(network["expose"]); ok {
			config.NetworkExpose = append(config.NetworkExpose, expose...)
		}
		if allow, ok := asSlice(network["allow"]); ok {
			config.AdditionalAllow = append(config.AdditionalAllow, allow...)
		}
	}
	if monitor, ok := asSlice(rootUDS["monitor"]); ok {
		config.Monitor = append(config.Monitor, monitor...)
	}
	if len(config.AdditionalAllow) == 0 {
		if allow, ok := asSlice(rootUDS["allow"]); ok {
			config.AdditionalAllow = append(config.AdditionalAllow, allow...)
		}
	}

	if sso, ok := asSlice(rootUDS["sso"]); ok {
		config.SSOConfigured = true
		config.SSO = append(config.SSO, sso...)
	}

	if rawCABundle, exists := rootUDS["caBundle"]; exists {
		caBundle, ok := asMap(rawCABundle)
		if !ok {
			return model.Package{}, fmt.Errorf("invalid x-uds.caBundle: must be an object")
		}

		for key, value := range caBundle {
			if key != "configMap" {
				return model.Package{}, fmt.Errorf("invalid x-uds.caBundle.%s: unsupported field", key)
			}
			configMap, err := normalizeCABundleConfigMap(value)
			if err != nil {
				return model.Package{}, err
			}
			config.CABundle = map[string]any{"configMap": configMap}
		}
	}

	return config, nil
}

func normalizeCABundleConfigMap(value any) (map[string]any, error) {
	configMap, ok := asMap(value)
	if !ok {
		return nil, fmt.Errorf("invalid x-uds.caBundle.configMap: must be an object")
	}

	normalized := map[string]any{}
	for key, raw := range configMap {
		switch key {
		case "name", "key":
			value := strings.TrimSpace(asString(raw))
			if value == "" {
				return nil, fmt.Errorf("invalid x-uds.caBundle.configMap.%s: must be a non-empty string", key)
			}
			normalized[key] = value
		case "labels", "annotations":
			value, err := normalizeStringMap(raw, fmt.Sprintf("x-uds.caBundle.configMap.%s", key))
			if err != nil {
				return nil, err
			}
			normalized[key] = value
		default:
			return nil, fmt.Errorf("invalid x-uds.caBundle.configMap.%s: unsupported field", key)
		}
	}

	return normalized, nil
}

func normalizeStringMap(value any, field string) (map[string]string, error) {
	items, ok := asMap(value)
	if !ok {
		return nil, fmt.Errorf("invalid %s: must be an object", field)
	}

	normalized := make(map[string]string, len(items))
	for key, raw := range items {
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("invalid %s.%s: must be a string", field, key)
		}
		normalized[key] = text
	}
	return normalized, nil
}

func parseEnvironment(raw types.MappingWithEquals) []model.EnvVar {
	if len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]model.EnvVar, 0, len(keys))
	for _, key := range keys {
		value := ""
		if raw[key] != nil {
			value = *raw[key]
		}
		env = append(env, model.EnvVar{Name: key, Value: value})
	}
	return env
}

func copyCommand(command types.ShellCommand) []string {
	if len(command) == 0 {
		return nil
	}
	out := make([]string, 0, len(command))
	for _, token := range command {
		out = append(out, token)
	}
	return out
}

func parseHealthcheck(raw *types.HealthCheckConfig) *model.Healthcheck {
	if raw == nil || raw.Disable || len(raw.Test) == 0 {
		return nil
	}
	tokens := []string(raw.Test)
	mode := strings.ToUpper(strings.TrimSpace(tokens[0]))
	args := tokens[1:]

	command := []string{}
	switch mode {
	case "NONE":
		return nil
	case "CMD-SHELL":
		if len(args) == 0 {
			return nil
		}
		command = []string{"/bin/sh", "-c", strings.Join(args, " ")}
	case "CMD":
		if len(args) == 0 {
			return nil
		}
		command = append(command, args...)
	default:
		command = append(command, tokens...)
	}

	probe := &model.Healthcheck{Command: command}
	if raw.Interval != nil {
		probe.PeriodSeconds = durationSeconds(*raw.Interval)
	}
	if raw.StartPeriod != nil {
		probe.InitialDelaySeconds = durationSeconds(*raw.StartPeriod)
	}
	if raw.Timeout != nil {
		probe.TimeoutSeconds = durationSeconds(*raw.Timeout)
	}
	if raw.Retries != nil {
		probe.FailureThreshold = int(*raw.Retries)
	}
	return probe
}

func durationSeconds(value types.Duration) int {
	seconds := int(time.Duration(value).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func parsePorts(rawPorts []types.ServicePortConfig, expose types.StringOrNumberList) ([]model.Port, error) {
	ports := []model.Port{}
	seen := map[string]struct{}{}
	for _, raw := range rawPorts {
		if raw.Target == 0 {
			continue
		}
		proto := protocolOrDefault(raw.Protocol)
		key := portKey(int(raw.Target), proto)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{
			Number:      int(raw.Target),
			Protocol:    proto,
			Raw:         key,
			Published:   true,
			Name:        strings.TrimSpace(raw.Name),
			AppProtocol: strings.TrimSpace(raw.AppProtocol),
		})
	}

	for _, token := range expose {
		target, proto, err := parsePortToken(token)
		if err != nil {
			return nil, err
		}
		key := portKey(target, proto)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{Number: target, Protocol: proto, Raw: key})
	}

	return ports, nil
}

func portKey(number int, proto string) string {
	return fmt.Sprintf("%d/%s", number, protocolOrDefault(proto))
}

func parsePortToken(token string) (int, string, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, "", fmt.Errorf("port must not be empty")
	}

	proto := "TCP"
	base := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		base = strings.TrimSpace(trimmed[:idx])
		proto = protocolOrDefault(trimmed[idx+1:])
	}

	parts := strings.Split(base, ":")
	target := strings.TrimSpace(parts[len(parts)-1])
	if strings.Contains(target, "-") {
		rangeParts := strings.SplitN(target, "-", 2)
		target = strings.TrimSpace(rangeParts[0])
	}
	port, err := strconv.Atoi(target)
	if err != nil || port <= 0 {
		return 0, "", fmt.Errorf("invalid port %q", token)
	}
	return port, proto, nil
}

func protocolOrDefault(raw string) string {
	trimmed := strings.ToUpper(strings.TrimSpace(raw))
	if trimmed == "" {
		return "TCP"
	}
	return trimmed
}

func parseServiceVolumes(raw []types.ServiceVolumeConfig, serviceName string, aliases map[string]string, volumes map[string]model.Volume) ([]model.VolumeMount, error) {
	out := make([]model.VolumeMount, 0, len(raw))
	for _, mount := range raw {
		mountType := strings.ToLower(strings.TrimSpace(mount.Type))
		source := strings.TrimSpace(mount.Source)
		target := strings.TrimSpace(mount.Target)
		if target == "" {
			return nil, fmt.Errorf("volume.target must be set")
		}
		if mountType == "" {
			if looksLikePath(source) {
				mountType = "bind"
			} else {
				mountType = "volume"
			}
		}

		switch mountType {
		case "bind":
			fmt.Fprintf(os.Stderr, "warning: service %q bind mount %q:%q skipped; bind mounts have no equivalent in Kubernetes\n", serviceName, source, target)
			continue
		case "volume":
			name := strings.TrimSpace(source)
			if name == "" {
				name = anonymousVolumeName(serviceName, target)
				volumes[name] = model.Volume{Name: name}
				registerAlias(aliases, name, name)
			} else {
				resolved, ok := resolveAlias(aliases, name)
				if !ok {
					return nil, fmt.Errorf("unknown top-level volume %q", source)
				}
				name = resolved
			}
			out = append(out, model.VolumeMount{Name: name, Target: target, ReadOnly: mount.ReadOnly})
		default:
			return nil, fmt.Errorf("service %q mount %q uses unsupported type %q; only named volumes are supported", serviceName, target, mountType)
		}
	}
	return out, nil
}

func parseServiceSecrets(raw []types.ServiceSecretConfig, aliases map[string]string) ([]model.FileRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	refs := make([]model.FileRef, 0, len(raw))
	seen := map[string]struct{}{}
	for _, entry := range raw {
		sourceRaw := strings.TrimSpace(entry.Source)
		if sourceRaw == "" {
			return nil, fmt.Errorf("secret source must not be empty")
		}
		source, ok := resolveAlias(aliases, sourceRaw)
		if !ok {
			return nil, fmt.Errorf("unknown top-level secret %q", sourceRaw)
		}
		target := strings.TrimSpace(entry.Target)
		key := source + "::" + target
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, model.FileRef{Source: source, Target: target})
	}
	return refs, nil
}

func parseServiceConfigs(raw []types.ServiceConfigObjConfig, aliases map[string]string) ([]model.FileRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	refs := make([]model.FileRef, 0, len(raw))
	seen := map[string]struct{}{}
	for _, entry := range raw {
		sourceRaw := strings.TrimSpace(entry.Source)
		if sourceRaw == "" {
			return nil, fmt.Errorf("config source must not be empty")
		}
		source, ok := resolveAlias(aliases, sourceRaw)
		if !ok {
			return nil, fmt.Errorf("unknown top-level config %q", sourceRaw)
		}
		target := strings.TrimSpace(entry.Target)
		key := source + "::" + target
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, model.FileRef{Source: source, Target: target})
	}
	return refs, nil
}

func parseDependsOn(raw types.DependsOnConfig, serviceAliases map[string]string) ([]model.Dependency, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	deps := make([]model.Dependency, 0, len(raw))
	seen := map[string]struct{}{}
	for rawName, dep := range raw {
		resolved, ok := resolveAlias(serviceAliases, rawName)
		if !ok {
			return nil, fmt.Errorf("unknown service %q", rawName)
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		condition := strings.TrimSpace(dep.Condition)
		if condition == "" {
			condition = types.ServiceConditionStarted
		}
		deps = append(deps, model.Dependency{Service: resolved, Condition: condition})
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Service < deps[j].Service })
	return deps, nil
}

func parseServiceNetworks(raw map[string]*types.ServiceNetworkConfig, aliases map[string]string) ([]string, error) {
	networks := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for rawName := range raw {
		name, ok := resolveAlias(aliases, rawName)
		if !ok {
			return nil, fmt.Errorf("unknown network %q", rawName)
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		networks = append(networks, name)
	}
	sort.Strings(networks)
	return networks, nil
}

func parseResources(raw *types.DeployConfig) model.Resources {
	if raw == nil {
		return model.Resources{}
	}
	var out model.Resources
	if raw.Resources.Limits != nil {
		out.Limits.CPU = formatNanoCPUs(raw.Resources.Limits.NanoCPUs)
		out.Limits.Memory = formatUnitBytes(raw.Resources.Limits.MemoryBytes)
	}
	if raw.Resources.Reservations != nil {
		out.Requests.CPU = formatNanoCPUs(raw.Resources.Reservations.NanoCPUs)
		out.Requests.Memory = formatUnitBytes(raw.Resources.Reservations.MemoryBytes)
	}
	return out
}

func normalizeTopLevelVolumes(raw types.Volumes) (map[string]model.Volume, map[string]string, error) {
	volumes := map[string]model.Volume{}
	aliases := map[string]string{}
	for key, value := range raw {
		normalized, err := normalizeName(key)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid top-level volume name %q: %w", key, err)
		}
		if _, exists := volumes[normalized]; exists {
			return nil, nil, fmt.Errorf("duplicate normalized top-level volume name %q", normalized)
		}
		volumes[normalized] = model.Volume{Name: normalized, External: bool(value.External)}
		registerAlias(aliases, key, normalized)
		registerAlias(aliases, normalized, normalized)
		if strings.TrimSpace(value.Name) != "" {
			registerAlias(aliases, value.Name, normalized)
		}
	}
	return volumes, aliases, nil
}

func normalizeTopLevelNetworks(raw types.Networks) (map[string]string, error) {
	aliases := map[string]string{}
	seen := map[string]struct{}{}
	for key, network := range raw {
		normalized, err := normalizeName(key)
		if err != nil {
			return nil, fmt.Errorf("invalid top-level network name %q: %w", key, err)
		}
		if _, exists := seen[normalized]; exists {
			return nil, fmt.Errorf("duplicate normalized top-level network name %q", normalized)
		}
		seen[normalized] = struct{}{}
		registerAlias(aliases, key, normalized)
		registerAlias(aliases, normalized, normalized)
		if bool(network.External) {
			fmt.Fprintf(os.Stderr, "warning: external Compose network %q treated as package-local; declare access to external workloads with x-uds.network.allow\n", key)
		}
	}
	return aliases, nil
}

func normalizeTopLevelSecrets(raw types.Secrets) (map[string]model.Secret, map[string]string, error) {
	secrets := map[string]model.Secret{}
	aliases := map[string]string{}
	for key, value := range raw {
		normalized, err := normalizeName(key)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid top-level secret name %q: %w", key, err)
		}
		if _, exists := secrets[normalized]; exists {
			return nil, nil, fmt.Errorf("duplicate normalized top-level secret name %q", normalized)
		}
		secrets[normalized] = model.Secret{Name: normalized, External: bool(value.External)}
		registerAlias(aliases, key, normalized)
		registerAlias(aliases, normalized, normalized)
		if strings.TrimSpace(value.Name) != "" {
			registerAlias(aliases, value.Name, normalized)
		}
	}
	return secrets, aliases, nil
}

func normalizeTopLevelConfigs(raw types.Configs) (map[string]model.Config, map[string]string, error) {
	configs := map[string]model.Config{}
	aliases := map[string]string{}
	for key, value := range raw {
		normalized, err := normalizeName(key)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid top-level config name %q: %w", key, err)
		}
		if _, exists := configs[normalized]; exists {
			return nil, nil, fmt.Errorf("duplicate normalized top-level config name %q", normalized)
		}
		if bool(value.External) && strings.TrimSpace(value.Content) == "" {
			return nil, nil, fmt.Errorf("external compose config %q is not supported", key)
		}
		configs[normalized] = model.Config{Name: normalized, External: bool(value.External), Content: value.Content}
		registerAlias(aliases, key, normalized)
		registerAlias(aliases, normalized, normalized)
		if strings.TrimSpace(value.Name) != "" {
			registerAlias(aliases, value.Name, normalized)
		}
	}
	return configs, aliases, nil
}

func formatNanoCPUs(value types.NanoCPUs) string {
	v := value.Value()
	if v <= 0 {
		return ""
	}
	return strconv.FormatFloat(float64(v), 'f', -1, 32)
}

func formatUnitBytes(value types.UnitBytes) string {
	if int64(value) <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(value), 10)
}

func normalizeProfiles(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, profile := range raw {
		trimmed := strings.TrimSpace(profile)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func looksLikePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "~/") {
		return true
	}
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		return true
	}
	return strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\")
}

func anonymousVolumeName(serviceName string, target string) string {
	base := strings.TrimSpace(target)
	if base == "" {
		base = "data"
	}
	base = strings.TrimPrefix(base, "/")
	base = strings.ReplaceAll(base, "/", "-")
	base = strings.ReplaceAll(base, "_", "-")
	base = strings.ReplaceAll(base, ".", "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "data"
	}
	name, err := normalizeName(fmt.Sprintf("%s-%s", serviceName, base))
	if err != nil {
		return "volume"
	}
	return name
}

func registerAlias(aliases map[string]string, raw string, normalized string) {
	trimmedRaw := strings.TrimSpace(raw)
	if trimmedRaw == "" {
		return
	}
	trimmedNormalized := strings.TrimSpace(normalized)
	if trimmedNormalized == "" {
		return
	}
	aliases[trimmedRaw] = trimmedNormalized
}

func resolveAlias(aliases map[string]string, raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if resolved, ok := aliases[trimmed]; ok {
		return resolved, true
	}
	normalized, err := normalizeName(trimmed)
	if err != nil {
		return "", false
	}
	if resolved, ok := aliases[normalized]; ok {
		return resolved, true
	}
	return "", false
}

var validNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

func normalizeName(name string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	if !validNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("name %q must match %s", name, validNamePattern.String())
	}
	return trimmed, nil
}

func asMap(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

func asSlice(value any) ([]any, bool) {
	v, ok := value.([]any)
	return v, ok
}

func asString(value any) string {
	s, _ := value.(string)
	return s
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func asStringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, entry := range values {
		if s := strings.TrimSpace(asString(entry)); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveRelativePath(baseDir string, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Clean(filepath.Join(home, strings.TrimPrefix(trimmed, "~/")))
		}
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	return filepath.Clean(filepath.Join(baseDir, trimmed))
}
