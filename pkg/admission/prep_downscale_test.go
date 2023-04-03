package admission

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
)

func newDebugLogger() log.Logger {
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	var options []level.Option
	options = append(options, level.AllowDebug())
	logger = level.NewFilter(logger, options...)
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)
	return logger
}

func TestPrepDownscale(t *testing.T) {
	ctx := context.Background()
	logger := newDebugLogger()

	ar := v1.AdmissionReview{
		Request: &v1.AdmissionRequest{
			Kind: metav1.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "Statefulset",
			},
			Resource: metav1.GroupVersionResource{
				Group:    "apps",
				Version:  "v1",
				Resource: "ingester-statefulset-1",
			},
			Namespace: "test",
			Object: runtime.RawExtension{
				Raw: statefulSetWithReplicas(2),
			},
			OldObject: runtime.RawExtension{
				Raw: statefulSetWithReplicas(3),
			},
		},
	}
	var api *kubernetes.Clientset

	admissionResponse := PrepDownscale(ctx, logger, ar, api)
	require.True(t, admissionResponse.Allowed)
}

func statefulSetWithReplicas(replicas int) []byte {
	return []byte(`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: web
  labels:
    grafana.com/prep-downscale: "true"
spec:
  selector:
    matchLabels:
      app: nginx # has to match .spec.template.metadata.labels
  serviceName: "nginx"
  replicas: ` + fmt.Sprintf("%d", replicas) + ` 
  minReadySeconds: 10 # by default is 0
  template:
    metadata:
      labels:
        app: nginx # has to match .spec.selector.matchLabels
    spec:
      terminationGracePeriodSeconds: 10
      containers:
      - name: nginx
        image: registry.k8s.io/nginx-slim:0.8
        ports:
        - containerPort: 80
          name: web
          volumeMounts:
        - name: www
          mountPath: /usr/share/nginx/html
  volumeClaimTemplates:
  - metadata:
    name: www
    spec:
      accessModes: [ "ReadWriteOnce" ]
      storageClassName: "my-storage-class"
      resources:
        requests:
          storage: 1Gi`)
}
