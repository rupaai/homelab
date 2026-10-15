package hub

import "github.com/rupaai/homelab/internal/core"

const (
	ActionApps       = "apps"
	ActionProjects   = "projects"
	ActionLogs       = "logs"
	ActionSecrets    = "secrets"
	ActionPeople     = "people"
	ActionSettings   = "settings"
	ActionRemoveUser = "remove-person"
	ActionManage     = "manage"
	ActionSelf       = "self"
)

// Can is the API access policy shared by every request and proxy check.
func Can(p core.Person, action, app string) bool {
	if p.Status != "signed_in" {
		return false
	}
	roles := map[string]bool{}
	for _, role := range p.Roles {
		roles[role] = true
	}
	admin := roles["owner"] || roles["admin"]
	grant := p.Grants[app]
	build := grant == "build"
	use := grant == "use" || build
	switch action {
	case ActionSelf:
		return true
	case ActionManage:
		return admin
	case ActionApps:
		return admin || (app == "" && (roles["member"] || roles["builder"])) || (app != "" && use)
	case ActionProjects:
		if app == "" {
			return admin || roles["builder"]
		}
		return admin || build
	case ActionLogs:
		return admin || (app != "" && build)
	case ActionSecrets:
		return admin
	case ActionPeople, ActionSettings:
		return admin
	case ActionRemoveUser:
		return admin && app != "owner"
	default:
		return false
	}
}
