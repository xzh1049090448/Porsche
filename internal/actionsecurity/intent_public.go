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

type PublicModelDeleteIntent struct {
	ModelGUID, ExpectedRevision int64
	Reason                      string
}
type PublicPricingPublishIntent struct{ ExpectedRevision int64 }
type PublicPricingRestoreIntent struct{ ReleaseGUID, ExpectedRevision int64 }
type PublicContentPublishIntent struct{ PriceReleaseGUID, ExpectedRevision int64 }
type PublicContentRestoreIntent struct{ ReleaseGUID, ExpectedRevision int64 }

func encodePublicModelDeleteIntent(intent PublicModelDeleteIntent) ([]byte, error) {
	if intent.ModelGUID <= 0 || intent.ExpectedRevision <= 0 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.ModelGUID); err != nil {
			return err
		}
		if err := w.fieldInt64(2, intent.ExpectedRevision); err != nil {
			return err
		}
		return w.fieldString(3, intent.Reason)
	})
}

func encodePublicPricingPublishIntent(intent PublicPricingPublishIntent) ([]byte, error) {
	if intent.ExpectedRevision <= 0 {
		return nil, errInvalidIntent
	}
	return encodeIntent(func(w *intentWriter) error { return w.fieldInt64(1, intent.ExpectedRevision) })
}

func encodePublicPricingRestoreIntent(intent PublicPricingRestoreIntent) ([]byte, error) {
	if intent.ReleaseGUID <= 0 || intent.ExpectedRevision <= 0 {
		return nil, errInvalidIntent
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.ReleaseGUID); err != nil {
			return err
		}
		return w.fieldInt64(2, intent.ExpectedRevision)
	})
}

func encodePublicContentPublishIntent(intent PublicContentPublishIntent) ([]byte, error) {
	if intent.PriceReleaseGUID <= 0 || intent.ExpectedRevision <= 0 {
		return nil, errInvalidIntent
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.PriceReleaseGUID); err != nil {
			return err
		}
		return w.fieldInt64(2, intent.ExpectedRevision)
	})
}

func encodePublicContentRestoreIntent(intent PublicContentRestoreIntent) ([]byte, error) {
	if intent.ReleaseGUID <= 0 || intent.ExpectedRevision <= 0 {
		return nil, errInvalidIntent
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.ReleaseGUID); err != nil {
			return err
		}
		return w.fieldInt64(2, intent.ExpectedRevision)
	})
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
