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
	if _, err := checkedU32Length(uint64(len(intent.ContentType))); err != nil {
		return nil, err
	}
	if _, err := checkedU32Length(uint64(len(intent.Reason))); err != nil {
		return nil, err
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldString(1, intent.ContentType); err != nil {
			return err
		}
		if err := w.fieldInt64(2, intent.VersionGUID); err != nil {
			return err
		}
		if err := w.fieldInt64(3, intent.ExpectedBaseVersion); err != nil {
			return err
		}
		return w.fieldString(4, intent.Reason)
	})
}

func encodeRollbackIntent(intent RollbackIntent) ([]byte, error) {
	if intent.ContentType == "" || intent.VersionGUID <= 0 || intent.ExpectedCurrentVersion <= 0 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	if _, err := checkedU32Length(uint64(len(intent.ContentType))); err != nil {
		return nil, err
	}
	if _, err := checkedU32Length(uint64(len(intent.Reason))); err != nil {
		return nil, err
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldString(1, intent.ContentType); err != nil {
			return err
		}
		if err := w.fieldInt64(2, intent.VersionGUID); err != nil {
			return err
		}
		if err := w.fieldInt64(3, intent.ExpectedCurrentVersion); err != nil {
			return err
		}
		return w.fieldString(4, intent.Reason)
	})
}
