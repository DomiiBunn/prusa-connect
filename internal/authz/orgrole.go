package authz

import (
	"context"
	"database/sql"
	"slices"

	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
)

// orgRoleOrder is the role layer of this server's org, strongest first. It is
// the set an account can hold, and the order a listing reads them in: a person
// who holds two is described by the stronger.
//
// It is a list and not a lattice in the graph, because the only question asked
// of the order is "which of these does this person hold", and a list answers it.
//
//nolint:gochecknoglobals // the org's role order, read-only after init.
var orgRoleOrder = []Relation{RelAdmin, RelOperator, RelViewer}

// OrgRoles is the org roles an account can hold, strongest first. Every surface
// that renders or takes a role reads them here, so the order a page shows and
// the order the graph writes cannot drift apart.
func OrgRoles() []Relation { return slices.Clone(orgRoleOrder) }

// IsOrgRole reports whether this relation is one of the roles an account holds
// on the org. A relation the org has that is not a role, such as its edge onto a
// resource kind, answers false.
func IsOrgRole(relation Relation) bool { return slices.Contains(orgRoleOrder, relation) }

// OrgRoleConferring is the smallest role on the org that reaches this action,
// and false for an action no org role reaches at all.
//
// A caller that has to grant an action asks this instead of writing the mapping
// out again, so a role that changes what it reaches changes this answer with it.
// report_telemetry and fetch_job answer false: they come from a device's own
// credential, and no role carries them.
//
// It walks orgRoles, which is ordered weakest first for this reason. A caller
// that grants what this answers should read OrgRoleConfers too: the role it
// names reaches more than the one action it was asked about.
func OrgRoleConferring(action Action) (Relation, bool) {
	domain, known := action.domain()
	if !known {
		return "", false
	}

	for _, role := range orgRoles {
		reached := role.Level.deviceMask()
		if domain == TypeResourceKind {
			reached = role.Level.resourceMask()
		}

		if reached&action.mask() != 0 {
			return role.Relation, true
		}
	}

	return "", false
}

// OrgRoleConfers is every action this role on the org reaches, in the order
// Actions lists them, and nil for a relation that is not a role on the org.
//
// A caller that hands somebody a role reads this to say what it handed over. A
// role reaches more than the one action it was chosen for: the viewer role
// carries observe on every device, and view_resource and read_storage on every
// resource kind, so writing it to answer a request for observe hands the other
// two over as well.
func OrgRoleConfers(relation Relation) []Action {
	held, isRole := roleLevel(relation)
	if !isRole || !IsOrgRole(relation) {
		return nil
	}

	var out []Action

	for _, action := range Actions() {
		domain, known := action.domain()
		if !known {
			continue
		}

		reached := held.deviceMask()
		if domain == TypeResourceKind {
			reached = held.resourceMask()
		}

		if reached&action.mask() != 0 {
			out = append(out, action)
		}
	}

	return out
}

// Grantable reports whether the vocabulary lets a principal of this type hold
// this relation on an object of that type.
//
// It answers the question NewTuple answers by refusing, for a caller that wants
// to say what it will not write before it tries to write it. A sink holds viewer
// on the org and no role above it, so the operator role on the org for a
// module's own principal is refused here rather than at the write.
func Grantable(object Type, relation Relation, subject Type) bool {
	shapes, known := lookupRelation(object, relation)
	if !known {
		return false
	}

	return permits(shapes, Subject{typ: subject})
}

// SetOrgRole replaces every org role one account holds with the one named, in
// the transaction that reads the graph to decide it. Afterwards the account
// holds that role and no other.
//
// It is the one path that writes the admin role. The HTTP routes that set a
// role, set-account-role and create-account with a role, both call it.
// WriteGrant refuses that write
// and keeps refusing it, because the two surfaces are bounded by different
// things. A grant written through WriteGrant names any org, and an admin role on
// a second org is what lets its holder attach that org to a device of somebody
// else's and outrank the administrator already there. That refusal is
// adminNotConferrable, and its shape is configurable behaviour on the grant
// surface.
//
// This call names no org. It writes on this node's own org, the one Bootstrap
// wrote the first grant on. No second org exists for it to reach, so that
// escalation cannot happen here.
//
// The permission is configure_resource on the auth resource kind, which is what
// administering this server's identity means in the graph and what every other
// account operation takes. A caller holding it can already reset the
// administrator's password and log in as them, so conferring the role adds no
// standing they could not already take, and it adds a record of who has it.
//
// The one refusal that is not a permission is the last administrator. A server
// whose org holds no admin has nobody who can add a person or change what one
// may do, and nothing short of editing the configuration and restarting puts an
// administrator back.
func (s *Store) SetOrgRole(ctx context.Context, who Subject, account Subject, role Relation) error {
	if err := asks(who); err != nil {
		return err
	}

	if !IsOrgRole(role) {
		return unknownRelation.New("that is not one of the roles an account holds on the org").
			With(paramType(TypeOrg), paramRelation(role))
	}

	org, err := NewObject(TypeOrg, bootstrapOrgName)
	if err != nil {
		return err
	}

	// Through the constructor, so a subject shape the role does not accept is
	// refused before a transaction is opened.
	granted, err := NewTuple(org, role, account)
	if err != nil {
		return err
	}

	return s.mutate(ctx, func(ctx context.Context, tx *sql.Tx, moved *scope) (int64, error) {
		if permErr := s.mayAdministerAccounts(ctx, tx, who); permErr != nil {
			return 0, permErr
		}

		tuples, loadErr := loadTuples(ctx, tx, s.dialect)
		if loadErr != nil {
			return 0, loadErr
		}

		withdrawn := rolesHeldOn(tuples, org, account, role)

		if !anyOrgAdmin(indexTuples(afterRoleChange(tuples, withdrawn, granted)), org) {
			return 0, lastAdministrator.New(
				"this is the only account holding the admin role, and taking it away would leave "+
					"nobody able to administer this server").
				With(paramObject(org), paramSubject(account))
		}

		moved.moved(org)

		return s.writeRole(ctx, tx, withdrawn, granted)
	})
}

// mayAdministerAccounts refuses a caller who may not administer this server's
// identity. It reads the projection inside the writing transaction, for the
// reason mayAdminister does: a grant withdrawn between a check against the pool
// and the write would still be enough to write with.
func (s *Store) mayAdministerAccounts(ctx context.Context, q queryer, who Subject) error {
	object, err := authResourceKind()
	if err != nil {
		return err
	}

	if err = requireAdministering(ctx, object); err != nil {
		return err
	}

	held, err := s.heldActions(ctx, q, who, object)
	if err != nil {
		return err
	}

	if held&ActionConfigureResource.mask() == 0 {
		return denied(ActionConfigureResource, object)
	}

	return nil
}

// rolesHeldOn is every org role this account holds on the org, except the one
// being written. Those are the rows the change takes away, so the account is
// left holding exactly one role.
func rolesHeldOn(tuples []Tuple, org Object, account Subject, keep Relation) []Tuple {
	var held []Tuple

	for _, tuple := range tuples {
		if tuple.object != org || tuple.subject != account {
			continue
		}

		if !IsOrgRole(tuple.relation) || tuple.relation == keep {
			continue
		}

		held = append(held, tuple)
	}

	return held
}

// afterRoleChange is the tuple set the call produces, which is what the last
// administrator rule is read against. A rule that read the graph as it stands
// would refuse a demotion that keeps another administrator and allow one that
// does not.
func afterRoleChange(tuples, withdrawn []Tuple, granted Tuple) []Tuple {
	after := make([]Tuple, 0, len(tuples)+1)

	for _, tuple := range tuples {
		if slices.Contains(withdrawn, tuple) {
			continue
		}

		after = append(after, tuple)
	}

	return append(after, granted)
}

// anyOrgAdmin reports whether any principal at all still holds the admin role on
// this org, through a grant of their own or through a group they belong to. A
// group written into the role with nobody in it holds nothing, which is why the
// membership is expanded rather than the tuples counted.
func anyOrgAdmin(g *graph, org Object) bool {
	held := false

	for _, subject := range g.roles[roleKey{Object: TypeOrg, ID: org.id, Relation: RelAdmin}] {
		g.expand(subject, func(principal) { held = true })
	}

	return held
}

// writeRole takes the account's other roles away and writes the one it is given,
// and reports how many rows moved. A change that moves nothing rolls the
// permissions epoch back with the rest of the transaction, so setting the role
// somebody already holds publishes no generation.
func (s *Store) writeRole(
	ctx context.Context, tx *sql.Tx, withdrawn []Tuple, granted Tuple,
) (int64, error) {
	var changed int64

	for _, tuple := range withdrawn {
		result, err := tx.ExecContext(ctx, s.q(deleteTupleSQL), tupleArgs(tuple)...)
		if err != nil {
			return 0, storeFailure("delete an org role", err)
		}

		removed, err := result.RowsAffected()
		if err != nil {
			return 0, storeFailure("count the org roles removed", err)
		}

		changed += removed
	}

	result, err := tx.ExecContext(ctx, s.q(insertTupleSQL), tupleArgs(granted)...)
	if err != nil {
		return 0, storeFailure("write the org role", err)
	}

	written, err := result.RowsAffected()
	if err != nil {
		return 0, storeFailure("count the org role written", err)
	}

	return changed + written, nil
}

// authResourceKind is the object the permission to administer identity is
// written on. The accounts are the auth resource kind's data, so configuring
// that kind is administering who logs in, and there is no second vocabulary for
// the question.
func authResourceKind() (Object, error) {
	return NewObject(TypeResourceKind, ref.KindAuth.String())
}
