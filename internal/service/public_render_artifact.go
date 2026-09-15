package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

const (
	PublicGenerationArtifactSchemaVersion = "public-generation/v1"
	PublicGenerationArtifactFile          = "generation.json"
	publicGenerationManifestFile          = "manifest.json"
	publicGenerationArtifactLimit         = 4 << 20
)

var publicGenerationHex = regexp.MustCompile(`^[0-9a-f]{64}$`)
var publicGenerationTargetPattern = regexp.MustCompile(`^generations/generation-([0-9]{20})-fence-([0-9]{10})$`)

type PublicGenerationDocuments struct {
	Home          string `json:"home"`
	About         string `json:"about"`
	Terms         string `json:"terms"`
	Privacy       string `json:"privacy"`
	LegalReviewed bool   `json:"legal_reviewed"`
}

// PublicGenerationArtifact is an immutable, anonymous public projection. The
// Vue shell remains fixed; this file contains only version-bound dynamic data.
type PublicGenerationArtifact struct {
	SchemaVersion         string                    `json:"schema_version"`
	Generation            int64                     `json:"generation"`
	ContentReleaseVersion int64                     `json:"content_release_version"`
	PriceReleaseVersion   int64                     `json:"price_release_version"`
	ContentHash           string                    `json:"content_hash"`
	PriceHash             string                    `json:"price_hash"`
	ETag                  string                    `json:"etag"`
	PriceVisibility       string                    `json:"price_visibility"`
	Documents             PublicGenerationDocuments `json:"documents"`
	HomeConfig            PublicHomeConfig          `json:"home_config"`
	Models                PublicModelListRead       `json:"models"`
}

type publicGenerationManifest struct {
	SchemaVersion string                          `json:"schema_version"`
	Generation    int64                           `json:"generation"`
	Fence         int                             `json:"fence"`
	Files         []publicGenerationManifestEntry `json:"files"`
}

type publicGenerationManifestEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func BuildPublicGenerationArtifact(lease *PublicRenderLease, projection *PublicCatalogProjection) ([]byte, string, error) {
	if lease == nil || projection == nil || lease.Generation <= 0 || lease.JobGUID != lease.Generation ||
		lease.ContentVersion != projection.ContentReleaseVersion || lease.PriceVersion != projection.PriceReleaseVersion ||
		!projection.HomeConfigAvailable || projection.HomeConfig.ContentReleaseVersion != lease.ContentVersion ||
		projection.HomeConfig.PriceReleaseVersion != lease.PriceVersion ||
		!publicGenerationHex.MatchString(lease.ContentHash) || !publicGenerationHex.MatchString(lease.PriceHash) ||
		!publicGenerationETag(projection.ETag) {
		return nil, "", ErrPublicRenderUnavailable
	}
	home, err := NormalizePublicHomeConfig(projection.HomeConfig)
	if err != nil || home.ContentReleaseVersion != lease.ContentVersion || home.PriceReleaseVersion != lease.PriceVersion {
		return nil, "", ErrPublicRenderUnavailable
	}
	pageSize := len(projection.Items)
	if pageSize == 0 {
		pageSize = 1
	}
	models := projection.List(PublicCatalogListRequest{Page: 1, PageSize: pageSize, Sort: "model_key", Order: "asc"}, false)
	if models.Total != len(projection.Items) || models.ReleaseVersion != lease.PriceVersion {
		return nil, "", ErrPublicRenderUnavailable
	}
	artifact := PublicGenerationArtifact{
		SchemaVersion: PublicGenerationArtifactSchemaVersion, Generation: lease.Generation,
		ContentReleaseVersion: lease.ContentVersion, PriceReleaseVersion: lease.PriceVersion,
		ContentHash: lease.ContentHash, PriceHash: lease.PriceHash, ETag: projection.ETag,
		PriceVisibility: projection.PriceVisibility.String(),
		Documents:       PublicGenerationDocuments{Home: projection.Content.Home, About: projection.Content.About, Terms: projection.Content.Terms, Privacy: projection.Content.Privacy, LegalReviewed: projection.Content.LegalReviewed},
		HomeConfig:      home, Models: models,
	}
	if artifact.PriceVisibility != "visible" && artifact.PriceVisibility != "authenticated_only" {
		return nil, "", ErrPublicRenderUnavailable
	}
	raw, err := json.Marshal(artifact)
	if err != nil || len(raw)+1 > publicGenerationArtifactLimit {
		return nil, "", ErrPublicRenderUnavailable
	}
	raw = append(raw, '\n')
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func publicGenerationETag(value string) bool {
	return len(value) == 66 && value[0] == '"' && value[len(value)-1] == '"' && publicGenerationHex.MatchString(value[1:len(value)-1])
}

type PublicGenerationPublisher struct {
	root                 string
	beforeSwitchLock     func()
	beforeCurrentReplace func()
}

func NewPublicGenerationPublisher(root string) *PublicGenerationPublisher {
	return &PublicGenerationPublisher{root: root}
}

type PublicGenerationActivation struct {
	publisher       *PublicGenerationPublisher
	previousTarget  string
	previousPresent bool
	candidateTarget string
	finished        bool
}

func (p *PublicGenerationPublisher) StageAndActivate(generation int64, fence int, raw []byte, digest string) (*PublicGenerationActivation, error) {
	if p == nil || generation <= 0 || fence <= 0 || len(raw) == 0 || len(raw) > publicGenerationArtifactLimit || !publicGenerationHex.MatchString(digest) {
		return nil, ErrPublicRenderInvalid
	}
	actual := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), digest) {
		return nil, ErrPublicRenderUnavailable
	}
	root, err := validatePublicGenerationRoot(p.root)
	if err != nil {
		return nil, err
	}
	generations := filepath.Join(root, "generations")
	if err := ensurePrivateDirectory(generations); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return nil, ErrPublicRenderUnavailable
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return nil, ErrPublicRenderUnavailable
	}
	if err := writeSyncedRegular(filepath.Join(stage, PublicGenerationArtifactFile), raw); err != nil {
		return nil, err
	}
	manifestRaw, err := buildPublicGenerationManifest(generation, fence, digest)
	if err != nil {
		return nil, err
	}
	if err := writeSyncedRegular(filepath.Join(stage, publicGenerationManifestFile), manifestRaw); err != nil {
		return nil, err
	}
	if err := syncDirectory(stage); err != nil || ValidatePublicGenerationDirectory(stage, generation, fence, digest) != nil {
		return nil, ErrPublicRenderUnavailable
	}
	name := fmt.Sprintf("generation-%020d-fence-%010d", generation, fence)
	final := filepath.Join(generations, name)
	if _, err := os.Lstat(final); err == nil {
		if ValidatePublicGenerationDirectory(final, generation, fence, digest) != nil {
			return nil, ErrPublicRenderUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrPublicRenderUnavailable
	} else {
		if err := os.Rename(stage, final); err != nil {
			return nil, ErrPublicRenderUnavailable
		}
		removeStage = false
		if err := syncDirectory(generations); err != nil {
			return nil, ErrPublicRenderUnavailable
		}
	}
	var previous string
	var present bool
	candidate := filepath.Join("generations", name)
	if p.beforeSwitchLock != nil {
		p.beforeSwitchLock()
	}
	if err := withPublicGenerationSwitchLock(root, func() error {
		var err error
		previous, present, err = readCurrentPublicGeneration(root)
		if err != nil {
			return err
		}
		if present {
			currentGeneration, currentFence, ok := parsePublicGenerationTarget(previous)
			if !ok {
				return ErrPublicRenderUnavailable
			}
			if generation < currentGeneration || generation == currentGeneration && fence <= currentFence {
				return ErrPublicRenderLeaseLost
			}
		}
		if p.beforeCurrentReplace != nil {
			p.beforeCurrentReplace()
		}
		if err := replacePublicGenerationLink(root, candidate); err != nil {
			return err
		}
		if err := syncDirectory(root); err != nil {
			_ = restorePublicGenerationLink(root, candidate, previous, present)
			return ErrPublicRenderUnavailable
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &PublicGenerationActivation{publisher: p, previousTarget: previous, previousPresent: present, candidateTarget: candidate}, nil
}

func (a *PublicGenerationActivation) Commit() error {
	if a == nil || a.publisher == nil || a.finished {
		return ErrPublicRenderInvalid
	}
	a.finished = true
	return nil
}

func (a *PublicGenerationActivation) Rollback() error {
	if a == nil || a.publisher == nil || a.finished {
		return ErrPublicRenderInvalid
	}
	root, err := validatePublicGenerationRoot(a.publisher.root)
	if err != nil {
		return err
	}
	if err := withPublicGenerationSwitchLock(root, func() error {
		current, present, err := readCurrentPublicGeneration(root)
		if err != nil || !present || current != a.candidateTarget {
			return ErrPublicRenderUnavailable
		}
		return restorePublicGenerationLink(root, current, a.previousTarget, a.previousPresent)
	}); err != nil {
		return err
	}
	a.finished = true
	return nil
}

func withPublicGenerationSwitchLock(root string, operation func() error) error {
	if operation == nil {
		return ErrPublicRenderInvalid
	}
	path := filepath.Join(root, ".switch.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return ErrPublicRenderUnavailable
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return ErrPublicRenderUnavailable
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return ErrPublicRenderUnavailable
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return ErrPublicRenderUnavailable
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return ErrPublicRenderUnavailable
	}
	result := operation()
	if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil && result == nil {
		return ErrPublicRenderUnavailable
	}
	return result
}

func buildPublicGenerationManifest(generation int64, fence int, digest string) ([]byte, error) {
	manifest := publicGenerationManifest{SchemaVersion: PublicGenerationArtifactSchemaVersion, Generation: generation, Fence: fence, Files: []publicGenerationManifestEntry{{Path: PublicGenerationArtifactFile, SHA256: digest}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, ErrPublicRenderUnavailable
	}
	return append(raw, '\n'), nil
}

func ValidatePublicGenerationDirectory(directory string, generation int64, fence int, digest string) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return ErrPublicRenderUnavailable
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		return ErrPublicRenderUnavailable
	}
	for _, expected := range []string{PublicGenerationArtifactFile, publicGenerationManifestFile} {
		path := filepath.Join(directory, expected)
		entry, err := os.Lstat(path)
		if err != nil || !entry.Mode().IsRegular() || entry.Mode().Perm()&0o177 != 0 || entry.Size() > publicGenerationArtifactLimit {
			return ErrPublicRenderUnavailable
		}
	}
	artifactRaw, err := os.ReadFile(filepath.Join(directory, PublicGenerationArtifactFile))
	if err != nil || len(artifactRaw) == 0 || len(artifactRaw) > publicGenerationArtifactLimit {
		return ErrPublicRenderUnavailable
	}
	sum := sha256.Sum256(artifactRaw)
	if hex.EncodeToString(sum[:]) != digest {
		return ErrPublicRenderUnavailable
	}
	var artifact PublicGenerationArtifact
	if !decodeCanonicalJSON(artifactRaw, &artifact) || artifact.SchemaVersion != PublicGenerationArtifactSchemaVersion || artifact.Generation != generation {
		return ErrPublicRenderUnavailable
	}
	manifestRaw, err := os.ReadFile(filepath.Join(directory, publicGenerationManifestFile))
	if err != nil {
		return ErrPublicRenderUnavailable
	}
	var manifest publicGenerationManifest
	if !decodeCanonicalJSON(manifestRaw, &manifest) || manifest.SchemaVersion != PublicGenerationArtifactSchemaVersion || manifest.Generation != generation || manifest.Fence != fence || len(manifest.Files) != 1 || manifest.Files[0].Path != PublicGenerationArtifactFile || manifest.Files[0].SHA256 != digest {
		return ErrPublicRenderUnavailable
	}
	return nil
}

func decodeCanonicalJSON(raw []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return false
	}
	canonical, err := json.Marshal(target)
	return err == nil && bytes.Equal(raw, append(canonical, '\n'))
}

func validatePublicGenerationRoot(raw string) (string, error) {
	if raw == "" || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		return "", ErrPublicRenderInvalid
	}
	info, err := os.Lstat(raw)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", ErrPublicRenderUnavailable
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", ErrPublicRenderUnavailable
	}
	return resolved, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrPublicRenderUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return ErrPublicRenderUnavailable
	}
	return nil
}

func writeSyncedRegular(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrPublicRenderUnavailable
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return ErrPublicRenderUnavailable
	}
	if _, err := file.Write(raw); err != nil || file.Sync() != nil || file.Close() != nil {
		return ErrPublicRenderUnavailable
	}
	ok = true
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return ErrPublicRenderUnavailable
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return ErrPublicRenderUnavailable
	}
	return nil
}

func readCurrentPublicGeneration(root string) (string, bool, error) {
	current := filepath.Join(root, "current")
	info, err := os.Lstat(current)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false, ErrPublicRenderUnavailable
	}
	target, err := os.Readlink(current)
	if err != nil || validatePublicGenerationTarget(root, target) != nil {
		return "", false, ErrPublicRenderUnavailable
	}
	return target, true, nil
}

func validatePublicGenerationTarget(root, target string) error {
	if _, _, ok := parsePublicGenerationTarget(target); filepath.IsAbs(target) || filepath.Clean(target) != target || !ok {
		return ErrPublicRenderUnavailable
	}
	resolved := filepath.Join(root, target)
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrPublicRenderUnavailable
	}
	return nil
}

func parsePublicGenerationTarget(target string) (int64, int, bool) {
	matches := publicGenerationTargetPattern.FindStringSubmatch(target)
	if len(matches) != 3 {
		return 0, 0, false
	}
	generation, generationErr := strconv.ParseInt(matches[1], 10, 64)
	fence64, fenceErr := strconv.ParseInt(matches[2], 10, 32)
	if generationErr != nil || fenceErr != nil || generation <= 0 || fence64 <= 0 {
		return 0, 0, false
	}
	return generation, int(fence64), true
}

func replacePublicGenerationLink(root, target string) error {
	if err := validatePublicGenerationTarget(root, target); err != nil {
		return err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return ErrPublicRenderUnavailable
	}
	temporary := filepath.Join(root, ".current-"+hex.EncodeToString(nonce))
	if err := os.Symlink(target, temporary); err != nil {
		return ErrPublicRenderUnavailable
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, filepath.Join(root, "current")); err != nil {
		return ErrPublicRenderUnavailable
	}
	return nil
}

func restorePublicGenerationLink(root, candidate, previous string, previousPresent bool) error {
	current, present, err := readCurrentPublicGeneration(root)
	if err != nil || !present || current != candidate {
		return ErrPublicRenderUnavailable
	}
	if previousPresent {
		if err := replacePublicGenerationLink(root, previous); err != nil {
			return err
		}
	} else if err := os.Remove(filepath.Join(root, "current")); err != nil {
		return ErrPublicRenderUnavailable
	}
	return syncDirectory(root)
}

type PublicRenderWorkerJobs interface {
	Lease(context.Context, PublicRenderLeaseInput) (*PublicRenderLease, error)
	Complete(context.Context, PublicRenderTransitionInput) error
	Fail(context.Context, PublicRenderTransitionInput) error
}

type PublicRenderWorkerCatalog interface {
	Projection(context.Context) (*PublicCatalogProjection, error)
}

type PublicRenderWorker struct {
	jobs      PublicRenderWorkerJobs
	catalog   PublicRenderWorkerCatalog
	publisher *PublicGenerationPublisher
}

type PublicRenderWorkerResult struct {
	Status     string `json:"status"`
	Generation int64  `json:"generation,omitempty"`
	Digest     string `json:"sha256,omitempty"`
}

func NewPublicRenderWorker(jobs PublicRenderWorkerJobs, catalog PublicRenderWorkerCatalog, publisher *PublicGenerationPublisher) *PublicRenderWorker {
	return &PublicRenderWorker{jobs: jobs, catalog: catalog, publisher: publisher}
}

func (w *PublicRenderWorker) RunOnce(ctx context.Context, input PublicRenderLeaseInput) (PublicRenderWorkerResult, error) {
	if w == nil || w.jobs == nil || w.catalog == nil || w.publisher == nil || !validPublicRenderLeaseInput(input.OwnerToken, input.LeaseMillis) {
		return PublicRenderWorkerResult{}, ErrPublicRenderInvalid
	}
	lease, err := w.jobs.Lease(ctx, input)
	if err != nil {
		return PublicRenderWorkerResult{}, err
	}
	if lease == nil {
		return PublicRenderWorkerResult{Status: "idle"}, nil
	}
	transition := PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: input.OwnerToken, Fence: lease.Fence}
	projection, err := w.catalog.Projection(ctx)
	if err != nil {
		return PublicRenderWorkerResult{}, w.recordFailure(ctx, transition, "validation_failed")
	}
	raw, digest, err := BuildPublicGenerationArtifact(lease, projection)
	if err != nil {
		return PublicRenderWorkerResult{}, w.recordFailure(ctx, transition, "validation_failed")
	}
	activation, err := w.publisher.StageAndActivate(lease.Generation, lease.Fence, raw, digest)
	if err != nil {
		return PublicRenderWorkerResult{}, w.recordFailure(ctx, transition, "render_failed")
	}
	if err := w.jobs.Complete(ctx, transition); err != nil {
		if rollbackErr := activation.Rollback(); rollbackErr != nil {
			return PublicRenderWorkerResult{}, ErrPublicRenderUnavailable
		}
		return PublicRenderWorkerResult{}, err
	}
	if err := activation.Commit(); err != nil {
		return PublicRenderWorkerResult{}, ErrPublicRenderUnavailable
	}
	return PublicRenderWorkerResult{Status: "published", Generation: lease.Generation, Digest: digest}, nil
}

func (w *PublicRenderWorker) recordFailure(ctx context.Context, transition PublicRenderTransitionInput, code string) error {
	transition.Failure = code
	if err := w.jobs.Fail(ctx, transition); err != nil {
		return err
	}
	return ErrPublicRenderUnavailable
}
