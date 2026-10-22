package review

// Validate checks the reviewed hash, scope, acknowledgements and execution options without opening a database.
func Validate(request Request) error {
	if err := validate(request); err != nil {
		return err
	}
	return validateScope(request.Artifact.Baseline)
}
