package actionsecurity

type PublishIntent struct {
	ContentType                      string
	VersionGUID, ExpectedBaseVersion int64
	Reason                           string
}
type RollbackIntent struct {
	ContentType                         string
	VersionGUID, ExpectedCurrentVersion int64
	Reason                              string
}

func encodePublishIntent(intent PublishIntent) ([]byte, error) {
	if intent.ContentType == "" || intent.VersionGUID <= 0 || intent.ExpectedBaseVersion <= 0 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	var w intentWriter
	w.fieldString(1, intent.ContentType)
	w.fieldInt64(2, intent.VersionGUID)
	w.fieldInt64(3, intent.ExpectedBaseVersion)
	w.fieldString(4, intent.Reason)
	return w.bytes(), nil
}

func encodeRollbackIntent(intent RollbackIntent) ([]byte, error) {
	if intent.ContentType == "" || intent.VersionGUID <= 0 || intent.ExpectedCurrentVersion <= 0 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	var w intentWriter
	w.fieldString(1, intent.ContentType)
	w.fieldInt64(2, intent.VersionGUID)
	w.fieldInt64(3, intent.ExpectedCurrentVersion)
	w.fieldString(4, intent.Reason)
	return w.bytes(), nil
}
