package render

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

const zarfSchemaURL = "https://raw.githubusercontent.com/zarf-dev/zarf/main/zarf.schema.json"

func WritePackage(root string, app model.App) error {
	chartDir := filepath.Join(root, "chart")
	templatesDir := filepath.Join(chartDir, "templates")
	for _, dir := range []string{root, chartDir, templatesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	secretVariables := buildSecretVariables(app.Secrets)
	servicePorts := map[string]int{}
	for _, svc := range app.Services {
		ports := effectiveServicePorts(svc, app.Exposes)
		if len(ports) > 0 {
			servicePorts[svc.Name] = ports[0].Number
		}
	}

	if err := writeYAMLFile(filepath.Join(chartDir, "Chart.yaml"), chartManifest{
		APIVersion:  "v2",
		Name:        app.Package.Name,
		Description: fmt.Sprintf("UDS package chart generated from Docker Compose for %s", app.Package.Name),
		Type:        "application",
		Version:     app.Package.Version,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte("{}\n"), 0o644); err != nil {
		return fmt.Errorf("write chart values: %w", err)
	}

	if err := writeYAMLFile(filepath.Join(templatesDir, "namespace.yaml"), namespaceManifest{
		APIVersion: "v1",
		Kind:       "Namespace",
		Metadata: objectMeta{
			Name: templateNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of": app.Package.Name,
			},
		},
	}); err != nil {
		return err
	}

	for _, name := range sortedVolumeNames(app.Volumes) {
		volume := app.Volumes[name]
		if volume.External {
			continue
		}
		if err := writeYAMLFile(filepath.Join(templatesDir, fmt.Sprintf("pvc-%s.yaml", volume.Name)), persistentVolumeClaimManifest{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
			Metadata: objectMeta{
				Name:      volume.Name,
				Namespace: templateNamespace,
				Labels:    appLabels(app.Package.Name, volume.Name),
			},
			Spec: persistentVolumeClaimSpec{
				AccessModes: []string{"ReadWriteOnce"},
				Resources:   pvcResources{Requests: pvcRequests{Storage: "1Gi"}},
			},
		}); err != nil {
			return err
		}
	}

	for _, name := range sortedConfigNames(app.Configs) {
		config := app.Configs[name]
		if config.External {
			continue
		}
		if err := writeYAMLFile(filepath.Join(templatesDir, fmt.Sprintf("configmap-%s.yaml", config.Name)), configMapManifest{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Metadata: objectMeta{
				Name:      config.Name,
				Namespace: templateNamespace,
				Labels:    appLabels(app.Package.Name, config.Name),
			},
			Data: map[string]string{config.Name: config.Content},
		}); err != nil {
			return err
		}
	}

	for _, name := range sortedSecretNames(app.Secrets) {
		secret := app.Secrets[name]
		variableName := secretVariables[secret.Name]
		if err := writeYAMLFile(filepath.Join(templatesDir, fmt.Sprintf("secret-%s.yaml", secret.Name)), secretManifest{
			APIVersion: "v1",
			Kind:       "Secret",
			Metadata: objectMeta{
				Name:      secret.Name,
				Namespace: templateNamespace,
				Labels:    appLabels(app.Package.Name, secret.Name),
			},
			Type:       "Opaque",
			StringData: map[string]string{secret.Name: fmt.Sprintf("###ZARF_VAR_%s###", variableName)},
		}); err != nil {
			return err
		}
	}

	for _, svc := range app.Services {
		ports := effectiveServicePorts(svc, app.Exposes)
		deployment, err := buildDeployment(app.Package.Name, svc, app.Exposes, servicePorts)
		if err != nil {
			return err
		}
		if err := writeYAMLFile(filepath.Join(templatesDir, fmt.Sprintf("deployment-%s.yaml", svc.Name)), deployment); err != nil {
			return err
		}

		if len(ports) > 0 {
			if err := writeYAMLFile(filepath.Join(templatesDir, fmt.Sprintf("service-%s.yaml", svc.Name)), serviceManifest{
				APIVersion: "v1",
				Kind:       "Service",
				Metadata: objectMeta{
					Name:      svc.Name,
					Namespace: templateNamespace,
					Labels:    appLabels(app.Package.Name, svc.Name),
				},
				Spec: serviceSpec{
					Selector: serviceSelector(svc.Name),
					Ports:    buildServicePorts(ports),
				},
			}); err != nil {
				return err
			}
		}
	}

	if err := writeYAMLFile(filepath.Join(templatesDir, "uds-package.yaml"), buildUDSPackage(app)); err != nil {
		return err
	}

	if err := writeZarfConfig(filepath.Join(root, "zarf.yaml"), app, secretVariables, servicePorts); err != nil {
		return err
	}

	return nil
}

func writeZarfConfig(path string, app model.App, secretVariables map[string]string, servicePorts map[string]int) error {
	images := map[string]struct{}{}
	needsDependencyImage := false
	for _, svc := range app.Services {
		images[strings.TrimSpace(svc.Image)] = struct{}{}
		if len(buildDependencyInitContainers(svc, servicePorts)) > 0 {
			needsDependencyImage = true
		}
	}
	if needsDependencyImage {
		images[model.DependencyInitImage] = struct{}{}
	}

	imageList := make([]string, 0, len(images))
	for image := range images {
		if strings.TrimSpace(image) == "" {
			continue
		}
		imageList = append(imageList, image)
	}
	sort.Strings(imageList)

	variables := []zarfVariable{}
	for _, secretName := range sortedSecretNames(app.Secrets) {
		variables = append(variables, zarfVariable{
			Name:        secretVariables[secretName],
			Description: fmt.Sprintf("Value for compose secret %s", secretName),
			Prompt:      true,
			Sensitive:   true,
		})
	}

	pkg := zarfPackageConfig{
		APIVersion: "zarf.dev/v1alpha1",
		Kind:       "ZarfPackageConfig",
		Metadata: zarfMetadata{
			Name:        app.Package.Name,
			Description: fmt.Sprintf("UDS package generated from Docker Compose for %s", app.Package.Name),
			Version:     app.Package.Version,
		},
		Variables: variables,
		Components: []zarfComponent{
			{
				Name:        app.Package.Name,
				Required:    true,
				Description: fmt.Sprintf("Deploy %s", app.Package.Name),
				Charts: []zarfChart{
					{
						Name:      app.Package.Name,
						Namespace: app.Package.Namespace,
						Version:   app.Package.Version,
						LocalPath: "chart",
					},
				},
				Images: imageList,
			},
		},
	}

	data, err := yamlv3.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("marshal zarf config: %w", err)
	}
	content := append([]byte("# yaml-language-server: $schema="+zarfSchemaURL+"\n"), data...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write zarf config: %w", err)
	}
	return nil
}

func buildDeployment(appName string, svc model.Service, exposes []model.Expose, servicePorts map[string]int) (deploymentManifest, error) {
	ports := effectiveServicePorts(svc, exposes)
	volumes, volumeMounts := buildVolumes(svc)
	resources := buildResources(svc.Resources)
	securityContext := buildSecurityContext(svc.User)
	initContainers := buildDependencyInitContainers(svc, servicePorts)

	container := containerSpec{
		Name:            svc.Name,
		Image:           svc.Image,
		ImagePullPolicy: "IfNotPresent",
		Command:         svc.Command,
		Args:            svc.Args,
		Env:             buildEnv(svc.Env),
		Ports:           buildContainerPorts(ports),
		VolumeMounts:    volumeMounts,
		LivenessProbe:   buildProbe(svc.Healthcheck),
		Resources:       resources,
		SecurityContext: securityContext,
	}

	manifest := deploymentManifest{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Metadata: objectMeta{
			Name:      svc.Name,
			Namespace: templateNamespace,
			Labels:    appLabels(appName, svc.Name),
		},
		Spec: deploymentSpec{
			Replicas: 1,
			Selector: labelSelector{MatchLabels: serviceSelector(svc.Name)},
			Template: podTemplateSpec{
				Metadata: objectMeta{Labels: serviceSelector(svc.Name)},
				Spec: podSpec{
					InitContainers: initContainers,
					Containers:     []containerSpec{container},
					Volumes:        volumes,
				},
			},
		},
	}

	return manifest, nil
}

func buildUDSPackage(app model.App) udsPackageManifest {
	allow := []map[string]any{
		{"direction": "Ingress", "remoteGenerated": "IntraNamespace"},
		{"direction": "Egress", "remoteGenerated": "IntraNamespace"},
	}
	for _, rule := range app.Package.AdditionalAllow {
		if item, ok := rule.(map[string]any); ok {
			allow = append(allow, item)
		}
	}

	exposes := make([]udsExpose, 0, len(app.Exposes))
	for _, expose := range app.Exposes {
		item := udsExpose{
			Service:  expose.Service,
			Host:     expose.Host,
			Gateway:  expose.Gateway,
			Port:     expose.Port,
			Selector: map[string]string{"app.kubernetes.io/name": expose.Service},
		}
		if len(expose.Paths) > 0 {
			item.Uptime = &udsUptime{Checks: udsChecks{Paths: expose.Paths}}
		}
		exposes = append(exposes, item)
	}

	return udsPackageManifest{
		APIVersion: "uds.dev/v1alpha1",
		Kind:       "Package",
		Metadata: objectMeta{
			Name:      app.Package.Name,
			Namespace: templateNamespace,
		},
		Spec: udsPackageSpec{
			Network: udsNetwork{
				ServiceMesh: udsServiceMesh{Mode: "ambient"},
				Expose:      exposes,
				Allow:       allow,
			},
		},
	}
}

func effectiveServicePorts(svc model.Service, exposes []model.Expose) []model.Port {
	ports := append([]model.Port(nil), svc.Ports...)
	seen := map[string]struct{}{}
	for _, port := range ports {
		seen[port.Raw] = struct{}{}
	}
	for _, expose := range exposes {
		if expose.Service != svc.Name || expose.Port <= 0 {
			continue
		}
		key := fmt.Sprintf("%d/TCP", expose.Port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, model.Port{Number: expose.Port, Protocol: "TCP", Raw: key})
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Number == ports[j].Number {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].Number < ports[j].Number
	})
	return ports
}

func buildVolumes(svc model.Service) ([]volumeSpec, []volumeMountSpec) {
	volumes := make([]volumeSpec, 0, len(svc.Volumes)+len(svc.Secrets)+len(svc.Configs))
	mounts := make([]volumeMountSpec, 0, len(svc.Volumes)+len(svc.Secrets)+len(svc.Configs))

	for _, mount := range svc.Volumes {
		volumeName := sanitizeManifestName("volume-" + mount.Name)
		volumes = append(volumes, volumeSpec{
			Name: volumeName,
			PersistentVolumeClaim: &persistentVolumeClaimVolumeSource{
				ClaimName: mount.Name,
			},
		})
		mounts = append(mounts, volumeMountSpec{
			Name:      volumeName,
			MountPath: mount.Target,
			ReadOnly:  mount.ReadOnly,
		})
	}

	for _, ref := range svc.Secrets {
		volumeName := sanitizeManifestName("secret-" + ref.Source)
		volumes = append(volumes, volumeSpec{
			Name: volumeName,
			Secret: &secretVolumeSource{
				SecretName: ref.Source,
				Items:      []keyToPath{{Key: ref.Source, Path: ref.Source}},
			},
		})
		mounts = append(mounts, volumeMountSpec{
			Name:      volumeName,
			MountPath: resolveSecretTargetPath(ref.Source, ref.Target),
			SubPath:   ref.Source,
			ReadOnly:  true,
		})
	}

	for _, ref := range svc.Configs {
		volumeName := sanitizeManifestName("config-" + ref.Source)
		volumes = append(volumes, volumeSpec{
			Name: volumeName,
			ConfigMap: &configMapVolumeSource{
				Name:  ref.Source,
				Items: []keyToPath{{Key: ref.Source, Path: ref.Source}},
			},
		})
		mounts = append(mounts, volumeMountSpec{
			Name:      volumeName,
			MountPath: resolveConfigTargetPath(ref.Source, ref.Target),
			SubPath:   ref.Source,
			ReadOnly:  true,
		})
	}

	return volumes, mounts
}

func buildDependencyInitContainers(svc model.Service, servicePorts map[string]int) []containerSpec {
	if len(svc.DependsOn) == 0 {
		return nil
	}
	containers := []containerSpec{}
	for _, dep := range svc.DependsOn {
		port, ok := servicePorts[dep.Service]
		if !ok || port <= 0 {
			continue
		}
		waitScript := fmt.Sprintf("until nc -z %s %d; do echo waiting for %s:%d; sleep 2; done", dep.Service, port, dep.Service, port)
		containers = append(containers, containerSpec{
			Name:            sanitizeManifestName("wait-" + dep.Service),
			Image:           model.DependencyInitImage,
			ImagePullPolicy: "IfNotPresent",
			Command:         []string{"sh", "-c", waitScript},
		})
	}
	return containers
}

func buildEnv(env []model.EnvVar) []envVar {
	if len(env) == 0 {
		return nil
	}
	out := make([]envVar, 0, len(env))
	for _, item := range env {
		out = append(out, envVar{Name: item.Name, Value: item.Value})
	}
	return out
}

func buildContainerPorts(ports []model.Port) []containerPort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]containerPort, 0, len(ports))
	for i, port := range ports {
		out = append(out, containerPort{
			Name:          buildPortName(i, port),
			ContainerPort: port.Number,
			Protocol:      strings.ToUpper(port.Protocol),
		})
	}
	return out
}

func buildServicePorts(ports []model.Port) []servicePort {
	out := make([]servicePort, 0, len(ports))
	for i, port := range ports {
		out = append(out, servicePort{
			Name:       buildPortName(i, port),
			Port:       port.Number,
			Protocol:   strings.ToUpper(port.Protocol),
			TargetPort: port.Number,
		})
	}
	return out
}

func buildResources(spec model.Resources) *resourceRequirements {
	resources := &resourceRequirements{}
	if strings.TrimSpace(spec.Limits.CPU) != "" || strings.TrimSpace(spec.Limits.Memory) != "" {
		resources.Limits = map[string]string{}
		if strings.TrimSpace(spec.Limits.CPU) != "" {
			resources.Limits["cpu"] = spec.Limits.CPU
		}
		if strings.TrimSpace(spec.Limits.Memory) != "" {
			resources.Limits["memory"] = spec.Limits.Memory
		}
	}
	if strings.TrimSpace(spec.Requests.CPU) != "" || strings.TrimSpace(spec.Requests.Memory) != "" {
		resources.Requests = map[string]string{}
		if strings.TrimSpace(spec.Requests.CPU) != "" {
			resources.Requests["cpu"] = spec.Requests.CPU
		}
		if strings.TrimSpace(spec.Requests.Memory) != "" {
			resources.Requests["memory"] = spec.Requests.Memory
		}
	}
	if len(resources.Limits) == 0 && len(resources.Requests) == 0 {
		return nil
	}
	return resources
}

func buildSecurityContext(user string) *securityContext {
	trimmed := strings.TrimSpace(user)
	if trimmed == "" {
		return nil
	}
	base := trimmed
	if strings.Contains(base, ":") {
		base = strings.SplitN(base, ":", 2)[0]
	}
	if strings.EqualFold(base, "root") {
		return nil
	}
	context := &securityContext{}
	if uid, err := strconv.ParseInt(base, 10, 64); err == nil {
		context.RunAsUser = &uid
		nonRoot := uid != 0
		context.RunAsNonRoot = &nonRoot
		return context
	}
	nonRoot := true
	context.RunAsNonRoot = &nonRoot
	return context
}

func buildProbe(healthcheck *model.Healthcheck) *probe {
	if healthcheck == nil || len(healthcheck.Command) == 0 {
		return nil
	}
	return &probe{
		Exec:                &execAction{Command: healthcheck.Command},
		PeriodSeconds:       healthcheck.PeriodSeconds,
		InitialDelaySeconds: healthcheck.InitialDelaySeconds,
		TimeoutSeconds:      healthcheck.TimeoutSeconds,
		FailureThreshold:    healthcheck.FailureThreshold,
	}
}

func buildSecretVariables(secrets map[string]model.Secret) map[string]string {
	used := map[string]struct{}{}
	out := map[string]string{}
	for _, name := range sortedSecretNames(secrets) {
		out[name] = buildSecretVariableName(name, used)
	}
	return out
}

func buildSecretVariableName(secretName string, used map[string]struct{}) string {
	base := strings.ToUpper(strings.TrimSpace(secretName))
	base = strings.ReplaceAll(base, "-", "_")
	base = strings.ReplaceAll(base, ".", "_")
	base = invalidSecretVariableRunes.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_")
	if base == "" {
		base = "VALUE"
	}
	candidate := base
	for i := 2; ; i++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s_%d", base, i)
	}
}

func buildPortName(index int, port model.Port) string {
	if index == 0 && strings.EqualFold(port.Protocol, "TCP") {
		return "http"
	}
	return fmt.Sprintf("port-%d-%s", port.Number, strings.ToLower(port.Protocol))
}

func resolveSecretTargetPath(source string, target string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return filepath.ToSlash(filepath.Join("/run/secrets", source))
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return filepath.ToSlash(filepath.Join("/run/secrets", trimmed))
}

func resolveConfigTargetPath(source string, target string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return filepath.ToSlash(filepath.Join("/", source))
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return filepath.ToSlash(filepath.Join("/", trimmed))
}

func appLabels(appName string, name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":    name,
		"app.kubernetes.io/part-of": appName,
	}
}

func serviceSelector(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name}
}

func writeYAMLFile(path string, value any) error {
	data, err := yamlv3.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal yaml for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}
	return nil
}

func sortedVolumeNames(volumes map[string]model.Volume) []string {
	keys := make([]string, 0, len(volumes))
	for key := range volumes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSecretNames(secrets map[string]model.Secret) []string {
	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedConfigNames(configs map[string]model.Config) []string {
	keys := make([]string, 0, len(configs))
	for key := range configs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var invalidManifestNameRunes = regexp.MustCompile(`[^a-z0-9.-]+`)
var invalidSecretVariableRunes = regexp.MustCompile(`[^A-Z0-9_]+`)

func sanitizeManifestName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.ReplaceAll(name, "_", "-")
	name = invalidManifestNameRunes.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		return "resource"
	}
	return name
}

const templateNamespace = "{{ .Release.Namespace }}"

type chartManifest struct {
	APIVersion  string `yaml:"apiVersion"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	Version     string `yaml:"version"`
}

type objectMeta struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

type namespaceManifest struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
}

type persistentVolumeClaimManifest struct {
	APIVersion string                    `yaml:"apiVersion"`
	Kind       string                    `yaml:"kind"`
	Metadata   objectMeta                `yaml:"metadata"`
	Spec       persistentVolumeClaimSpec `yaml:"spec"`
}

type persistentVolumeClaimSpec struct {
	AccessModes []string     `yaml:"accessModes"`
	Resources   pvcResources `yaml:"resources"`
}

type pvcResources struct {
	Requests pvcRequests `yaml:"requests"`
}

type pvcRequests struct {
	Storage string `yaml:"storage"`
}

type configMapManifest struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   objectMeta        `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
}

type secretManifest struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   objectMeta        `yaml:"metadata"`
	Type       string            `yaml:"type"`
	StringData map[string]string `yaml:"stringData"`
}

type deploymentManifest struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   objectMeta     `yaml:"metadata"`
	Spec       deploymentSpec `yaml:"spec"`
}

type deploymentSpec struct {
	Replicas int             `yaml:"replicas"`
	Selector labelSelector   `yaml:"selector"`
	Template podTemplateSpec `yaml:"template"`
}

type labelSelector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}

type podTemplateSpec struct {
	Metadata objectMeta `yaml:"metadata"`
	Spec     podSpec    `yaml:"spec"`
}

type podSpec struct {
	InitContainers []containerSpec `yaml:"initContainers,omitempty"`
	Containers     []containerSpec `yaml:"containers"`
	Volumes        []volumeSpec    `yaml:"volumes,omitempty"`
}

type containerSpec struct {
	Name            string                `yaml:"name"`
	Image           string                `yaml:"image"`
	ImagePullPolicy string                `yaml:"imagePullPolicy,omitempty"`
	Command         []string              `yaml:"command,omitempty"`
	Args            []string              `yaml:"args,omitempty"`
	Env             []envVar              `yaml:"env,omitempty"`
	Ports           []containerPort       `yaml:"ports,omitempty"`
	VolumeMounts    []volumeMountSpec     `yaml:"volumeMounts,omitempty"`
	LivenessProbe   *probe                `yaml:"livenessProbe,omitempty"`
	Resources       *resourceRequirements `yaml:"resources,omitempty"`
	SecurityContext *securityContext      `yaml:"securityContext,omitempty"`
}

type envVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type containerPort struct {
	Name          string `yaml:"name,omitempty"`
	ContainerPort int    `yaml:"containerPort"`
	Protocol      string `yaml:"protocol,omitempty"`
}

type volumeMountSpec struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SubPath   string `yaml:"subPath,omitempty"`
	ReadOnly  bool   `yaml:"readOnly,omitempty"`
}

type probe struct {
	Exec                *execAction `yaml:"exec,omitempty"`
	PeriodSeconds       int         `yaml:"periodSeconds,omitempty"`
	InitialDelaySeconds int         `yaml:"initialDelaySeconds,omitempty"`
	TimeoutSeconds      int         `yaml:"timeoutSeconds,omitempty"`
	FailureThreshold    int         `yaml:"failureThreshold,omitempty"`
}

type execAction struct {
	Command []string `yaml:"command"`
}

type resourceRequirements struct {
	Limits   map[string]string `yaml:"limits,omitempty"`
	Requests map[string]string `yaml:"requests,omitempty"`
}

type securityContext struct {
	RunAsNonRoot *bool  `yaml:"runAsNonRoot,omitempty"`
	RunAsUser    *int64 `yaml:"runAsUser,omitempty"`
}

type volumeSpec struct {
	Name                  string                             `yaml:"name"`
	PersistentVolumeClaim *persistentVolumeClaimVolumeSource `yaml:"persistentVolumeClaim,omitempty"`
	Secret                *secretVolumeSource                `yaml:"secret,omitempty"`
	ConfigMap             *configMapVolumeSource             `yaml:"configMap,omitempty"`
}

type persistentVolumeClaimVolumeSource struct {
	ClaimName string `yaml:"claimName"`
}

type secretVolumeSource struct {
	SecretName string      `yaml:"secretName"`
	Items      []keyToPath `yaml:"items,omitempty"`
}

type configMapVolumeSource struct {
	Name  string      `yaml:"name"`
	Items []keyToPath `yaml:"items,omitempty"`
}

type keyToPath struct {
	Key  string `yaml:"key"`
	Path string `yaml:"path"`
}

type serviceManifest struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   objectMeta  `yaml:"metadata"`
	Spec       serviceSpec `yaml:"spec"`
}

type serviceSpec struct {
	Selector map[string]string `yaml:"selector"`
	Ports    []servicePort     `yaml:"ports"`
}

type servicePort struct {
	Name       string `yaml:"name,omitempty"`
	Port       int    `yaml:"port"`
	TargetPort int    `yaml:"targetPort,omitempty"`
	Protocol   string `yaml:"protocol,omitempty"`
}

type udsPackageManifest struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   objectMeta     `yaml:"metadata"`
	Spec       udsPackageSpec `yaml:"spec"`
}

type udsPackageSpec struct {
	Network udsNetwork `yaml:"network"`
}

type udsNetwork struct {
	ServiceMesh udsServiceMesh   `yaml:"serviceMesh"`
	Expose      []udsExpose      `yaml:"expose,omitempty"`
	Allow       []map[string]any `yaml:"allow"`
}

type udsServiceMesh struct {
	Mode string `yaml:"mode"`
}

type udsExpose struct {
	Service  string            `yaml:"service"`
	Selector map[string]string `yaml:"selector,omitempty"`
	Gateway  string            `yaml:"gateway"`
	Host     string            `yaml:"host"`
	Port     int               `yaml:"port"`
	Uptime   *udsUptime        `yaml:"uptime,omitempty"`
}

type udsUptime struct {
	Checks udsChecks `yaml:"checks"`
}

type udsChecks struct {
	Paths []string `yaml:"paths"`
}

type zarfPackageConfig struct {
	APIVersion string          `yaml:"apiVersion,omitempty"`
	Kind       string          `yaml:"kind"`
	Metadata   zarfMetadata    `yaml:"metadata"`
	Variables  []zarfVariable  `yaml:"variables,omitempty"`
	Components []zarfComponent `yaml:"components"`
}

type zarfMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Version     string `yaml:"version"`
}

type zarfVariable struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Prompt      bool   `yaml:"prompt,omitempty"`
	Sensitive   bool   `yaml:"sensitive,omitempty"`
}

type zarfComponent struct {
	Name        string      `yaml:"name"`
	Required    bool        `yaml:"required"`
	Description string      `yaml:"description,omitempty"`
	Charts      []zarfChart `yaml:"charts,omitempty"`
	Images      []string    `yaml:"images,omitempty"`
}

type zarfChart struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace,omitempty"`
	Version   string `yaml:"version,omitempty"`
	LocalPath string `yaml:"localPath,omitempty"`
}
