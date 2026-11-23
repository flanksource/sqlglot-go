package sqlserver

// ProgressStep is what a Progress reports on.
type ProgressStep string

const (
	// ProgressCheck is planning the bundle again to compare it with the plan
	// file: before anything runs, and after Script when its effects are unknown.
	ProgressCheck ProgressStep = "check"
	// ProgressScript is running Script and recording it.
	ProgressScript ProgressStep = "script"
	// ProgressSchema is running the schema step.
	ProgressSchema ProgressStep = "schema"
)

// Progress is one step of an Apply starting or, when Done, ending with an error or warning.
type Progress struct {
	Step    ProgressStep
	Script  string
	Done    bool
	Err     error
	Warning error
}

// WithProgress calls fn, on the applying goroutine, as each step of the apply
// starts and ends. A step the plan does not reach is never reported.
func WithProgress(fn func(Progress)) ApplyOption {
	return func(c *applyConfig) { c.progress = fn }
}

// report runs fn as step of script, reporting it to the progress option.
func (c applyConfig) report(step ProgressStep, script string, fn func() error) error {
	if c.progress == nil {
		return fn()
	}
	c.progress(Progress{Step: step, Script: script})
	err := fn()
	c.progress(Progress{Step: step, Script: script, Done: true, Err: err})
	return err
}

func (c applyConfig) emit(p Progress) {
	if c.progress != nil {
		c.progress(p)
	}
}
