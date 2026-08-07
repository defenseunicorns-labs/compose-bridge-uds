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

const (
	// chartDirName is the Helm chart directory written under the package root.
	chartDirName = "chart"
	// zarfValuesRel is the Zarf-templated values file (relative to the package
	// root) referenced by the component's chart valuesFiles. Only written when
	// the package has secrets.
	zarfValuesRel = "values/values.yaml"
	// secretValuePlaceholder is the sentinel injected into a marshaled Secret's
	// stringData and then replaced with a Helm value reference, so the Secret can
	// be rendered by Helm from chart values rather than a static literal.
	secretValuePlaceholder    = "__HELM_SECRET_VALUE__"
	composeNetworkLabelPrefix = "network.compose.bridge.uds.dev/"
)

func WritePackage(root string, app model.App) error {
	chartDir := filepath.Join(root, chartDirName)
	templatesDir := filepath.Join(chartDir, "templates")
	for _, dir := range []string{root, chartDir, templatesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	secretVariables := buildSecretVariables(app.Secrets)
	preserveNetworkMembership := hasDistinctNetworkMemberships(app.Services)
	servicePorts := map[string]int{}
	for _, svc := range app.Services {
		if len(svc.Ports) > 0 {
			servicePorts[svc.Name] = svc.Ports[0].Number
		}
	}

	if err := writeYAMLFile(filepath.Join(templatesDir, "namespace.yaml"), namespaceManifest{
		APIVersion: "v1",
		Kind:       "Namespace",
		Metadata: objectMeta{
			Name: app.Package.Namespace,
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
				Namespace: app.Package.Namespace,
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
				Namespace: app.Package.Namespace,
				Labels:    appLabels(app.Package.Name, config.Name),
			},
			Data: map[string]string{config.Name: config.Content},
		}); err != nil {
			return err
		}
	}

	// Secrets are rendered by Helm from chart values so Zarf can substitute the
	// sensitive value into the values file at deploy time (Zarf only templates
	// chart valuesFiles, not arbitrary template files). secretValueKeys tracks the
	// values keys used so the chart defaults and the Zarf values file stay in sync.
	secretValueKeys := make([]string, 0, len(app.Secrets))
	for _, name := range sortedSecretNames(app.Secrets) {
		secret := app.Secrets[name]
		if secret.External {
			continue
		}
		variableName := secretVariables[secret.Name]
		if err := writeSecretTemplate(filepath.Join(templatesDir, fmt.Sprintf("secret-%s.yaml", secret.Name)), app, secret, variableName); err != nil {
			return err
		}
		secretValueKeys = append(secretValueKeys, variableName)
	}

	for _, svc := range app.Services {
		deployment, err := buildDeployment(app.Package.Name, app.Package.Namespace, svc, servicePorts, preserveNetworkMembership)
		if err != nil {
			return err
		}
		if err := writeYAMLFile(filepath.Join(templatesDir, fmt.Sprintf("deployment-%s.yaml", svc.Name)), deployment); err != nil {
			return err
		}
		if err := writeYAMLFile(filepath.Join(templatesDir, fmt.Sprintf("service-%s.yaml", svc.Name)), buildService(app.Package.Name, app.Package.Namespace, svc)); err != nil {
			return err
		}
	}

	udsPackage, err := buildUDSPackage(app)
	if err != nil {
		return err
	}
	if err := writeYAMLFile(filepath.Join(templatesDir, "uds-package.yaml"), udsPackage); err != nil {
		return err
	}

	if exemption := buildUDSExemption(app); exemption != nil {
		if err := writeYAMLFile(filepath.Join(templatesDir, "uds-exemption.yaml"), exemption); err != nil {
			return err
		}
	}

	if err := writeChartMetadata(filepath.Join(chartDir, "Chart.yaml"), app); err != nil {
		return err
	}
	if err := writeChartValues(filepath.Join(chartDir, "values.yaml"), secretValueKeys, false); err != nil {
		return err
	}

	hasSecrets := len(secretValueKeys) > 0
	if hasSecrets {
		valuesPath := filepath.Join(root, filepath.FromSlash(zarfValuesRel))
		if err := os.MkdirAll(filepath.Dir(valuesPath), 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", filepath.Dir(valuesPath), err)
		}
		if err := writeChartValues(valuesPath, secretValueKeys, true); err != nil {
			return err
		}
	}

	images := make([]string, 0, len(app.Services))
	for _, svc := range app.Services {
		images = append(images, buildComponentImages(svc, servicePorts)...)
	}

	if err := writeZarfConfig(filepath.Join(root, "zarf.yaml"), app, secretVariables, dedupeStrings(images), hasSecrets); err != nil {
		return err
	}

	return nil
}

// writeSecretTemplate writes a Helm-templated Secret whose value is sourced from
// chart values (.Values.secrets.<variableName>) rather than a static literal, so
// Zarf can inject the sensitive value via the chart values file at deploy time.
func writeSecretTemplate(path string, app model.App, secret model.Secret, variableName string) error {
	data, err := yamlv3.Marshal(secretManifest{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata: objectMeta{
			Name:      secret.Name,
			Namespace: app.Package.Namespace,
			Labels:    appLabels(app.Package.Name, secret.Name),
		},
		Type:       "Opaque",
		StringData: map[string]string{secret.Name: secretValuePlaceholder},
	})
	if err != nil {
		return fmt.Errorf("marshal yaml for %s: %w", path, err)
	}
	rendered := strings.ReplaceAll(string(data), secretValuePlaceholder,
		fmt.Sprintf("{{ .Values.secrets.%s | quote }}", variableName))
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}
	return nil
}

// writeChartMetadata writes the generated chart's Chart.yaml.
func writeChartMetadata(path string, app model.App) error {
	return writeYAMLFile(path, chartMetadata{
		APIVersion:  "v2",
		Name:        app.Package.Name,
		Description: fmt.Sprintf("UDS package generated from Docker Compose for %s", app.Package.Name),
		Type:        "application",
		Version:     app.Package.Version,
		AppVersion:  app.Package.Version,
	})
}

// writeChartValues writes a values document mapping each secret values key to a
// default (empty) value, or to a Zarf variable placeholder when placeholder is set.
func writeChartValues(path string, secretKeys []string, placeholder bool) error {
	secrets := map[string]string{}
	for _, key := range secretKeys {
		if placeholder {
			secrets[key] = fmt.Sprintf("###ZARF_VAR_%s###", key)
		} else {
			secrets[key] = ""
		}
	}
	return writeYAMLFile(path, chartValues{Secrets: secrets})
}

func writeZarfConfig(path string, app model.App, secretVariables map[string]string, images []string, hasSecrets bool) error {
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
	}

	chart := zarfChart{
		Name:      app.Package.Name,
		Namespace: app.Package.Namespace,
		LocalPath: chartDirName,
		Version:   app.Package.Version,
	}
	if hasSecrets {
		chart.ValuesFiles = []string{zarfValuesRel}
	}
	pkg.Components = append(pkg.Components, zarfComponent{
		Name:        app.Package.Name,
		Required:    true,
		Description: fmt.Sprintf("Deploy %s", app.Package.Name),
		Charts:      []zarfChart{chart},
		Images:      dedupeStrings(images),
	})

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

func buildDeployment(appName string, namespace string, svc model.Service, servicePorts map[string]int, preserveNetworkMembership bool) (deploymentManifest, error) {
	ports := svc.Ports
	volumes, volumeMounts := buildVolumes(svc)
	resources := buildResources(svc.Resources)
	securityContext := buildSecurityContext(svc)
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

	podLabels := serviceSelector(svc.Name)
	if preserveNetworkMembership {
		for key, value := range composeNetworkLabels(svc.Networks) {
			podLabels[key] = value
		}
	}

	manifest := deploymentManifest{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Metadata: objectMeta{
			Name:      svc.Name,
			Namespace: namespace,
			Labels:    appLabels(appName, svc.Name),
		},
		Spec: deploymentSpec{
			Replicas: 1,
			Selector: labelSelector{MatchLabels: serviceSelector(svc.Name)},
			Template: podTemplateSpec{
				Metadata: objectMeta{Labels: podLabels},
				Spec: podSpec{
					Hostname:       svc.Hostname,
					InitContainers: initContainers,
					Containers:     []containerSpec{container},
					Volumes:        volumes,
				},
			},
		},
	}

	return manifest, nil
}

func buildService(appName string, namespace string, svc model.Service) serviceManifest {
	ports := svc.Ports
	manifest := serviceManifest{
		APIVersion: "v1",
		Kind:       "Service",
		Metadata: objectMeta{
			Name:      svc.Name,
			Namespace: namespace,
			Labels:    appLabels(appName, svc.Name),
		},
		Spec: serviceSpec{
			Selector: serviceSelector(svc.Name),
		},
	}
	if len(ports) == 0 {
		manifest.Spec.ClusterIP = "None"
		return manifest
	}
	manifest.Spec.Ports = buildServicePorts(ports)
	return manifest
}

func buildUDSPackage(app model.App) (udsPackageManifest, error) {
	// --- Allow rules ---
	allow := buildNetworkAllowRules(app)
	for _, rule := range app.Package.AdditionalAllow {
		if item, ok := rule.(map[string]any); ok {
			allow = append(allow, item)
		}
	}

	// --- Expose rules ---
	var expose []any
	if len(app.Package.NetworkExpose) > 0 {
		expose = enrichNetworkExposes(app)
	} else {
		expose = buildAutoExposes(app)
	}

	// --- SSO ---
	sso := buildSSO(app)
	monitor, err := buildMonitor(app)
	if err != nil {
		return udsPackageManifest{}, err
	}

	spec := udsPackageSpec{
		Network: udsNetwork{
			ServiceMesh: udsServiceMesh{Mode: "ambient"},
			Expose:      expose,
			Allow:       allow,
		},
	}
	if len(app.Package.CABundle) > 0 {
		spec.CABundle = app.Package.CABundle
	}
	if len(sso) > 0 {
		spec.SSO = sso
	}
	if len(monitor) > 0 {
		spec.Monitor = monitor
	}

	return udsPackageManifest{
		APIVersion: "uds.dev/v1alpha1",
		Kind:       "Package",
		Metadata: objectMeta{
			Name:      app.Package.Name,
			Namespace: app.Package.Namespace,
		},
		Spec: spec,
	}, nil
}

func buildNetworkAllowRules(app model.App) []map[string]any {
	if !hasDistinctNetworkMemberships(app.Services) {
		return []map[string]any{
			{"direction": "Ingress", "remoteGenerated": "IntraNamespace"},
			{"direction": "Egress", "remoteGenerated": "IntraNamespace"},
		}
	}

	var allow []map[string]any
	for _, network := range sortedServiceNetworks(app.Services) {
		labelKey := composeNetworkLabelPrefix + network
		for _, direction := range []string{"Ingress", "Egress"} {
			allow = append(allow, map[string]any{
				"description":     fmt.Sprintf("compose-%s-%s", network, strings.ToLower(direction)),
				"direction":       direction,
				"selector":        map[string]string{labelKey: "true"},
				"remoteNamespace": app.Package.Namespace,
				"remoteSelector":  map[string]string{labelKey: "true"},
			})
		}
	}
	return allow
}

func hasDistinctNetworkMemberships(services []model.Service) bool {
	if len(services) < 2 {
		return false
	}
	first := strings.Join(services[0].Networks, ",")
	for _, svc := range services[1:] {
		if strings.Join(svc.Networks, ",") != first {
			return true
		}
	}
	return false
}

func sortedServiceNetworks(services []model.Service) []string {
	seen := map[string]struct{}{}
	for _, svc := range services {
		for _, network := range svc.Networks {
			seen[network] = struct{}{}
		}
	}
	networks := make([]string, 0, len(seen))
	for network := range seen {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	return networks
}

func composeNetworkLabels(networks []string) map[string]string {
	labels := make(map[string]string, len(networks))
	for _, network := range networks {
		labels[composeNetworkLabelPrefix+network] = "true"
	}
	return labels
}

// buildServiceIndex creates a map from service name to Service for O(1) lookup.
func buildServiceIndex(services []model.Service) map[string]model.Service {
	idx := make(map[string]model.Service, len(services))
	for _, svc := range services {
		idx[svc.Name] = svc
	}
	return idx
}

// setDefault sets key in m only if it is not already present.
func setDefault(m map[string]any, key string, value any) {
	if _, exists := m[key]; !exists {
		m[key] = value
	}
}

// buildAutoExposes generates tenant-gateway expose entries for services with published ports.
func buildAutoExposes(app model.App) []any {
	var expose []any
	for _, svc := range app.Services {
		port, ok := primaryPublishedPort(svc.Ports)
		if !ok {
			continue
		}
		svcSelector := map[string]string{"app.kubernetes.io/name": svc.Name}
		expose = append(expose, map[string]any{
			"service":   svc.Name,
			"host":      svc.Name,
			"gateway":   "tenant",
			"port":      port.Number,
			"selector":  svcSelector,
			"podLabels": svcSelector,
		})
	}
	return expose
}

// enrichNetworkExposes fills in missing fields on user-provided x-uds.network.expose entries.
func enrichNetworkExposes(app model.App) []any {
	serviceByName := buildServiceIndex(app.Services)
	enriched := make([]any, 0, len(app.Package.NetworkExpose))

	for _, raw := range app.Package.NetworkExpose {
		item, ok := raw.(map[string]any)
		if !ok {
			enriched = append(enriched, raw)
			continue
		}

		serviceName, _ := item["service"].(string)
		setDefault(item, "gateway", "tenant")
		setDefault(item, "host", serviceName)

		if svc, found := serviceByName[serviceName]; found {
			svcSelector := map[string]string{"app.kubernetes.io/name": svc.Name}
			setDefault(item, "selector", svcSelector)
			setDefault(item, "podLabels", svcSelector)
			if _, exists := item["port"]; !exists {
				if port, ok := primaryGatewayPort(svc.Ports, false); ok {
					item["port"] = port.Number
				} else if port, err := svc.PrimaryPort(); err == nil {
					item["port"] = port.Number
				}
			}
		}

		enriched = append(enriched, item)
	}
	return enriched
}

// buildSSO generates, disables, or enriches SSO configuration.
func buildSSO(app model.App) []any {
	if app.Package.SSOConfigured {
		if len(app.Package.SSO) == 0 {
			return nil
		}
		return enrichSSOEntries(app)
	}
	return buildInferredSSO(app)
}

func buildMonitor(app model.App) ([]any, error) {
	if len(app.Package.Monitor) == 0 {
		return nil, nil
	}

	serviceByName := buildServiceIndex(app.Services)
	enriched := make([]any, 0, len(app.Package.Monitor))

	for _, raw := range app.Package.Monitor {
		item, ok := raw.(map[string]any)
		if !ok {
			enriched = append(enriched, raw)
			continue
		}

		entry := cloneAnyMap(item)
		serviceRaw, hasService := entry["service"]
		serviceName := strings.TrimSpace(rawString(serviceRaw))
		if !hasService {
			enriched = append(enriched, entry)
			continue
		}
		if serviceName == "" {
			return nil, fmt.Errorf("x-uds.monitor service must be a non-empty string")
		}

		svc, found := serviceByName[serviceName]
		if !found {
			return nil, fmt.Errorf("x-uds.monitor service %q does not match a compose service", serviceName)
		}

		delete(entry, "service")
		selector := serviceSelector(svc.Name)
		setDefault(entry, "selector", selector)
		setDefault(entry, "podSelector", selector)
		setDefault(entry, "kind", "ServiceMonitor")
		setDefault(entry, "path", "/metrics")
		if err := enrichMonitorPortFields(entry, svc); err != nil {
			return nil, fmt.Errorf("x-uds.monitor service %q: %w", serviceName, err)
		}
		defaultMonitorAuthorization(entry)

		enriched = append(enriched, entry)
	}

	return enriched, nil
}

func enrichMonitorPortFields(entry map[string]any, svc model.Service) error {
	ports := monitorPortsForService(svc)
	if len(ports) == 0 {
		return fmt.Errorf("service has no declared TCP ports for monitor inference")
	}

	portName, hasPortName := lookupRawString(entry, "portName")
	targetPort, hasTargetPort := lookupRawInt(entry, "targetPort")

	switch {
	case hasPortName && hasTargetPort:
		matchedByName, ok := findMonitorPortByName(ports, portName)
		if !ok {
			return fmt.Errorf("portName %q does not match any declared service port (%s)", portName, formatMonitorPorts(ports))
		}
		if matchedByName.Number != targetPort {
			return fmt.Errorf("portName %q resolves to %d, but targetPort is %d", portName, matchedByName.Number, targetPort)
		}
		entry["portName"] = matchedByName.Name
		entry["targetPort"] = matchedByName.Number
	case hasPortName:
		matchedByName, ok := findMonitorPortByName(ports, portName)
		if !ok {
			return fmt.Errorf("portName %q does not match any declared service port (%s)", portName, formatMonitorPorts(ports))
		}
		entry["portName"] = matchedByName.Name
		entry["targetPort"] = matchedByName.Number
	case hasTargetPort:
		matchedByNumber, ok := findMonitorPortByNumber(ports, targetPort)
		if !ok {
			return fmt.Errorf("targetPort %d does not match any declared service port (%s)", targetPort, formatMonitorPorts(ports))
		}
		entry["portName"] = matchedByNumber.Name
		entry["targetPort"] = matchedByNumber.Number
	default:
		if len(ports) != 1 {
			return fmt.Errorf("service has multiple declared TCP ports; set portName or targetPort (%s)", formatMonitorPorts(ports))
		}
		entry["portName"] = ports[0].Name
		entry["targetPort"] = ports[0].Number
	}

	return nil
}

func defaultMonitorAuthorization(entry map[string]any) {
	authorization, ok := entry["authorization"].(map[string]any)
	if !ok {
		return
	}
	authorizationCopy := cloneAnyMap(authorization)
	setDefault(authorizationCopy, "type", "Bearer")
	entry["authorization"] = authorizationCopy
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return map[string]any{}
	}
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

type monitorPort struct {
	Name   string
	Number int
}

func monitorPortsForService(svc model.Service) []monitorPort {
	ports := make([]monitorPort, 0, len(svc.Ports))
	for i, port := range svc.Ports {
		if !strings.EqualFold(port.Protocol, "TCP") {
			continue
		}
		ports = append(ports, monitorPort{
			Name:   buildPortName(i, port),
			Number: port.Number,
		})
	}
	return ports
}

func findMonitorPortByName(ports []monitorPort, name string) (monitorPort, bool) {
	for _, port := range ports {
		if port.Name == name {
			return port, true
		}
	}
	return monitorPort{}, false
}

func findMonitorPortByNumber(ports []monitorPort, number int) (monitorPort, bool) {
	for _, port := range ports {
		if port.Number == number {
			return port, true
		}
	}
	return monitorPort{}, false
}

func formatMonitorPorts(ports []monitorPort) string {
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, fmt.Sprintf("%s=%d", port.Name, port.Number))
	}
	return strings.Join(values, ", ")
}

func lookupRawString(values map[string]any, key string) (string, bool) {
	raw, exists := values[key]
	if !exists {
		return "", false
	}
	text := strings.TrimSpace(rawString(raw))
	if text == "" {
		return "", false
	}
	return text, true
}

func rawString(value any) string {
	text, _ := value.(string)
	return text
}

func lookupRawInt(values map[string]any, key string) (int, bool) {
	raw, exists := values[key]
	if !exists {
		return 0, false
	}
	switch typed := raw.(type) {
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

// buildInferredSSO generates a default SSO client from the app's expose rules.
func buildInferredSSO(app model.App) []any {
	host, service := findPrimaryExposedService(app)
	if host == "" {
		return nil
	}
	return []any{
		map[string]any{
			"clientId": app.Package.Name,
			"name":     titleCase(app.Package.Name),
			"redirectUris": []any{
				fmt.Sprintf("https://%s.uds.dev/*", host),
			},
			"enableAuthserviceSelector": map[string]string{
				"app.kubernetes.io/name": service,
			},
		},
	}
}

// enrichSSOEntries fills in missing fields on user-provided x-uds.sso entries.
func enrichSSOEntries(app model.App) []any {
	host, service := findPrimaryExposedService(app)
	enriched := make([]any, 0, len(app.Package.SSO))

	for _, raw := range app.Package.SSO {
		item, ok := raw.(map[string]any)
		if !ok {
			enriched = append(enriched, raw)
			continue
		}
		setDefault(item, "clientId", app.Package.Name)
		setDefault(item, "name", titleCase(app.Package.Name))
		if host != "" {
			setDefault(item, "redirectUris", []any{
				fmt.Sprintf("https://%s.uds.dev/*", host),
			})
		}
		if service != "" {
			setDefault(item, "enableAuthserviceSelector", map[string]string{
				"app.kubernetes.io/name": service,
			})
		}
		enriched = append(enriched, item)
	}
	return enriched
}

// findPrimaryExposedService determines the primary host and service name from expose rules.
func findPrimaryExposedService(app model.App) (host string, service string) {
	if len(app.Package.NetworkExpose) > 0 {
		for _, raw := range app.Package.NetworkExpose {
			if item, ok := raw.(map[string]any); ok {
				h, _ := item["host"].(string)
				s, _ := item["service"].(string)
				if h == "" {
					h = s
				}
				return h, s
			}
		}
	}
	for _, svc := range app.Services {
		if _, ok := primaryPublishedPort(svc.Ports); ok {
			return svc.Name, svc.Name
		}
	}
	return "", ""
}

// titleCase converts a hyphenated name to Title Case (e.g. "hello-world" → "Hello World").
func titleCase(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func primaryPublishedPort(ports []model.Port) (model.Port, bool) {
	return primaryGatewayPort(ports, true)
}

func primaryGatewayPort(ports []model.Port, requirePublished bool) (model.Port, bool) {
	for _, port := range ports {
		if gatewayPortCandidate(port, requirePublished) && port.HasWebHint() {
			return port, true
		}
	}
	for _, port := range ports {
		if gatewayPortCandidate(port, requirePublished) {
			return port, true
		}
	}
	return model.Port{}, false
}

func gatewayPortCandidate(port model.Port, requirePublished bool) bool {
	return !requirePublished || port.Published
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

func buildComponentImages(svc model.Service, servicePorts map[string]int) []string {
	images := []string{svc.Image}
	if len(buildDependencyInitContainers(svc, servicePorts)) > 0 {
		images = append(images, model.DependencyInitImage)
	}
	return dedupeStrings(images)
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
			Name:        buildPortName(i, port),
			Port:        port.Number,
			Protocol:    strings.ToUpper(port.Protocol),
			TargetPort:  port.Number,
			AppProtocol: port.AppProtocol,
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

func buildSecurityContext(svc model.Service) *securityContext {
	ctx := &securityContext{}
	hasAny := false

	user := strings.TrimSpace(svc.User)
	if user != "" {
		base := user
		if strings.Contains(base, ":") {
			base = strings.SplitN(base, ":", 2)[0]
		}
		if !strings.EqualFold(base, "root") {
			if uid, err := strconv.ParseInt(base, 10, 64); err == nil {
				ctx.RunAsUser = &uid
				nonRoot := uid != 0
				ctx.RunAsNonRoot = &nonRoot
			} else {
				nonRoot := true
				ctx.RunAsNonRoot = &nonRoot
			}
			hasAny = true
		}
	}

	if svc.Privileged {
		t := true
		ctx.Privileged = &t
		hasAny = true
	}

	if len(svc.CapAdd) > 0 || len(svc.CapDrop) > 0 {
		ctx.Capabilities = &capabilitiesSpec{
			Add:  svc.CapAdd,
			Drop: svc.CapDrop,
		}
		hasAny = true
	}

	if !hasAny {
		return nil
	}
	return ctx
}

func isRootUser(user string) bool {
	trimmed := strings.TrimSpace(user)
	if trimmed == "" {
		return false
	}
	base := trimmed
	if strings.Contains(base, ":") {
		base = strings.SplitN(base, ":", 2)[0]
	}
	base = strings.TrimSpace(base)
	if strings.EqualFold(base, "root") {
		return true
	}
	uid, err := strconv.ParseInt(base, 10, 64)
	return err == nil && uid == 0
}

func hasSeccompUnconfined(securityOpts []string) bool {
	for _, opt := range securityOpts {
		normalized := strings.ToLower(strings.TrimSpace(opt))
		if normalized == "seccomp:unconfined" || normalized == "seccomp=unconfined" {
			return true
		}
	}
	return false
}

func serviceExemptionType(svc model.Service) string {
	var types []string
	if isRootUser(svc.User) {
		types = append(types, "root user")
	}
	if svc.Privileged {
		types = append(types, "privileged")
	}
	if len(svc.CapAdd) > 0 {
		types = append(types, "Linux capabilities")
	}
	if hasSeccompUnconfined(svc.SecurityOpts) {
		types = append(types, "seccomp")
	}
	return strings.Join(types, " and ")
}

func serviceExemptionPolicies(svc model.Service) []string {
	var policies []string
	if isRootUser(svc.User) {
		policies = append(policies, "RequireNonRootUser")
	}
	if svc.Privileged {
		policies = append(policies, "DisallowPrivileged")
	}
	if len(svc.CapAdd) > 0 {
		policies = append(policies, "DropAllCapabilities", "RestrictCapabilities")
	}
	if hasSeccompUnconfined(svc.SecurityOpts) {
		policies = append(policies, "RestrictSeccomp")
	}
	return policies
}

func buildUDSExemption(app model.App) *udsExemptionManifest {
	var exemptions []udsExemptionEntry
	for _, svc := range app.Services {
		policies := serviceExemptionPolicies(svc)
		if len(policies) == 0 {
			continue
		}
		exemptions = append(exemptions, udsExemptionEntry{
			Title: fmt.Sprintf("%s policy exemption for %s %s", serviceExemptionType(svc), app.Package.Name, svc.Name),
			Matcher: udsExemptionMatcher{
				Kind:      "pod",
				Namespace: app.Package.Namespace,
				Name:      fmt.Sprintf("^%s-.*", svc.Name),
			},
			Policies: policies,
		})
	}
	if len(exemptions) == 0 {
		return nil
	}
	return &udsExemptionManifest{
		APIVersion: "uds.dev/v1alpha1",
		Kind:       "Exemption",
		Metadata: objectMeta{
			Name:      app.Package.Name,
			Namespace: "uds-policy-exemptions",
		},
		Spec: udsExemptionSpec{
			Exemptions: exemptions,
		},
	}
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
	if name := sanitizePortName(port.Name); name != "" {
		return name
	}
	if index == 0 && strings.EqualFold(port.Protocol, "TCP") {
		return "http"
	}
	return fmt.Sprintf("port-%d-%s", port.Number, strings.ToLower(port.Protocol))
}

func sanitizePortName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.ReplaceAll(name, "_", "-")
	name = invalidPortNameRunes.ReplaceAllString(name, "-")
	name = repeatedPortNameHyphens.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if len(name) > 15 {
		name = strings.Trim(name[:15], "-")
	}
	if !portNameLetter.MatchString(name) {
		return ""
	}
	return name
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

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

var invalidManifestNameRunes = regexp.MustCompile(`[^a-z0-9.-]+`)
var invalidPortNameRunes = regexp.MustCompile(`[^a-z0-9-]+`)
var repeatedPortNameHyphens = regexp.MustCompile(`-+`)
var portNameLetter = regexp.MustCompile(`[a-z]`)
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
	Hostname       string          `yaml:"hostname,omitempty"`
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
	RunAsNonRoot *bool             `yaml:"runAsNonRoot,omitempty"`
	RunAsUser    *int64            `yaml:"runAsUser,omitempty"`
	Privileged   *bool             `yaml:"privileged,omitempty"`
	Capabilities *capabilitiesSpec `yaml:"capabilities,omitempty"`
}

type capabilitiesSpec struct {
	Add  []string `yaml:"add,omitempty"`
	Drop []string `yaml:"drop,omitempty"`
}

type udsExemptionManifest struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   objectMeta       `yaml:"metadata"`
	Spec       udsExemptionSpec `yaml:"spec"`
}

type udsExemptionSpec struct {
	Exemptions []udsExemptionEntry `yaml:"exemptions"`
}

type udsExemptionEntry struct {
	Title    string              `yaml:"title"`
	Matcher  udsExemptionMatcher `yaml:"matcher"`
	Policies []string            `yaml:"policies"`
}

type udsExemptionMatcher struct {
	Kind      string `yaml:"kind"`
	Namespace string `yaml:"namespace"`
	Name      string `yaml:"name"`
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
	ClusterIP string            `yaml:"clusterIP,omitempty"`
	Selector  map[string]string `yaml:"selector"`
	Ports     []servicePort     `yaml:"ports,omitempty"`
}

type servicePort struct {
	Name        string `yaml:"name,omitempty"`
	Port        int    `yaml:"port"`
	TargetPort  int    `yaml:"targetPort,omitempty"`
	Protocol    string `yaml:"protocol,omitempty"`
	AppProtocol string `yaml:"appProtocol,omitempty"`
}

type udsPackageManifest struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   objectMeta     `yaml:"metadata"`
	Spec       udsPackageSpec `yaml:"spec"`
}

type udsPackageSpec struct {
	Network  udsNetwork     `yaml:"network"`
	Monitor  []any          `yaml:"monitor,omitempty"`
	SSO      []any          `yaml:"sso,omitempty"`
	CABundle map[string]any `yaml:"caBundle,omitempty"`
}

type udsNetwork struct {
	ServiceMesh udsServiceMesh   `yaml:"serviceMesh"`
	Expose      []any            `yaml:"expose,omitempty"`
	Allow       []map[string]any `yaml:"allow"`
}

type udsServiceMesh struct {
	Mode string `yaml:"mode"`
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
	Name        string   `yaml:"name"`
	Namespace   string   `yaml:"namespace"`
	LocalPath   string   `yaml:"localPath"`
	Version     string   `yaml:"version"`
	ValuesFiles []string `yaml:"valuesFiles,omitempty"`
}

// chartMetadata is the Chart.yaml written for the generated Helm chart.
type chartMetadata struct {
	APIVersion  string `yaml:"apiVersion"`
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Type        string `yaml:"type,omitempty"`
	Version     string `yaml:"version"`
	AppVersion  string `yaml:"appVersion,omitempty"`
}

// chartValues is the values document written for both the chart defaults
// (chart/values.yaml) and the Zarf-templated overrides (values/values.yaml).
type chartValues struct {
	Secrets map[string]string `yaml:"secrets"`
}
