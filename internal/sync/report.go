package sync

import "fmt"

// ObjectResult summarizes what happened to one object type during a run.
type ObjectResult struct {
	Created int
	Updated int
	Deleted int
	Skipped int
	Failed  int
	Errors  []error
}

func (r *ObjectResult) addFailure(err error) {
	r.Failed++
	r.Errors = append(r.Errors, err)
}

// String renders a one-line summary suitable for a structured log field or
// --dry-run output.
func (r ObjectResult) String() string {
	return fmt.Sprintf("created=%d updated=%d deleted=%d skipped=%d failed=%d", r.Created, r.Updated, r.Deleted, r.Skipped, r.Failed)
}

// Result is the outcome of one Engine.RunJob call across every object type
// the job requested.
type Result struct {
	SourceName   string
	Sites        ObjectResult
	Devices      ObjectResult
	IPGroups     ObjectResult
	DeviceLabels ObjectResult
	// FetchErrors holds any error that aborted fetching a requested object
	// type entirely (as opposed to a per-item apply failure, which is
	// recorded in the relevant ObjectResult.Errors instead).
	FetchErrors []error
}

// HasFailures reports whether anything went wrong this run, for exit-code
// and alerting purposes.
func (r Result) HasFailures() bool {
	return len(r.FetchErrors) > 0 || r.Sites.Failed > 0 || r.Devices.Failed > 0 || r.IPGroups.Failed > 0 || r.DeviceLabels.Failed > 0
}
