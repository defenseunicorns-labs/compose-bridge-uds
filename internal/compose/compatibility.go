package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

type CompatibilityIssue struct {
	Code        string
	Path        string
	Message     string
	Remediation string
}

type CompatibilityError struct {
	Issues []CompatibilityIssue
}

func (e *CompatibilityError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "compose model contains %d unsupported setting(s)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(&b, "\n- [%s] %s: %s", issue.Code, issue.Path, issue.Message)
		if issue.Remediation != "" {
			fmt.Fprintf(&b, "; %s", issue.Remediation)
		}
	}
	return b.String()
}

var supportedServiceKeys = map[string]struct{}{
	"build": {}, "cap_add": {}, "cap_drop": {}, "command": {}, "configs": {},
	"container_name": {}, "depends_on": {}, "deploy": {}, "entrypoint": {},
	"environment": {}, "env_file": {}, "expose": {}, "healthcheck": {}, "hostname": {},
	"image": {}, "networks": {}, "ports": {}, "privileged": {}, "profiles": {}, "restart": {},
	"secrets": {}, "security_opt": {}, "user": {}, "volumes": {},
}

var unsupportedServiceRemediation = map[string]string{
	"labels":       "replace Docker runtime discovery with explicit UDS networking configuration",
	"network_mode": "use ordinary Compose service networking",
	"platform":     "use an OCI image compatible with the target Kubernetes nodes",
	"runtime":      "use the cluster's standard OCI runtime",
	"stop_signal":  "update the image to shut down correctly on SIGTERM",
	"sysctls":      "remove host kernel tuning from the application package",
}

func validateCompatibility(project types.Project, raw map[string]any) error {
	issues := []CompatibilityIssue{}
	rawServices, _ := asMap(raw["services"])
	serviceNames := make([]string, 0, len(project.Services))
	for name := range project.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	for _, serviceName := range serviceNames {
		service := project.Services[serviceName]
		path := "services." + serviceName
		rawService, _ := asMap(rawServices[serviceName])

		if strings.TrimSpace(service.Image) == "" {
			if service.Build != nil {
				issues = append(issues, CompatibilityIssue{
					Code:        "build-image-unresolved",
					Path:        path + ".image",
					Message:     "the local build image was not resolved by Compose Bridge",
					Remediation: "run `docker compose build` before conversion or declare `image:` explicitly",
				})
			} else {
				issues = append(issues, CompatibilityIssue{
					Code:        "image-required",
					Path:        path + ".image",
					Message:     "the service has no container image",
					Remediation: "declare `image:` or `build:`",
				})
			}
		}

		for _, mount := range service.Volumes {
			mountType := strings.ToLower(strings.TrimSpace(mount.Type))
			if mountType == "" && looksLikePath(mount.Source) {
				mountType = types.VolumeTypeBind
			}
			switch mountType {
			case types.VolumeTypeBind:
				continue
			case "", types.VolumeTypeVolume:
			default:
				issues = append(issues, CompatibilityIssue{
					Code:        "volume-type",
					Path:        path + ".volumes",
					Message:     fmt.Sprintf("volume type %q is not supported", mount.Type),
					Remediation: "use a named volume, Compose config, or Compose secret",
				})
			}
		}

		if containerName := strings.TrimSpace(service.ContainerName); containerName != "" {
			normalizedContainer, containerErr := normalizeName(containerName)
			normalizedService, serviceErr := normalizeName(serviceName)
			if containerErr != nil || serviceErr != nil || normalizedContainer != normalizedService {
				issues = append(issues, CompatibilityIssue{
					Code:        "container-name-alias",
					Path:        path + ".container_name",
					Message:     "container_name differs from the Compose service name and would change service discovery",
					Remediation: "remove container_name and address the service by its Compose service name",
				})
			}
		}
		for networkName, network := range service.Networks {
			if network == nil {
				continue
			}
			if len(network.Aliases) > 0 || len(network.DriverOpts) > 0 || network.InterfaceName != "" || network.Ipv4Address != "" || network.Ipv6Address != "" || len(network.LinkLocalIPs) > 0 || network.MacAddress != "" {
				issues = append(issues, CompatibilityIssue{
					Code:        "network-options",
					Path:        path + ".networks." + networkName,
					Message:     "network aliases, addresses, interface names, and driver options are not translated",
					Remediation: "use Kubernetes service names and cluster-assigned addresses",
				})
			}
		}

		for key := range rawService {
			if strings.HasPrefix(key, "x-") {
				continue
			}
			if _, ok := supportedServiceKeys[key]; ok {
				continue
			}
			remediation := unsupportedServiceRemediation[key]
			if remediation == "" {
				remediation = "remove the setting or provide an equivalent through supported Compose and x-uds fields"
			}
			issues = append(issues, CompatibilityIssue{
				Code:        "service-field",
				Path:        path + "." + key,
				Message:     fmt.Sprintf("Compose field %q is not translated", key),
				Remediation: remediation,
			})
		}
	}

	issues = append(issues, validateNetworkTopology(project)...)
	for name, network := range project.Networks {
		if network.Driver != "" || len(network.DriverOpts) > 0 || network.Internal || network.Attachable || len(network.Ipam.Config) > 0 || network.Ipam.Driver != "" {
			issues = append(issues, CompatibilityIssue{
				Code:        "network-options",
				Path:        "networks." + name,
				Message:     "network driver, IPAM, internal, and attachable settings are not translated",
				Remediation: "use a standard shared Compose network",
			})
		}
	}
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Path < issues[j].Path
	})
	return &CompatibilityError{Issues: issues}
}

func validateNetworkTopology(project types.Project) []CompatibilityIssue {
	sets := map[string]struct{}{}
	for _, service := range project.Services {
		networks := make([]string, 0, len(service.Networks))
		for name := range service.Networks {
			networks = append(networks, name)
		}
		sort.Strings(networks)
		sets[strings.Join(networks, ",")] = struct{}{}
	}
	if len(sets) <= 1 {
		return nil
	}
	return []CompatibilityIssue{{
		Code:        "network-topology",
		Path:        "services.*.networks",
		Message:     "services use different Compose network memberships, which would be flattened inside the package namespace",
		Remediation: "use one shared Compose network or wait for network-isolation support",
	}}
}
