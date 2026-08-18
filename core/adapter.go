package core

import "context"

// Route is one canonical public URL the host wants search engines to index.
type Route struct {
	URL               string
	Type              string
	ExpectedIndexable bool
}

// JobRef identifies a host-side publish job.
type JobRef struct{ ID string }

// JobState is the host's report on a publish job.
type JobState struct {
	Status  string // RUNNING / SUCCEEDED / FAILED
	Message string
}

// ProjectAdapter is the optional bridge to a host application: its canonical
// routes, prerender/publish pipeline, and nothing else. Every method is
// optional in the sense that the whole adapter is — without one, providers,
// sync, reports, and diagnostics all still work.
//
// The host supplies this adapter directly when embedding the module. A
// standalone deployment may choose an HTTP adapter, but the core never assumes
// a process boundary and never imports a specific host.
type ProjectAdapter interface {
	ListCanonicalRoutes(ctx context.Context, site Site) ([]Route, error)
	TriggerPublish(ctx context.Context, site Site) (JobRef, error)
	GetPublishJob(ctx context.Context, ref JobRef) (JobState, error)
}
