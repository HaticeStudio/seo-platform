package core

import "context"

// RequestContext is what resolvers may inspect: verified auth data only,
// never raw headers a client could forge.
type RequestContext struct {
	Subject Subject
	// Host is the HTTP host the request arrived on, useful for host-based
	// multi-site routing behind a trusted proxy.
	Host string
}

// Subject is the generic authenticated principal the API accepts. The core
// recognizes no specific IAM product — any OIDC/JWT/proxy/API-key adapter
// that produces a Subject works.
type Subject struct {
	ID     string
	Issuer string
	Roles  []string
	Scopes []string
	// Workspace is set only when the deployment enables multi-site.
	Workspace string
}

// HasScope reports whether the subject carries the named API scope.
func (s Subject) HasScope(scope string) bool {
	for _, have := range s.Scopes {
		if have == scope {
			return true
		}
	}
	return false
}

// API scopes actually checked by the server. Roles are just default bundles
// of these (ADR 0005).
const (
	ScopeRead              = "read"
	ScopeSync              = "sync"
	ScopeConnectionsManage = "connections.manage"
	ScopeSitesManage       = "sites.manage"
	ScopeMembersManage     = "members.manage"
	ScopeAuditRead         = "audit.read"
)

// SiteResolver decides which Site a request is about. Single-site deployments
// use StaticSiteResolver and never think about it again.
type SiteResolver interface {
	ResolveSite(ctx context.Context, request RequestContext) (Site, error)
}

// WorkspaceResolver is the optional multi-site extension point. Registering
// one turns on workspace scoping everywhere; not registering it is the
// single-site default and adds no configuration burden.
type WorkspaceResolver interface {
	ResolveWorkspace(ctx context.Context, subject Subject) (Workspace, error)
}

// Workspace is an isolation boundary for sites, connections, secrets, and
// audit data in multi-site mode.
type Workspace struct {
	ID   string
	Name string
}

// StaticSiteResolver always returns the one configured site.
type StaticSiteResolver struct{ Site Site }

func (r StaticSiteResolver) ResolveSite(context.Context, RequestContext) (Site, error) {
	return r.Site, nil
}
