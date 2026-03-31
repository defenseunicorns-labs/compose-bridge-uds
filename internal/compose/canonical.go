package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	yamlv3 "gopkg.in/yaml.v3"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

func LoadCanonicalFile(path string) (model.App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.App{}, fmt.Errorf("read canonical compose file: %w", err)
	}
	return LoadCanonicalYAML(data)
}

func LoadCanonicalYAML(data []byte) (model.App, error) {
	var project types.Project
	if err := yamlv3.Unmarshal(data, &project); err != nil {
		return model.App{}, fmt.Errorf("decode canonical compose yaml: %w", err)
	}

	var raw map[string]any
	if err := yamlv3.Unmarshal(data, &raw); err != nil {
		return model.App{}, fmt.Errorf("decode canonical compose extensions: %w", err)
	}

	return loadProject(project, raw)
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

	rawServices, _ := asMap(raw["services"])
	services := make([]model.Service, 0, len(keys))
	exposes := []model.Expose{}

	for _, key := range keys {
		rawSvc := project.Services[key]
		serviceName, _ := resolveAlias(serviceAliases, key)

		ports, err := parsePorts(rawSvc.Ports, rawSvc.Expose)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q ports: %w", key, err)
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
		usesBuild := rawSvc.Build != nil
		if usesBuild {
			if err := validateBuildImage(serviceName, projectName, image); err != nil {
				return model.App{}, err
			}
		} else if image == "" {
			return model.App{}, fmt.Errorf("service %q must define image", key)
		}

		rawServiceMap, _ := asMap(rawServices[key])
		serviceExposes, exposeDeclared, err := parseServiceExposes(rawServiceMap, serviceName, ports)
		if err != nil {
			return model.App{}, fmt.Errorf("service %q x-uds.expose: %w", key, err)
		}

		services = append(services, model.Service{
			Name:           serviceName,
			Image:          image,
			UsesBuild:      usesBuild,
			Ports:          ports,
			ExposeDeclared: exposeDeclared,
			Env:            parseEnvironment(rawSvc.Environment),
			User:           strings.TrimSpace(rawSvc.User),
			Command:        copyCommand(rawSvc.Entrypoint),
			Args:           copyCommand(rawSvc.Command),
			Healthcheck:    parseHealthcheck(rawSvc.HealthCheck),
			Volumes:        volumeMounts,
			Secrets:        secretRefs,
			Configs:        configRefs,
			DependsOn:      dependsOn,
			Resources:      parseResources(rawSvc.Deploy),
			Profiles:       normalizeProfiles(rawSvc.Profiles),
		})
		exposes = append(exposes, serviceExposes...)
	}

	return model.App{
		Package:  packageCfg,
		Services: services,
		Volumes:  volumes,
		Secrets:  secrets,
		Configs:  configs,
		Exposes:  exposes,
	}, nil
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
		if allow, ok := asSlice(network["allow"]); ok {
			config.AdditionalAllow = append(config.AdditionalAllow, allow...)
		}
	}
	if len(config.AdditionalAllow) == 0 {
		if allow, ok := asSlice(rootUDS["allow"]); ok {
			config.AdditionalAllow = append(config.AdditionalAllow, allow...)
		}
	}

	return config, nil
}

func validateBuildImage(serviceName string, projectName string, image string) error {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return fmt.Errorf("service %q uses build but has no image; specify a pullable image and run `docker compose build` before conversion", serviceName)
	}

	defaultImage := projectName + "-" + serviceName
	if trimmed == defaultImage || strings.HasPrefix(trimmed, defaultImage+":") {
		return fmt.Errorf("service %q uses build with the default compose image name %q; specify an explicit pullable image and run `docker compose build` before conversion", serviceName, trimmed)
	}

	host := trimmed

	if slash := strings.Index(trimmed, "/"); slash >= 0 {
		host = trimmed[:slash]
	}
	if strings.HasSuffix(host, ".local") {
		return fmt.Errorf("service %q uses build with non-pullable local image %q; use a pullable image reference and run `docker compose build` before conversion", serviceName, trimmed)
	}

	return nil
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
		key := fmt.Sprintf("%d/%s", raw.Target, proto)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{Number: int(raw.Target), Protocol: proto, Raw: key, Published: true})
	}

	for _, token := range expose {
		target, proto, err := parsePortToken(token)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d/%s", target, proto)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{Number: target, Protocol: proto, Raw: key})
	}

	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Number == ports[j].Number {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].Number < ports[j].Number
	})
	return ports, nil
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
			return nil, fmt.Errorf("service %q uses bind mount %q:%q; bind mounts are not supported, use compose configs/secrets or named volumes instead", serviceName, source, target)
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

func parseServiceExposes(rawService map[string]any, serviceName string, ports []model.Port) ([]model.Expose, bool, error) {
	if len(rawService) == 0 {
		return nil, false, nil
	}
	uds, ok := asMap(rawService["x-uds"])
	if !ok {
		return nil, false, nil
	}
	exposeRaw, exists := uds["expose"]
	if !exists {
		return nil, false, nil
	}

	switch typed := exposeRaw.(type) {
	case bool:
		if !typed {
			return nil, true, nil
		}
		expose, err := parseExposeEntry(nil, serviceName, ports)
		if err != nil {
			return nil, true, err
		}
		return []model.Expose{expose}, true, nil
	case map[string]any:
		expose, err := parseExposeEntry(typed, serviceName, ports)
		if err != nil {
			return nil, true, err
		}
		if expose.Port == 0 {
			return nil, true, nil
		}
		return []model.Expose{expose}, true, nil
	case []any:
		out := make([]model.Expose, 0, len(typed))
		for _, entry := range typed {
			entryMap, ok := asMap(entry)
			if !ok {
				return nil, true, fmt.Errorf("expose list entries must be objects")
			}
			expose, err := parseExposeEntry(entryMap, serviceName, ports)
			if err != nil {
				return nil, true, err
			}
			if expose.Port > 0 {
				out = append(out, expose)
			}
		}
		return out, true, nil
	default:
		return nil, true, fmt.Errorf("unsupported expose shape %T", exposeRaw)
	}
}

func parseExposeEntry(raw map[string]any, serviceName string, ports []model.Port) (model.Expose, error) {
	expose := model.Expose{
		Service: serviceName,
		Host:    serviceName,
		Gateway: "tenant",
	}

	if raw != nil {
		if enabled, ok := raw["enabled"].(bool); ok && !enabled {
			return model.Expose{}, nil
		}
		if value := strings.TrimSpace(asString(raw["host"])); value != "" {
			expose.Host = value
		}
		if value := strings.TrimSpace(asString(raw["gateway"])); value != "" {
			expose.Gateway = value
		}
		if value, ok := asInt(raw["port"]); ok {
			expose.Port = value
		}
		if paths := asStringSlice(raw["paths"]); len(paths) > 0 {
			expose.Paths = paths
		} else if path := strings.TrimSpace(asString(raw["path"])); path != "" {
			expose.Paths = []string{path}
		}
	}

	if expose.Port == 0 {
		primary, err := primaryPort(ports)
		if err != nil {
			return model.Expose{}, fmt.Errorf("exposed service has no port; set x-uds.expose.port explicitly")
		}
		expose.Port = primary.Number
	}

	return expose, nil
}

func primaryPort(ports []model.Port) (model.Port, error) {
	if len(ports) == 0 {
		return model.Port{}, fmt.Errorf("no ports available")
	}
	return ports[0], nil
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
