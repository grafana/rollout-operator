package zpdb

import (
	"fmt"
	"testing"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// A dry run leaves the Pod in place, so allowing one must not reserve disruption capacity, or the probe would
// block the evictions it checked. Clients send dry runs in two forms: the admission request's flag, set from
// the request's dryRun query option, and dryRun in the eviction body, which kubectl drain --dry-run=server
// uses and the API server doesn't copy into the flag.
func TestEvictionController_HandlePodEvictionRequest_DryRun(t *testing.T) {
	for _, form := range []string{"admission flag", "eviction body"} {
		t.Run(form, func(t *testing.T) {
			var objects []runtime.Object
			for _, zone := range []string{statefulSetZoneA, statefulSetZoneB} {
				sts := newEvictionControllerSts(zone)
				*sts.Spec.Replicas = 1
				sts.Status.Replicas = 1
				objects = append(objects, sts, newPod(zone+"-0", sts))
			}
			client := fake.NewClientset(objects...)
			c := NewEvictionController(client, newFakeDynamicClient(), testNamespace, newTestPodsFactory(client), log.NewNopLogger(), NewMetrics(prometheus.NewRegistry()))
			require.NoError(t, c.Start())
			defer c.Stop()
			// Each zone's single Pod is its own partition 0, so the two Pods check each other.
			_, _, err := c.cfgObserver.pdbCache.addOrUpdateRaw(newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, "ingester-zone-[a-z]-([0-9]+)", 1))
			require.NoError(t, err)

			probe := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
			if form == "admission flag" {
				dryRun := true
				probe.Request.DryRun = &dryRun
			} else {
				probe.Request.Object.Raw = fmt.Appendf(nil, `{"apiVersion":"policy/v1","kind":"Eviction","metadata":{"name":%q,"namespace":%q},"deleteOptions":{"dryRun":["All"]}}`, testPodZoneA0, testNamespace)
			}
			require.True(t, c.HandlePodEvictionRequest(t.Context(), probe, NewMaxUnavailableZeroOverrideNone()).Allowed)
			require.True(t, c.HandlePodEvictionRequest(t.Context(), createBasicEvictionAdmissionReview(testPodZoneB0, testNamespace), NewMaxUnavailableZeroOverrideNone()).Allowed,
				"the peer's eviction depends on the probed Pod, which is still in place")
		})
	}
}
