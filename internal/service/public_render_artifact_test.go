package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func testPublicGenerationProjection() *PublicCatalogProjection {
	effective := "2026-09-16T00:00:00Z"
	return &PublicCatalogProjection{
		Content: PublicContentDraft{Revision: 7, Home: "<p>published</p>\n", About: "<p>about</p>\n", Terms: "<p>terms</p>\n", Privacy: "<p>privacy</p>\n", LegalReviewed: true},
		HomeConfig: PublicHomeConfig{
			Announcements:         []PublicHomeConfigAnnouncement{{GUID: "11", Title: "notice", BodyHTML: "<p>safe</p>\n", EffectiveAt: &effective, SortOrder: 1}},
			FAQs:                  []PublicHomeConfigFAQ{},
			FeaturedModelKeys:     []string{"alpha-chat"},
			ContentReleaseVersion: 7,
			PriceReleaseVersion:   9,
		},
		HomeConfigAvailable:   true,
		ContentReleaseVersion: 7,
		PriceReleaseVersion:   9,
		PriceVisibility:       models.PublicPriceVisibilityAuthenticatedOnly,
		ETag:                  `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		Items: []PublicCatalogItem{{
			ModelKey: "alpha-chat", DisplayName: "Alpha", Provider: "acme", Capabilities: []string{"chat"}, ContextWindow: 8192,
			InputPriceUSDPerMillionTokens: "1.00000000", OutputPriceUSDPerMillionTokens: "2.00000000", PricingType: "token", EndpointTypes: []string{"chat"}, UpdatedAt: "2026-09-16T00:00:00Z",
		}},
		GoneKeys: map[string]struct{}{},
	}
}

func testPublicGenerationLease(generation int64) *PublicRenderLease {
	return &PublicRenderLease{
		JobGUID: generation, Generation: generation, Fence: 1,
		PriceVersion: 9, ContentVersion: 7,
		PriceHash: strings.Repeat("b", 64), ContentHash: strings.Repeat("c", 64),
	}
}

func TestBuildPublicGenerationArtifactBindsVersionsAndRedactsAnonymousPrices(t *testing.T) {
	raw, digest, err := BuildPublicGenerationArtifact(testPublicGenerationLease(41), testPublicGenerationProjection())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest=%q", digest)
	}
	if strings.Contains(string(raw), "1.00000000") || strings.Contains(string(raw), "2.00000000") {
		t.Fatalf("authenticated prices leaked into anonymous artifact: %s", raw)
	}
	var artifact PublicGenerationArtifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SchemaVersion != PublicGenerationArtifactSchemaVersion || artifact.Generation != 41 || artifact.ContentReleaseVersion != 7 || artifact.PriceReleaseVersion != 9 || artifact.PriceHash != strings.Repeat("b", 64) || artifact.ContentHash != strings.Repeat("c", 64) {
		t.Fatalf("artifact binding mismatch: %#v", artifact)
	}
	if artifact.Models.Total != 1 || artifact.Models.Items[0].PriceVisibility != "authenticated_only" || artifact.HomeConfig.FeaturedModelKeys[0] != "alpha-chat" {
		t.Fatalf("public subset mismatch: %#v", artifact)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("artifact is not canonical newline-terminated JSON")
	}
}

func TestBuildPublicGenerationArtifactRejectsMixedOrUnsafeGeneration(t *testing.T) {
	for name, mutate := range map[string]func(*PublicRenderLease, *PublicCatalogProjection){
		"mixed content version":    func(_ *PublicRenderLease, p *PublicCatalogProjection) { p.ContentReleaseVersion++ },
		"mixed home price version": func(_ *PublicRenderLease, p *PublicCatalogProjection) { p.HomeConfig.PriceReleaseVersion++ },
		"invalid price hash":       func(l *PublicRenderLease, _ *PublicCatalogProjection) { l.PriceHash = "secret" },
		"missing structured home":  func(_ *PublicRenderLease, p *PublicCatalogProjection) { p.HomeConfigAvailable = false },
	} {
		t.Run(name, func(t *testing.T) {
			lease, projection := testPublicGenerationLease(42), testPublicGenerationProjection()
			mutate(lease, projection)
			if _, _, err := BuildPublicGenerationArtifact(lease, projection); !errors.Is(err, ErrPublicRenderUnavailable) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPublicGenerationPublisherActivatesOnlyValidatedRegularFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "renderer")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	publisher := NewPublicGenerationPublisher(root)
	raw, digest, err := BuildPublicGenerationArtifact(testPublicGenerationLease(51), testPublicGenerationProjection())
	if err != nil {
		t.Fatal(err)
	}
	var canonical PublicGenerationArtifact
	if !decodeCanonicalJSON(raw, &canonical) {
		t.Fatalf("builder returned non-canonical artifact: %s", raw)
	}
	manifestRaw, err := buildPublicGenerationManifest(51, 1, digest)
	if err != nil {
		t.Fatal(err)
	}
	var manifest publicGenerationManifest
	if !decodeCanonicalJSON(manifestRaw, &manifest) {
		t.Fatalf("builder returned non-canonical manifest: %s", manifestRaw)
	}
	activation, err := publisher.StageAndActivate(51, 1, raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil || target != filepath.Join("generations", "generation-00000000000000000051-fence-0000000001") {
		t.Fatalf("current=%q err=%v", target, err)
	}
	artifactPath := filepath.Join(root, target, PublicGenerationArtifactFile)
	info, err := os.Lstat(artifactPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact info=%v err=%v", info, err)
	}
	if err := ValidatePublicGenerationDirectory(filepath.Join(root, target), 51, 1, digest); err != nil {
		t.Fatal(err)
	}
	if err := activation.Commit(); err != nil {
		t.Fatal(err)
	}

	bad := append([]byte(nil), raw...)
	bad[len(bad)-2] ^= 1
	if _, err := publisher.StageAndActivate(52, 1, bad, digest); !errors.Is(err, ErrPublicRenderUnavailable) {
		t.Fatalf("tampered stage err=%v", err)
	}
	still, _ := os.Readlink(filepath.Join(root, "current"))
	if still != target {
		t.Fatalf("failed stage switched current from %q to %q", target, still)
	}
}

func TestPublicGenerationPublisherRejectsSymlinkRootsAndGenerationEntries(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(base, "linked")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	raw, digest, err := BuildPublicGenerationArtifact(testPublicGenerationLease(61), testPublicGenerationProjection())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPublicGenerationPublisher(linkedRoot).StageAndActivate(61, 1, raw, digest); !errors.Is(err, ErrPublicRenderUnavailable) {
		t.Fatalf("symlink root err=%v", err)
	}

	root := filepath.Join(base, "safe")
	if err := os.MkdirAll(filepath.Join(root, "generations"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "generations", "generation-00000000000000000061-fence-0000000001")
	if err := os.Symlink(outside, name); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPublicGenerationPublisher(root).StageAndActivate(61, 1, raw, digest); !errors.Is(err, ErrPublicRenderUnavailable) {
		t.Fatalf("symlink generation err=%v", err)
	}

	lockRoot := filepath.Join(base, "lock-safe")
	if err := os.Mkdir(lockRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lockTarget := filepath.Join(base, "outside-lock")
	if err := os.WriteFile(lockTarget, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lockTarget, filepath.Join(lockRoot, ".switch.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPublicGenerationPublisher(lockRoot).StageAndActivate(61, 1, raw, digest); !errors.Is(err, ErrPublicRenderUnavailable) {
		t.Fatalf("symlink switch lock err=%v", err)
	}
}

func TestPublicGenerationPublisherFencesStaleActivationAndRollback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "renderer")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	publisher := NewPublicGenerationPublisher(root)
	raw, digest, err := BuildPublicGenerationArtifact(testPublicGenerationLease(62), testPublicGenerationProjection())
	if err != nil {
		t.Fatal(err)
	}
	stale, err := publisher.StageAndActivate(62, 1, raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	current, err := publisher.StageAndActivate(62, 2, raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Rollback(); !errors.Is(err, ErrPublicRenderUnavailable) {
		t.Fatalf("stale rollback err=%v", err)
	}
	want := filepath.Join("generations", "generation-00000000000000000062-fence-0000000002")
	got, _ := os.Readlink(filepath.Join(root, "current"))
	if got != want {
		t.Fatalf("stale rollback changed current=%q want=%q", got, want)
	}
	olderRaw, olderDigest, err := BuildPublicGenerationArtifact(testPublicGenerationLease(61), testPublicGenerationProjection())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.StageAndActivate(61, 99, olderRaw, olderDigest); !errors.Is(err, ErrPublicRenderLeaseLost) {
		t.Fatalf("older generation activation err=%v", err)
	}
	if err := current.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicGenerationPublisherSerializesFenceCheckAndCurrentSwitch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "renderer")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	base := NewPublicGenerationPublisher(root)
	raw, digest, err := BuildPublicGenerationArtifact(testPublicGenerationLease(90), testPublicGenerationProjection())
	if err != nil {
		t.Fatal(err)
	}
	activation, err := base.StageAndActivate(90, 1, raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := activation.Commit(); err != nil {
		t.Fatal(err)
	}

	olderEntered := make(chan struct{})
	releaseOlder := make(chan struct{})
	older := NewPublicGenerationPublisher(root)
	older.beforeCurrentReplace = func() {
		close(olderEntered)
		<-releaseOlder
	}
	olderResult := make(chan error, 1)
	go func() {
		raw, digest, buildErr := BuildPublicGenerationArtifact(testPublicGenerationLease(91), testPublicGenerationProjection())
		if buildErr != nil {
			olderResult <- buildErr
			return
		}
		activation, activateErr := older.StageAndActivate(91, 1, raw, digest)
		if activateErr == nil {
			activateErr = activation.Commit()
		}
		olderResult <- activateErr
	}()
	<-olderEntered

	newerResult := make(chan error, 1)
	newerAttempting := make(chan struct{})
	go func() {
		raw, digest, buildErr := BuildPublicGenerationArtifact(testPublicGenerationLease(92), testPublicGenerationProjection())
		if buildErr != nil {
			newerResult <- buildErr
			return
		}
		newer := NewPublicGenerationPublisher(root)
		newer.beforeSwitchLock = func() { close(newerAttempting) }
		activation, activateErr := newer.StageAndActivate(92, 1, raw, digest)
		if activateErr == nil {
			activateErr = activation.Commit()
		}
		newerResult <- activateErr
	}()
	<-newerAttempting
	var earlyNewer error
	newerCompletedWithoutWaiting := false
	select {
	case earlyNewer = <-newerResult:
		newerCompletedWithoutWaiting = true
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseOlder)
	if err := <-olderResult; err != nil {
		t.Fatal(err)
	}
	if !newerCompletedWithoutWaiting {
		earlyNewer = <-newerResult
	}
	if earlyNewer != nil {
		t.Fatal(earlyNewer)
	}

	current, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("generations", "generation-00000000000000000092-fence-0000000001")
	if current != want {
		t.Fatalf("stale publisher overwrote current=%q want=%q", current, want)
	}
}

type fakeArtifactRenderJobs struct {
	lease       *PublicRenderLease
	completeErr error
	failure     string
	completed   bool
}

func (f *fakeArtifactRenderJobs) Lease(context.Context, PublicRenderLeaseInput) (*PublicRenderLease, error) {
	return f.lease, nil
}
func (f *fakeArtifactRenderJobs) Complete(context.Context, PublicRenderTransitionInput) error {
	f.completed = true
	return f.completeErr
}
func (f *fakeArtifactRenderJobs) Fail(_ context.Context, input PublicRenderTransitionInput) error {
	f.failure = input.Failure
	return nil
}

type fakeArtifactCatalog struct {
	projection *PublicCatalogProjection
	err        error
}

func (f fakeArtifactCatalog) Projection(context.Context) (*PublicCatalogProjection, error) {
	return f.projection, f.err
}

func TestPublicRenderWorkerPreservesLastGoodAndSanitizesFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "renderer")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	firstJobs := &fakeArtifactRenderJobs{lease: testPublicGenerationLease(71)}
	worker := NewPublicRenderWorker(firstJobs, fakeArtifactCatalog{projection: testPublicGenerationProjection()}, NewPublicGenerationPublisher(root))
	result, err := worker.RunOnce(context.Background(), PublicRenderLeaseInput{OwnerToken: "owner-token-long-enough", LeaseMillis: 30_000})
	if err != nil || result.Status != "published" || !firstJobs.completed {
		t.Fatalf("result=%#v completed=%v err=%v", result, firstJobs.completed, err)
	}
	lastGood, _ := os.Readlink(filepath.Join(root, "current"))

	secondJobs := &fakeArtifactRenderJobs{lease: testPublicGenerationLease(72)}
	secret := "do-not-leak-secret"
	failed := NewPublicRenderWorker(secondJobs, fakeArtifactCatalog{err: errors.New(secret)}, NewPublicGenerationPublisher(root))
	_, err = failed.RunOnce(context.Background(), PublicRenderLeaseInput{OwnerToken: "owner-token-long-enough", LeaseMillis: 30_000})
	if !errors.Is(err, ErrPublicRenderUnavailable) || strings.Contains(err.Error(), secret) || secondJobs.failure != "validation_failed" {
		t.Fatalf("err=%v failure=%q", err, secondJobs.failure)
	}
	current, _ := os.Readlink(filepath.Join(root, "current"))
	if current != lastGood {
		t.Fatalf("validation failure changed current from %q to %q", lastGood, current)
	}
}

func TestPublicRenderWorkerRollsBackPointerWhenCompletionLosesLease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "renderer")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	baseJobs := &fakeArtifactRenderJobs{lease: testPublicGenerationLease(81)}
	if _, err := NewPublicRenderWorker(baseJobs, fakeArtifactCatalog{projection: testPublicGenerationProjection()}, NewPublicGenerationPublisher(root)).RunOnce(context.Background(), PublicRenderLeaseInput{OwnerToken: "owner-token-long-enough", LeaseMillis: 30_000}); err != nil {
		t.Fatal(err)
	}
	lastGood, _ := os.Readlink(filepath.Join(root, "current"))

	jobs := &fakeArtifactRenderJobs{lease: testPublicGenerationLease(82), completeErr: ErrPublicRenderLeaseLost}
	_, err := NewPublicRenderWorker(jobs, fakeArtifactCatalog{projection: testPublicGenerationProjection()}, NewPublicGenerationPublisher(root)).RunOnce(context.Background(), PublicRenderLeaseInput{OwnerToken: "owner-token-long-enough", LeaseMillis: 30_000})
	if !errors.Is(err, ErrPublicRenderLeaseLost) {
		t.Fatalf("err=%v", err)
	}
	current, _ := os.Readlink(filepath.Join(root, "current"))
	if current != lastGood {
		t.Fatalf("completion failure left candidate active: %q want %q", current, lastGood)
	}
}
