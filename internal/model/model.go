package model

import (
	"fmt"
	"strings"
)

const (
	DefaultVersion      = "0.1.0"
	DependencyInitImage = "busybox:1.36"
)

type Port struct {
	Number      int
	Protocol    string
	Raw         string
	Published   bool
	Name        string
	AppProtocol string
}

func (p Port) HasWebHint() bool {
	return IsWebPortHint(p.AppProtocol) || IsWebPortHint(p.Name)
}

func IsWebPortHint(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	normalized = strings.ReplaceAll(normalized, "_", "-")

	switch normalized {
	case "http", "https", "http2", "h2c", "grpc", "grpcs", "web", "www":
		return true
	default:
		return false
	}
}

type EnvVar struct {
	Name  string
	Value string
}

type ResourceSet struct {
	CPU    string
	Memory string
}

type Resources struct {
	Limits   ResourceSet
	Requests ResourceSet
}

type Healthcheck struct {
	Command             []string
	PeriodSeconds       int
	InitialDelaySeconds int
	TimeoutSeconds      int
	FailureThreshold    int
}

type Volume struct {
	Name     string
	External bool
}

type VolumeMount struct {
	Name     string
	Target   string
	ReadOnly bool
}

type FileRef struct {
	Source string
	Target string
}

type Secret struct {
	Name     string
	External bool
}

type Config struct {
	Name     string
	External bool
	Content  string
}

type Dependency struct {
	Service   string
	Condition string
}

type Package struct {
	Name            string
	Namespace       string
	Version         string
	NetworkExpose   []any
	Monitor         []any
	AdditionalAllow []any
	SSOConfigured   bool
	SSO             []any
	CABundle        map[string]any
}

type Service struct {
	Name         string
	Image        string
	UsesBuild    bool
	Ports        []Port
	Networks     []string
	Env          []EnvVar
	User         string
	Privileged   bool
	CapAdd       []string
	CapDrop      []string
	SecurityOpts []string
	Command      []string
	Args         []string
	Stdin        bool
	Hostname     string
	Healthcheck  *Healthcheck
	Volumes      []VolumeMount
	Secrets      []FileRef
	Configs      []FileRef
	DependsOn    []Dependency
	Resources    Resources
	Profiles     []string
}

type App struct {
	Package  Package
	Services []Service
	Volumes  map[string]Volume
	Secrets  map[string]Secret
	Configs  map[string]Config
}

func (s Service) PrimaryPort() (Port, error) {
	if len(s.Ports) == 0 {
		return Port{}, fmt.Errorf("service %q has no declared ports", s.Name)
	}
	return s.Ports[0], nil
}
