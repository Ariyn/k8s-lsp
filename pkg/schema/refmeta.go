package schema

type RefRole string

const (
	RefRoleUnknown    RefRole = ""
	RefRoleDefinition RefRole = "definition"
	RefRoleReference  RefRole = "reference"
)

type RefMeta struct {
	Role  RefRole
	Kind  string // optional target Kind hint (e.g. ConfigMap)
	Scope string // optional: "namespaced" | "cluster"
}

func (r *RefMeta) IsDefinition() bool {
	return r != nil && r.Role == RefRoleDefinition
}

func (r *RefMeta) IsReference() bool {
	return r != nil && r.Role == RefRoleReference
}
