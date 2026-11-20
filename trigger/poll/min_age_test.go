package poll

import (
	"errors"
	"testing"
	"time"

	"github.com/keel-hq/keel/approvals"
	"github.com/keel-hq/keel/internal/policy"
	"github.com/keel-hq/keel/provider"
	"github.com/keel-hq/keel/types"
	"github.com/keel-hq/keel/util/image"
)

func submittedTags(fp *fakeProvider) []string {
	var tags []string
	for _, e := range fp.submitted {
		tags = append(tags, e.Repository.Tag)
	}
	return tags
}

func TestWatchTagsMinAge(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	young := now.Add(-1 * time.Hour)

	tests := []struct {
		name       string
		minAge     time.Duration
		created    map[string]time.Time
		createdErr error
		want       []string
	}{
		{
			name:    "no minAge: newest tag is selected and the registry is not queried",
			created: map[string]time.Time{"1.3.0": young},
			want:    []string{"1.3.0"},
		},
		{
			name:    "newest tag too young: falls back to the newest tag old enough",
			minAge:  72 * time.Hour,
			created: map[string]time.Time{"1.3.0": young, "1.2.0": old},
			want:    []string{"1.2.0"},
		},
		{
			name:    "every candidate too young: no update",
			minAge:  72 * time.Hour,
			created: map[string]time.Time{"1.3.0": young, "1.2.0": young},
			want:    nil,
		},
		{
			name:    "newest tag old enough: selected",
			minAge:  72 * time.Hour,
			created: map[string]time.Time{"1.3.0": old},
			want:    []string{"1.3.0"},
		},
		{
			name:       "creation time unreadable: fails closed",
			minAge:     72 * time.Hour,
			createdErr: errors.New("boom"),
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reference, _ := image.Parse("foo/bar:1.1.0")
			ti := &types.TrackedImage{
				Image:   reference,
				Trigger: types.TriggerTypePoll,
				Policy:  policy.NewSemverPolicy(policy.SemverPolicyTypeAll, true),
				MinAge:  tt.minAge,
			}
			fp := &fakeProvider{images: []*types.TrackedImage{ti}}
			store, teardown := newTestingUtils()
			defer teardown()
			providers := provider.New([]provider.Provider{fp}, approvals.New(&approvals.Opts{Store: store}))

			frc := &fakeRegistryClient{
				tagsToReturn:    []string{"1.2.0", "1.3.0"},
				createdToReturn: tt.created,
				createdErr:      tt.createdErr,
				defaultCreated:  old,
			}

			NewWatchRepositoryTagsJob(providers, frc, &watchDetails{trackedImage: ti}).Run()

			got := submittedTags(fp)
			if len(got) != len(tt.want) || (len(got) == 1 && got[0] != tt.want[0]) {
				t.Fatalf("submitted tags: want %v, got %v", tt.want, got)
			}
			if tt.minAge == 0 && len(frc.createdCalls) != 0 {
				t.Errorf("registry Created() must not be called without minAge, got %v", frc.createdCalls)
			}
		})
	}
}

func TestWatchTagJobMinAge(t *testing.T) {
	const newDigest = "sha256:0604af35299dd37ff23937d115d103532948b568a9dd8197d14c256a8ab8b0bb"

	newJob := func(frc *fakeRegistryClient, minAge time.Duration) (*WatchTagJob, *fakeProvider) {
		reference, _ := image.Parse("foo/bar:latest")
		fp := &fakeProvider{}
		store, teardown := newTestingUtils()
		t.Cleanup(teardown)
		providers := provider.New([]provider.Provider{fp}, approvals.New(&approvals.Opts{Store: store}))
		details := &watchDetails{
			trackedImage: &types.TrackedImage{Image: reference, MinAge: minAge},
			digest:       "sha256:old",
		}
		return NewWatchTagJob(providers, frc, details), fp
	}

	t.Run("too young: nothing submitted and digest kept so the change is seen again", func(t *testing.T) {
		frc := &fakeRegistryClient{digestToReturn: newDigest, defaultCreated: time.Now().Add(-time.Hour)}
		job, fp := newJob(frc, 72*time.Hour)

		job.Run()
		job.Run()

		if len(fp.submitted) != 0 {
			t.Fatalf("expected no event, got %v", submittedTags(fp))
		}
		if job.details.digest != "sha256:old" {
			t.Errorf("digest must not be updated while the image is too young, got %s", job.details.digest)
		}
		if len(frc.createdCalls) != 2 {
			t.Errorf("expected the age to be re-checked on each poll, got %d calls", len(frc.createdCalls))
		}
	})

	t.Run("old enough: event submitted and digest updated", func(t *testing.T) {
		frc := &fakeRegistryClient{digestToReturn: newDigest, defaultCreated: time.Now().Add(-100 * time.Hour)}
		job, fp := newJob(frc, 72*time.Hour)

		job.Run()

		if len(fp.submitted) != 1 || fp.submitted[0].Repository.Digest != newDigest {
			t.Fatalf("expected one event for %s, got %+v", newDigest, fp.submitted)
		}
		if job.details.digest != newDigest {
			t.Errorf("digest should be updated, got %s", job.details.digest)
		}
	})

	t.Run("creation time unreadable: fails closed", func(t *testing.T) {
		frc := &fakeRegistryClient{digestToReturn: newDigest, createdErr: errors.New("boom")}
		job, fp := newJob(frc, 72*time.Hour)

		job.Run()

		if len(fp.submitted) != 0 || job.details.digest != "sha256:old" {
			t.Errorf("expected no update, got events=%v digest=%s", submittedTags(fp), job.details.digest)
		}
	})

	t.Run("no minAge: behaves as before without querying the creation time", func(t *testing.T) {
		frc := &fakeRegistryClient{digestToReturn: newDigest, defaultCreated: time.Now()}
		job, fp := newJob(frc, 0)

		job.Run()

		if len(fp.submitted) != 1 {
			t.Fatalf("expected one event, got %d", len(fp.submitted))
		}
		if len(frc.createdCalls) != 0 {
			t.Errorf("Created() must not be called without minAge")
		}
	})
}

// Events are shared by every workload tracking the image, so they must carry the
// build time whenever any related workload uses minAge (the provider enforces
// each workload's own delay), and stay untouched otherwise.
func TestEventsCarryCreatedOnlyWhenMinAgeIsUsed(t *testing.T) {
	built := time.Now().Add(-5 * 24 * time.Hour)

	for _, tt := range []struct {
		name    string
		minAges []time.Duration
		want    bool
	}{
		{"no workload uses minAge", []time.Duration{0, 0}, false},
		{"one of two related workloads uses minAge", []time.Duration{0, time.Hour}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var images []*types.TrackedImage
			for _, m := range tt.minAges {
				reference, _ := image.Parse("foo/bar:1.1.0")
				images = append(images, &types.TrackedImage{
					Image:   reference,
					Trigger: types.TriggerTypePoll,
					Policy:  policy.NewSemverPolicy(policy.SemverPolicyTypeAll, true),
					MinAge:  m,
				})
			}
			fp := &fakeProvider{images: images}
			store, teardown := newTestingUtils()
			defer teardown()
			providers := provider.New([]provider.Provider{fp}, approvals.New(&approvals.Opts{Store: store}))
			frc := &fakeRegistryClient{tagsToReturn: []string{"1.2.0"}, defaultCreated: built}

			NewWatchRepositoryTagsJob(providers, frc, &watchDetails{trackedImage: images[0]}).Run()

			if len(fp.submitted) != 1 {
				t.Fatalf("expected one event, got %v", submittedTags(fp))
			}
			gotCreated := !fp.submitted[0].Repository.Created.IsZero()
			if gotCreated != tt.want {
				t.Errorf("event carries created time: want %v, got %v", tt.want, gotCreated)
			}
			if tt.want && !fp.submitted[0].Repository.Created.Equal(built) {
				t.Errorf("unexpected created time %s", fp.submitted[0].Repository.Created)
			}
		})
	}
}
