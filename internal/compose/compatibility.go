package compose

import (
	"fmt"
	"os"
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
	"secrets": {}, "security_opt": {}, "stdin_open": {}, "user": {}, "volumes": {},
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
			fmt.Fprintf(os.Stderr, "warning: service %q container_name %q ignored; Kubernetes resources use the Compose service name\n", serviceName, containerName)
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

	for name, network := range project.Networks {
		unsupportedDriver := network.Driver != "" && network.Driver != "bridge"
		if unsupportedDriver || len(network.DriverOpts) > 0 || network.Internal || network.Attachable || len(network.Ipam.Config) > 0 || network.Ipam.Driver != "" {
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
