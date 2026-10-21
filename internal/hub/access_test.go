package hub

import (
	"testing"

	"github.com/rupaai/homelab/internal/core"
)

func TestCanRoleActionMatrix(t *testing.T) {
	roles := []string{"owner", "admin", "builder", "member", "secrets"}
	actions := []string{ActionSelf, ActionApps, ActionProjects, ActionLogs, ActionSecrets, ActionSecretValues, ActionPeople, ActionSettings, ActionManage, ActionRemoveUser}
	for _, role := range roles {
		admin := role == "owner" || role == "admin"
		for _, action := range actions {
			for _, grant := range []string{"", "use", "build"} {
				for _, app := range []string{"", "movies", "owner"} {
					person := core.Person{ID: role, Status: "signed_in", Roles: []string{role}, Grants: map[string]string{}}
					if grant != "" {
						person.Grants["movies"] = grant
					}
					want := false
					switch action {
					case ActionSelf:
						want = true
					case ActionManage, ActionSecrets, ActionPeople, ActionSettings:
						want = admin
					case ActionSecretValues:
						want = role == "owner" || role == "secrets"
					case ActionApps:
						want = admin || (app == "" && (role == "builder" || role == "member")) || (app == "movies" && grant != "")
					case ActionProjects, ActionLogs:
						want = admin || (app == "" && action == ActionProjects && role == "builder") || (app == "movies" && grant == "build")
					case ActionRemoveUser:
						want = admin && app != "owner"
					}
					name := role + "/" + action + "/" + app + "/" + grant
					t.Run(name, func(t *testing.T) {
						if got := Can(person, action, app); got != want {
							t.Fatalf("Can()=%v want %v", got, want)
						}
					})
				}
			}
		}
	}
	if Can(core.Person{Status: "invited", Roles: []string{"admin"}}, ActionPeople, "") {
		t.Fatal("invited admin was authorized")
	}
}
