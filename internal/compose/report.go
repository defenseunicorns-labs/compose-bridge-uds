package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

var translatedServiceSettings = map[string]string{
	"build":        "generated Buildx Bake service",
	"cap_add":      "Deployment container security context",
	"cap_drop":     "Deployment container security context",
	"command":      "Deployment container arguments",
	"configs":      "Deployment config volume mounts",
	"depends_on":   "Deployment dependency init containers",
	"entrypoint":   "Deployment container command",
	"environment":  "environment ConfigMap and deploy-time variables",
	"env_file":     "environment ConfigMap and deploy-time variables",
	"expose":       "Kubernetes Service ports",
	"hostname":     "Deployment Pod hostname",
	"image":        "Zarf package image and Deployment container image",
	"networks":     "UDS network policy selectors",
	"ports":        "Kubernetes Service ports and UDS gateway exposure",
	"privileged":   "Deployment container security context and UDS exemption",
	"secrets":      "Deployment secret volume mounts",
	"security_opt": "Deployment container security context and UDS exemption",
	"stdin_open":   "Deployment container stdin setting",
	"user":         "Deployment container security context",
}

var supportedUDSMetadataKeys = map[string]struct{}{
	"name":        {},
	"version":     {},
	"labels":      {},
	"annotations": {},
}

var supportedUDSSpecKeys = map[string]struct{}{
	"network":  {},
	"monitor":  {},
	"sso":      {},
	"caBundle": {},
}

var supportedUDSNetworkKeys = map[string]struct{}{
	"expose": {},
	"allow":  {},
}

func rejectedConversion(path, code string, err error) (model.Conversion, error) {
	report := newConversionReport()
	report.Rejected = append(report.Rejected, model.ConversionDecision{
		Path:    path,
		Code:    code,
		Message: err.Error(),
	})
	return model.Conversion{Report: report}, err
}

func newConversionReport() model.ConversionReport {
	return model.ConversionReport{
		Translated: []model.ConversionDecision{},
		Inferred:   []model.ConversionDecision{},
		Ignored:    []model.ConversionDecision{},
		Rejected:   []model.ConversionDecision{},
	}
}

func buildConversionReport(project types.Project, raw map[string]any, excludedServices map[string]struct{}) model.ConversionReport {
	report := newConversionReport()
	uds, _ := asMap(raw["x-uds"])
	metadata, _ := asMap(uds["metadata"])
	if _, exists := raw["name"]; exists {
		if strings.TrimSpace(asString(metadata["name"])) != "" {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    "name",
				Message: "x-uds.metadata.name overrides the Compose project name for the generated package identity.",
			})
		} else {
			report.Translated = append(report.Translated, model.ConversionDecision{
				Path:    "name",
				Target:  "package metadata name",
				Message: "Used the Compose project name as the generated package identity.",
			})
		}
	}

	rawServices, _ := asMap(raw["services"])
	serviceNames := make([]string, 0, len(project.Services))
	for name := range project.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	for _, serviceName := range serviceNames {
		service := project.Services[serviceName]
		servicePath := "services." + serviceName
		if _, excluded := excludedServices[serviceName]; excluded {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    servicePath,
				Message: "Excluded this development-only service because every depends_on reference marks it required: false.",
			})
			continue
		}

		rawService, _ := asMap(rawServices[serviceName])
		keys := sortedNames(rawService)
		for _, key := range keys {
			path := servicePath + "." + key
			switch key {
			case "container_name":
				report.Ignored = append(report.Ignored, model.ConversionDecision{
					Path:    path,
					Message: "Kubernetes resources use the Compose service name, so container_name has no effect.",
				})
			case "restart":
				report.Ignored = append(report.Ignored, model.ConversionDecision{
					Path:    path,
					Message: "Kubernetes controls workload restarts, so the Compose restart policy is not translated.",
				})
			case "profiles":
				report.Ignored = append(report.Ignored, model.ConversionDecision{
					Path:    path,
					Message: "Compose profile selection is resolved before package generation and is not represented in the package.",
				})
			case "volumes":
				addVolumeDecisions(&report, servicePath, service.Volumes)
			case "deploy":
				addDeployDecisions(&report, path, rawService[key])
			case "healthcheck":
				if service.HealthCheck == nil || service.HealthCheck.Disable || len(service.HealthCheck.Test) == 0 {
					report.Ignored = append(report.Ignored, model.ConversionDecision{
						Path:    path,
						Message: "The healthcheck is disabled or has no command, so no Kubernetes probe is generated.",
					})
				} else {
					report.Translated = append(report.Translated, translatedDecision(path, "Deployment liveness probe"))
				}
			case "image":
				if service.Build != nil {
					report.Ignored = append(report.Ignored, model.ConversionDecision{
						Path:    path,
						Message: "Local build services use a generated internal image reference instead of the Compose image setting.",
					})
				} else {
					report.Translated = append(report.Translated, translatedDecision(path, translatedServiceSettings[key]))
				}
			default:
				if target, ok := translatedServiceSettings[key]; ok {
					report.Translated = append(report.Translated, translatedDecision(path, target))
				} else if strings.HasPrefix(key, "x-") {
					report.Ignored = append(report.Ignored, model.ConversionDecision{
						Path:    path,
						Message: "This service extension is not consumed by the UDS package translator.",
					})
				}
			}
		}
	}

	addNetworkDecisions(&report, project, raw, excludedServices)
	addUDSExtensionDecisions(&report, raw)
	for _, key := range sortedNames(raw) {
		if key == "version" {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    key,
				Message: "The legacy Compose schema version does not affect the generated package.",
			})
		} else if strings.HasPrefix(key, "x-") && key != "x-uds" {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    key,
				Message: "This top-level Compose extension is not consumed by the UDS package translator.",
			})
		}
	}

	return report
}

func translatedDecision(path, target string) model.ConversionDecision {
	return model.ConversionDecision{
		Path:    path,
		Target:  target,
		Message: "Translated this Compose setting into generated package configuration.",
	}
}

func addVolumeDecisions(report *model.ConversionReport, servicePath string, volumes []types.ServiceVolumeConfig) {
	for i, volume := range volumes {
		path := fmt.Sprintf("%s.volumes[%d]", servicePath, i)
		mountType := strings.ToLower(strings.TrimSpace(volume.Type))
		if mountType == "" && looksLikePath(volume.Source) {
			mountType = types.VolumeTypeBind
		}
		if mountType == types.VolumeTypeBind {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    path,
				Message: "Bind mounts have no portable Kubernetes equivalent; use a named volume, config, or secret.",
			})
			continue
		}
		if mountType == "" || mountType == types.VolumeTypeVolume {
			report.Translated = append(report.Translated, translatedDecision(path, "Deployment volume mount"))
		}
	}
}

func addDeployDecisions(report *model.ConversionReport, path string, value any) {
	deploy, ok := asMap(value)
	if !ok {
		return
	}
	for _, key := range sortedNames(deploy) {
		settingPath := path + "." + key
		if key == "resources" {
			addDeployResourceDecisions(report, settingPath, deploy[key])
			continue
		}
		report.Ignored = append(report.Ignored, model.ConversionDecision{
			Path:    settingPath,
			Message: "Only deploy.resources is translated; Kubernetes and UDS control this deploy setting.",
		})
	}
}

func addDeployResourceDecisions(report *model.ConversionReport, path string, value any) {
	resources, ok := asMap(value)
	if !ok {
		return
	}
	for _, key := range sortedNames(resources) {
		settingPath := path + "." + key
		if key != "limits" && key != "reservations" {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    settingPath,
				Message: "Only deploy.resources.limits and deploy.resources.reservations CPU and memory settings are translated.",
			})
			continue
		}
		resourceSet, ok := asMap(resources[key])
		if !ok {
			continue
		}
		for _, resourceKey := range sortedNames(resourceSet) {
			resourcePath := settingPath + "." + resourceKey
			if resourceKey == "cpus" || resourceKey == "memory" {
				report.Translated = append(report.Translated, translatedDecision(resourcePath, "Deployment resource requests and limits"))
				continue
			}
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    resourcePath,
				Message: "Only CPU and memory resource requests and limits are translated from deploy.resources.",
			})
		}
	}
}

func addNetworkDecisions(report *model.ConversionReport, project types.Project, raw map[string]any, excludedServices map[string]struct{}) {
	used := map[string]struct{}{}
	for serviceName, service := range project.Services {
		if _, excluded := excludedServices[serviceName]; excluded {
			continue
		}
		for networkName := range service.Networks {
			used[networkName] = struct{}{}
		}
	}
	rawNetworks, _ := asMap(raw["networks"])
	for _, name := range sortedNames(project.Networks) {
		path := "networks." + name
		if _, exists := used[name]; !exists {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    path,
				Message: "The network is not used by a packaged service.",
			})
			continue
		}
		if _, configured := rawNetworks[name]; configured {
			report.Translated = append(report.Translated, translatedDecision(path, "UDS network policy selectors"))
		} else {
			report.Inferred = append(report.Inferred, model.ConversionDecision{
				Path:    path,
				Value:   name,
				Message: "Inferred the Compose default network used for generated UDS network policy selectors.",
			})
		}
		if bool(project.Networks[name].External) {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    path + ".external",
				Message: "External Compose network semantics cannot identify Kubernetes peers; the network is treated as package-local.",
			})
		}
	}
}

func addUDSExtensionDecisions(report *model.ConversionReport, raw map[string]any) {
	uds, ok := asMap(raw["x-uds"])
	if !ok {
		return
	}
	if metadata, ok := asMap(uds["metadata"]); ok {
		for _, key := range sortedNames(metadata) {
			path := "x-uds.metadata." + key
			if key == "name" && strings.TrimSpace(asString(metadata[key])) == "" {
				report.Ignored = append(report.Ignored, model.ConversionDecision{
					Path:    path,
					Message: "This metadata.name value is empty or invalid, so package identity is inferred from the Compose project name.",
				})
				continue
			}
			if _, supported := supportedUDSMetadataKeys[key]; supported {
				report.Translated = append(report.Translated, translatedDecision(path, "package metadata"))
				continue
			}
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    path,
				Message: "This x-uds metadata field is not consumed by the package translator.",
			})
		}
	}
	if spec, ok := asMap(uds["spec"]); ok {
		for _, key := range sortedNames(spec) {
			path := "x-uds.spec." + key
			if key == "network" {
				if network, ok := asMap(spec[key]); ok {
					for _, networkKey := range sortedNames(network) {
						networkPath := path + "." + networkKey
						if _, supported := supportedUDSNetworkKeys[networkKey]; supported {
							report.Translated = append(report.Translated, translatedDecision(networkPath, "UDS Package custom resource"))
							continue
						}
						report.Ignored = append(report.Ignored, model.ConversionDecision{
							Path:    networkPath,
							Message: "Only x-uds.spec.network.expose and x-uds.spec.network.allow are consumed by the package translator.",
						})
					}
					continue
				}
			}
			if _, supported := supportedUDSSpecKeys[key]; supported {
				report.Translated = append(report.Translated, translatedDecision(path, "UDS Package custom resource"))
				continue
			}
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    path,
				Message: "This x-uds spec field is not consumed by the package translator.",
			})
		}
	}
}

func completeConversionReport(report *model.ConversionReport, project types.Project, raw map[string]any, app model.App) {
	addResourceDecisions(report, project, app)
	uds, _ := asMap(raw["x-uds"])
	metadata, _ := asMap(uds["metadata"])
	if strings.TrimSpace(asString(metadata["name"])) == "" {
		report.Inferred = append(report.Inferred, model.ConversionDecision{
			Path:    "x-uds.metadata.name",
			Value:   app.Package.Name,
			Message: "Inferred the package name and Kubernetes namespace from the Compose project name.",
		})
	}
	if !app.Package.VersionConfigured {
		report.Inferred = append(report.Inferred, model.ConversionDecision{
			Path:    "x-uds.metadata.version",
			Value:   app.Package.Version,
			Message: "Inferred the package version from the primary service image tag, falling back to the bridge default when necessary.",
		})
	}

	rawServices, _ := asMap(raw["services"])
	for _, service := range app.Services {
		rawServiceName, _ := findRawService(rawServices, service.Name)
		if service.Build != nil {
			report.Inferred = append(report.Inferred, model.ConversionDecision{
				Path:    "services." + rawServiceName + ".image",
				Value:   service.Image,
				Message: "Generated an internal image reference for the local build.",
			})
		}
	}

	if len(app.Package.NetworkExpose) == 0 {
		primaryExposedService := ""
		for _, service := range app.Services {
			for _, port := range service.Ports {
				if !port.Published {
					continue
				}
				if primaryExposedService == "" {
					primaryExposedService = service.Name
				}
				report.Inferred = append(report.Inferred, model.ConversionDecision{
					Path:    "x-uds.spec.network.expose",
					Value:   service.Name,
					Message: "Inferred a tenant gateway exposure from the service's published ports.",
				})
				break
			}
		}
		if !app.Package.SSOConfigured && primaryExposedService != "" {
			report.Inferred = append(report.Inferred, model.ConversionDecision{
				Path:    "x-uds.spec.sso",
				Value:   primaryExposedService,
				Message: "Inferred default SSO client configuration from the inferred service exposure.",
			})
		}
	}

	sortConversionReport(report)
}

func findRawService(rawServices map[string]any, normalizedName string) (string, map[string]any) {
	for name, value := range rawServices {
		normalized, err := normalizeName(name)
		if err != nil || normalized != normalizedName {
			continue
		}
		service, _ := asMap(value)
		return name, service
	}
	return normalizedName, nil
}

func addResourceDecisions(report *model.ConversionReport, project types.Project, app model.App) {
	addNamedResourceDecisions(report, "volumes", sortedNames(project.Volumes), func(name string) bool {
		normalized, err := normalizeName(name)
		if err != nil {
			return false
		}
		_, exists := app.Volumes[normalized]
		return exists
	}, "PersistentVolumeClaim")
	addNamedResourceDecisions(report, "secrets", sortedNames(project.Secrets), func(name string) bool {
		normalized, err := normalizeName(name)
		if err != nil {
			return false
		}
		_, exists := app.Secrets[normalized]
		return exists
	}, "Kubernetes Secret configuration")
	addNamedResourceDecisions(report, "configs", sortedNames(project.Configs), func(name string) bool {
		normalized, err := normalizeName(name)
		if err != nil {
			return false
		}
		_, exists := app.Configs[normalized]
		return exists
	}, "Kubernetes ConfigMap configuration")
}

func addNamedResourceDecisions(report *model.ConversionReport, kind string, names []string, retained func(string) bool, target string) {
	for _, name := range names {
		path := kind + "." + name
		if retained(name) {
			report.Translated = append(report.Translated, translatedDecision(path, target))
		} else {
			report.Ignored = append(report.Ignored, model.ConversionDecision{
				Path:    path,
				Message: "The resource is not referenced by a packaged service.",
			})
		}
	}
}

func sortConversionReport(report *model.ConversionReport) {
	less := func(items []model.ConversionDecision) {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Path == items[j].Path {
				return items[i].Code < items[j].Code
			}
			return items[i].Path < items[j].Path
		})
	}
	less(report.Translated)
	less(report.Inferred)
	less(report.Ignored)
	less(report.Rejected)
}

func sortedNames[M ~map[string]V, V any](values M) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
