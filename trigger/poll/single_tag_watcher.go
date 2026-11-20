package poll

import (
	"time"

	"github.com/keel-hq/keel/extension/credentialshelper"
	"github.com/keel-hq/keel/provider"
	"github.com/keel-hq/keel/registry"
	"github.com/keel-hq/keel/types"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// WatchTagJob - Watch specific tag job
type WatchTagJob struct {
	providers      provider.Providers
	registryClient registry.Client
	details        *watchDetails
}

// NewWatchTagJob - new watch tag job monitors specific tag by checking digest based on specified
// cron style schedule
func NewWatchTagJob(providers provider.Providers, registryClient registry.Client, details *watchDetails) *WatchTagJob {
	return &WatchTagJob{
		providers:      providers,
		registryClient: registryClient,
		details:        details,
	}
}

// Run - main function to check schedule
func (j *WatchTagJob) Run() {
	reg := j.details.trackedImage.Image.Scheme() + "://" + j.details.trackedImage.Image.Registry()
	registryOpts := registry.Opts{
		Registry: reg,
		Name:     j.details.trackedImage.Image.ShortName(),
		Tag:      j.details.trackedImage.Image.Tag(),
	}

	creds, err := credentialshelper.GetCredentials(j.details.trackedImage)
	if err == nil {
		registryOpts.Username = creds.Username
		registryOpts.Password = creds.Password
	}

	currentDigest, err := j.registryClient.Digest(registryOpts)

	registriesScannedCounter.With(prometheus.Labels{"registry": j.details.trackedImage.Image.Registry(), "image": j.details.trackedImage.Image.Repository()}).Inc()

	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
			"image": j.details.trackedImage.Image.String(),
		}).Error("trigger.poll.WatchTagJob: failed to check digest")
		return
	}

	log.WithFields(log.Fields{
		"current_digest": j.details.digest,
		"new_digest":     currentDigest,
		"registry_url":   reg,
		"image":          j.details.trackedImage.Image.String(),
	}).Debug("trigger.poll.WatchTagJob: checking digest")

	// checking whether image digest has changed
	if j.details.digest != currentDigest {
		// With keel.sh/minAge, hold back a freshly built image. The event is
		// shared by every workload tracking this tag, so the largest delay of
		// those workloads is used here (each workload's own delay is then
		// enforced by the provider). The stored digest is left untouched so
		// the change is detected again, and the update submitted, on the
		// first poll after the delay has elapsed.
		var created time.Time
		if min := j.maxMinAge(); min > 0 {
			var err error
			created, err = j.registryClient.Created(registryOpts)
			if err != nil {
				log.WithFields(log.Fields{
					"error": err,
					"image": j.details.trackedImage.Image.String(),
				}).Warn("trigger.poll.WatchTagJob: skipping update because the image creation time could not be read (keel.sh/minAge is set)")
				return
			}
			if age := time.Since(created); age < min {
				log.WithFields(log.Fields{
					"image":      j.details.trackedImage.Image.String(),
					"new_digest": currentDigest,
					"min_age":    min,
					"eligible":   time.Now().Add(min - age).Format(time.RFC3339),
				}).Info("trigger.poll.WatchTagJob: skipping update because the image is younger than keel.sh/minAge")
				return
			}
		}

		// updating digest
		j.details.digest = currentDigest

		event := types.Event{
			Repository: types.Repository{
				Name:   j.details.trackedImage.Image.Repository(),
				Tag:    j.details.trackedImage.Image.Tag(),
				Digest: currentDigest,
				// zero unless a workload uses keel.sh/minAge
				Created: created,
			},
			TriggerName: types.TriggerTypePoll.String(),
		}
		log.WithFields(log.Fields{
			"image":      j.details.trackedImage.Image.String(),
			"new_digest": currentDigest,
		}).Info("trigger.poll.WatchTagJob: digest change detected, submiting event to providers")

		// j.providers.Submit(event)
		err := j.providers.Submit(event)
		if err != nil {
			log.WithFields(log.Fields{
				"repository": j.details.trackedImage.Image.Repository(),
				"digest":     currentDigest,
				"error":      err,
			}).Error("trigger.poll.WatchRepositoryTagsJob: error while submitting an event")
		}

	}
}

// maxMinAge returns the largest keel.sh/minAge among the poll-tracked
// workloads that share this watcher's image.
func (j *WatchTagJob) maxMinAge() time.Duration {
	max := j.details.trackedImage.MinAge
	trackedImages, err := j.providers.TrackedImages()
	if err != nil {
		return max
	}
	for _, ti := range getRelatedTrackedImages(j.details.trackedImage, trackedImages) {
		if ti.MinAge > max {
			max = ti.MinAge
		}
	}
	return max
}
