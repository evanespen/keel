package kubernetes

import (
	"testing"
	"time"

	"github.com/keel-hq/keel/internal/k8s"
	"github.com/keel-hq/keel/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func minAgeDeployment(name, minAge string) *appsv1.Deployment {
	annotations := map[string]string{
		types.KeelPolicyLabel:  "minor",
		types.KeelTriggerLabel: "poll",
	}
	if minAge != "" {
		annotations[types.KeelMinAgeAnnotation] = minAge
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: annotations},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "registry.example.com/team/app:1.0.0"}},
		}}},
	}
}

// A poll event is shared by every workload tracking the image, so keel.sh/minAge
// must be enforced per workload when the event is distributed.
func TestMinAgeIsEnforcedPerWorkload(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		trigger string
		created time.Time
		want    []string
	}{
		{
			name:    "young image: only the workload without minAge is updated",
			trigger: "poll",
			created: now.Add(-time.Hour),
			want:    []string{"no-min-age"},
		},
		{
			name:    "image older than the short delay: short and no delay are updated",
			trigger: "poll",
			created: now.Add(-48 * time.Hour),
			want:    []string{"no-min-age", "short"},
		},
		{
			name:    "image older than every delay: all are updated",
			trigger: "poll",
			created: now.Add(-30 * 24 * time.Hour),
			want:    []string{"no-min-age", "short", "long"},
		},
		{
			name:    "unknown build time: workloads with minAge refuse (fail closed)",
			trigger: "poll",
			want:    []string{"no-min-age"},
		},
		{
			name:    "non-poll events are not subject to minAge",
			trigger: "native",
			created: now.Add(-time.Hour),
			want:    []string{"no-min-age", "short", "long"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &k8s.GenericResourceCache{}
			for _, d := range []*appsv1.Deployment{
				minAgeDeployment("no-min-age", ""),
				minAgeDeployment("short", "24h"),
				minAgeDeployment("long", "168h"),
			} {
				r, err := k8s.NewGenericResource(d)
				if err != nil {
					t.Fatal(err)
				}
				cache.Add(r)
			}
			approvalsManager, teardown := approver()
			defer teardown()
			p, err := NewProvider(&fakeImplementer{}, &fakeSender{}, approvalsManager, cache)
			if err != nil {
				t.Fatal(err)
			}

			plans, err := p.createUpdatePlansForEvent(&types.Event{
				Repository:  types.Repository{Name: "registry.example.com/team/app", Tag: "1.1.0", Created: tt.created},
				TriggerName: tt.trigger,
			})
			if err != nil {
				t.Fatal(err)
			}

			got := map[string]bool{}
			for _, plan := range plans {
				got[plan.Resource.Name] = true
			}
			if len(got) != len(tt.want) {
				t.Fatalf("want %v updated, got %v", tt.want, got)
			}
			for _, name := range tt.want {
				if !got[name] {
					t.Errorf("want %q updated, got %v", name, got)
				}
			}
		})
	}
}
