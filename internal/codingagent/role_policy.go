package codingagent

import (
	"fmt"

	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
	"github.com/eaglc/codepilot/internal/workflow"
)

func (s *Service) nodeRoleDefinition(node workflow.Node) (roleprofile.Definition, error) {
	definition, err := s.deps.Roles.ResolveVersion(node.Role, roleprofile.Profile(node.Capability), node.PolicyVersion)
	if err != nil {
		return roleprofile.Definition{}, err
	}
	if definition.Workflow.Capability != node.Capability {
		return roleprofile.Definition{}, fmt.Errorf("role profile %q does not allow Workflow capability %q", node.Role, node.Capability)
	}
	return definition, nil
}

func (s *Service) nodeCapabilityProfile(node workflow.Node) (CapabilityProfile, error) {
	definition, err := s.nodeRoleDefinition(node)
	if err != nil {
		return "", err
	}
	return CapabilityProfile(definition.Profile), nil
}

func (s *Service) childRoleDefinition(child ChildAgent) (roleprofile.Definition, error) {
	return s.deps.Roles.ResolveVersion(child.Role, roleprofile.Profile(child.Profile), child.PolicyVersion)
}
