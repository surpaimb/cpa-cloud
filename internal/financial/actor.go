package financial

// Independently authored for docs/financial-actor-provenance-contract.md.
// The protocol and SQLite constraints are documented there; no external SDK is used.

import "database/sql"

type ActorKind string

const (
	ActorAdmin    ActorKind = "admin"
	ActorEmployee ActorKind = "employee"
	ActorSystem   ActorKind = "system"
)

type Actor struct {
	Kind ActorKind `json:"kind"`
	ID   string    `json:"id"`
}

func effectiveActor(actor Actor, adminID string) (Actor, bool) {
	if adminID != "" {
		if actor.Kind != "" || actor.ID != "" || !validText(adminID, 256) {
			return Actor{}, false
		}
		actor = Actor{Kind: ActorAdmin, ID: adminID}
	}
	if !validText(actor.ID, 256) {
		return Actor{}, false
	}
	switch actor.Kind {
	case ActorAdmin, ActorEmployee:
		return actor, true
	case ActorSystem:
		return actor, actor.ID == "payment_callback" || actor.ID == "subscription_one_shot_worker"
	default:
		return Actor{}, false
	}
}

func validActionActor(actor Actor, action string, commercial bool) bool {
	if actor.Kind != ActorSystem {
		return actor.Kind == ActorAdmin || actor.Kind == ActorEmployee
	}
	if commercial {
		return action == "subscription.renew" && actor.ID == "subscription_one_shot_worker"
	}
	return action == "payment_callback" && actor.ID == "payment_callback" || action == "subscription_purchase" && actor.ID == "subscription_one_shot_worker"
}

func actorColumns(actor Actor) (admin, employee, system any) {
	switch actor.Kind {
	case ActorAdmin:
		admin = actor.ID
	case ActorEmployee:
		employee = actor.ID
	case ActorSystem:
		system = actor.ID
	}
	return
}

func actorMatchesColumns(actor Actor, kind string, admin, employee, system sql.NullString) bool {
	if kind != string(actor.Kind) {
		return false
	}
	switch actor.Kind {
	case ActorAdmin:
		return admin.Valid && admin.String == actor.ID && !employee.Valid && !system.Valid
	case ActorEmployee:
		return !admin.Valid && employee.Valid && employee.String == actor.ID && !system.Valid
	case ActorSystem:
		return !admin.Valid && !employee.Valid && system.Valid && system.String == actor.ID
	default:
		return false
	}
}
