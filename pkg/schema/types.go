package schema

import "strings"

type Type string

const (
	TypeAny     Type = "any"
	TypeObject  Type = "object"
	TypeArray   Type = "array"
	TypeString  Type = "string"
	TypeInteger Type = "integer"
	TypeNumber  Type = "number"
	TypeBoolean Type = "boolean"
	TypeNull    Type = "null"
)

type GVK struct {
	Group   string
	Version string
	Kind    string
}

func (g GVK) IsZero() bool {
	return strings.TrimSpace(g.Kind) == "" || strings.TrimSpace(g.Version) == ""
}

func ParseAPIVersion(apiVersion string) (group, version string) {
	apiVersion = strings.TrimSpace(apiVersion)
	if apiVersion == "" {
		return "", ""
	}
	if strings.Contains(apiVersion, "/") {
		parts := strings.SplitN(apiVersion, "/", 2)
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	// Core group
	return "", apiVersion
}
