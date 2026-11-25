package resource

import (
	"github.com/prusa3d/prusa-connect/internal/app/config/install"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
)

// project renders one declaration as the API returns it, with the supervision
// plane's view attached.
func (s *Store) project(
	declared Declaration, version string, sees func(ref.Ref) bool,
) httpapi.ResourceInstance {
	options, known := s.declaredOptions(declared)

	instance := httpapi.ResourceInstance{
		ResourceSpec: httpapi.ResourceSpec{
			Type:     declared.Spec.Type,
			Enabled:  declared.Spec.Enabled,
			Required: declared.Spec.Required,
			Config:   httpapi.ResourceConfig(Redact(options, known, declared.Spec.Config)),

			HiddenContributions: keptHidden(declared.Spec.HiddenContributions),
		},
		Kind:       declared.Ref.Kind().String(),
		Name:       declared.Ref.ID(),
		Source:     declared.Source.String(),
		Version:    version,
		Generation: declared.Generation,
		Status:     s.declaredStatus(declared),
	}

	if !declared.UID.IsZero() {
		instance.UID = declared.UID.String()
	}

	// The zero runtime means builtin. It is written as "builtin" here, so a
	// client never reads an empty string for it.
	instance.Runtime = install.RuntimeBuiltin.String()
	if declared.Spec.Runtime != "" {
		instance.Runtime = declared.Spec.Runtime.String()
	}

	namespace, isModule := declared.ModuleNamespace()
	if isModule {
		instance.Namespace = namespace
		instance.Mount = declared.ModuleMount()
		instance.Module = moduleFactsOf(declared.Spec)

		every := Replaced(s.current.Instances, declared)
		if resolved, found := Resolve(every).Module(declared.Ref); found && instance.Module != nil {
			withDependencies(instance.Module, resolved, RequiredBy(every, namespace), sees, s.stateOf)
		}
	}

	if s.plane != nil {
		if observed, ok := s.plane.Status(declared.Ref); ok {
			instance.Status = observed
		}

		// The address is built from the namespace and published only while a
		// running module has a document, so a client that follows it does not
		// get a 404.
		if isModule && s.plane.Documented(declared.Ref) {
			instance.OpenAPIURL = httpapi.ModuleOpenAPIPath(namespace)
		}

		s.withDatabaseSchema(&instance, declared.Ref)

		// The UI block is published only for an enabled instance that has a
		// bundle open. The plane holds a bundle only while the instance runs.
		// A disabled instance is not asked, so a declaration that was just
		// switched off never shows an entry address a page would import and
		// fail on.
		if isModule && declared.Spec.Enabled {
			if surface, drawn := s.plane.UI(declared.Ref); drawn {
				instance.UI = &surface
			}
		}
	}

	withBudget(&instance, declared.Spec, s.switchOff)

	if declared.Spec.Service != nil {
		instance.StagedRoutes = StagedRoutesOf(declared.Spec.Service.StagedRoutes)
	}
	s.withSwitchOff(&instance, declared)

	return instance
}
