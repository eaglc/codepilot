package approval

import "github.com/eaglc/codepilot/internal/session"

func (s *Service) consumeOnceLocked(sessionID session.SessionID, fingerprint string) bool {
	values := s.once[sessionID]
	if values == nil || values[fingerprint] == 0 {
		return false
	}
	values[fingerprint]--
	if values[fingerprint] == 0 {
		delete(values, fingerprint)
	}
	if len(values) == 0 {
		delete(s.once, sessionID)
	}
	return true
}

func (s *Service) hasGrantLocked(sessionID session.SessionID, fingerprint string) bool {
	_, exists := s.grants[sessionID][fingerprint]
	return exists
}

func (s *Service) recordDecisionLocked(request session.ApprovalRequest, decision session.ApprovalDecision) {
	switch decision.Kind {
	case session.ApprovalAllowOnce:
		if s.once[request.SessionID] == nil {
			s.once[request.SessionID] = make(map[string]int)
		}
		s.once[request.SessionID][request.Action.Fingerprint]++
	case session.ApprovalAllowSession:
		if s.grants[request.SessionID] == nil {
			s.grants[request.SessionID] = make(map[string]struct{})
		}
		s.grants[request.SessionID][request.Action.Fingerprint] = struct{}{}
	}
}
