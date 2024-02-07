package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/common/model"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/apps/v1"

	"github.com/grafana/rollout-operator/pkg/config"
)

func cancelDelayedDownscaleIfConfigured(ctx context.Context, logger log.Logger, sts *v1.StatefulSet, httpClient httpClient, replicas int32) {
	delay, prepareURL, err := parseDelayedDownscaleAnnotations(sts.GetAnnotations())
	if delay == 0 || prepareURL == nil {
		return
	}

	if err != nil {
		level.Warn(logger).Log("msg", "failed to cancel possible downscale due to error", "name", sts.GetName(), "err", err)
		return
	}

	endpoints := createPrepareDownscaleEndpoints(sts.Namespace, sts.GetName(), 0, int(replicas), prepareURL)

	callCancelDelayedDownscale(ctx, logger, httpClient, endpoints)
}

func checkScalingDelay(ctx context.Context, logger log.Logger, sts *v1.StatefulSet, httpClient httpClient, currentReplicas, desiredReplicas int32) error {
	if currentReplicas == desiredReplicas {
		// should not happen
		return nil
	}

	delay, prepareURL, err := parseDelayedDownscaleAnnotations(sts.GetAnnotations())
	if delay == 0 || prepareURL == nil || err != nil {
		return err
	}

	if desiredReplicas >= currentReplicas {
		callCancelDelayedDownscale(ctx, logger, httpClient, createPrepareDownscaleEndpoints(sts.Namespace, sts.GetName(), 0, int(currentReplicas), prepareURL))
		// Proceed even if calling cancel of delayed downscale fails. We call cancellation repeatedly, so it will happen during next reconcile.
		return nil
	}

	{
		// Replicas in [0, desired) interval should cancel any delayed downscale, if they have any.
		cancelEndpoints := createPrepareDownscaleEndpoints(sts.Namespace, sts.GetName(), 0, int(desiredReplicas), prepareURL)
		callCancelDelayedDownscale(ctx, logger, httpClient, cancelEndpoints)
	}

	// Replicas in [desired, current) interval are going to be stopped.
	downscaleEndpoints := createPrepareDownscaleEndpoints(sts.Namespace, sts.GetName(), int(desiredReplicas), int(currentReplicas), prepareURL)
	maxPrepareTime, err := callPrepareDownscaleAndReturnMaxPrepareTimestamp(ctx, logger, httpClient, downscaleEndpoints)
	if err != nil {
		return fmt.Errorf("failed prepare pods for delayed downscale: %v", err)
	}

	elapsedSinceMaxTime := time.Since(maxPrepareTime)
	if elapsedSinceMaxTime < delay {
		return fmt.Errorf("configured downscale delay %v has not been reached for all pods. elapsed time: %v", delay, elapsedSinceMaxTime)
	}

	// We can proceed with downscale!
	level.Info(logger).Log("msg", "downscale delay has been reached on all downscaled pods", "name", sts.GetName(), "delay", delay, "elapsed", elapsedSinceMaxTime)
	return nil
}

func parseDelayedDownscaleAnnotations(annotations map[string]string) (time.Duration, *url.URL, error) {
	delayStr := annotations[config.RolloutDelayedDownscaleAnnotationKey]
	urlStr := annotations[config.RolloutDelayedDownscalePrepareUrlAnnotationKey]

	if delayStr == "" || urlStr == "" {
		return 0, nil, nil
	}

	d, err := model.ParseDuration(delayStr)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to parse %s annotation value as duration: %v", config.RolloutDelayedDownscaleAnnotationKey, err)
	}
	if d < 0 {
		return 0, nil, fmt.Errorf("negative value of %s annotation: %v", config.RolloutDelayedDownscaleAnnotationKey, delayStr)
	}

	delay := time.Duration(d)

	u, err := url.Parse(urlStr)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to parse %s annotation value as URL: %v", config.RolloutDelayedDownscalePrepareUrlAnnotationKey, err)
	}

	return delay, u, nil
}

type endpoint struct {
	namespace string
	podName   string
	url       url.URL
	index     int
}

// Create prepare-downscale endpoints for pods with index in [from, to) range. URL is fully reused except for host, which is replaced with pod's FQDN.
func createPrepareDownscaleEndpoints(namespace, serviceName string, from, to int, url *url.URL) []endpoint {
	eps := make([]endpoint, 0, to-from)

	// The DNS entry for a pod of a stateful set is
	// ingester-zone-a-0.$(servicename).$(namespace).svc.cluster.local.
	// The service in this case is ingester-zone-a as well.
	// https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#stable-network-id

	for index := from; index < to; index++ {
		ep := endpoint{
			namespace: namespace,
			podName:   fmt.Sprintf("%v-%v", serviceName, index),
			index:     index,
		}

		ep.url = *url
		ep.url.Host = fmt.Sprintf("%s.%v.%v.svc.cluster.local.", ep.podName, serviceName, ep.namespace)

		eps = append(eps, ep)
	}

	return eps
}

func callPrepareDownscaleAndReturnMaxPrepareTimestamp(ctx context.Context, logger log.Logger, client httpClient, endpoints []endpoint) (time.Time, error) {
	if len(endpoints) == 0 {
		return time.Time{}, fmt.Errorf("no endpoints")
	}

	var (
		maxTimeMu sync.Mutex
		maxTime   time.Time
	)

	type expectedResponse struct {
		Timestamp int64 `json:"timestamp"`
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(32)
	for ix := range endpoints {
		ep := endpoints[ix]
		g.Go(func() error {
			target := ep.url.String()

			epLogger := log.With(logger, "pod", ep.podName, "url", target)

			// POST -- prepare for delayed downscale, if not yet prepared, and return timestamp when prepare was called.
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
			if err != nil {
				level.Error(epLogger).Log("msg", "error creating HTTP POST request to endpoint", "err", err)
				return err
			}

			resp, err := client.Do(req)
			if err != nil {
				level.Error(epLogger).Log("error sending HTTP POST request to endpoint", "err", err)
				return err
			}

			defer resp.Body.Close()

			body, readError := io.ReadAll(resp.Body)
			if readError != nil {
				level.Error(epLogger).Log("msg", "error reading response from HTTP POST request to endpoint", "err", readError)
				return readError
			}

			if resp.StatusCode/100 != 2 {
				level.Error(epLogger).Log("msg", "unexpected status code returned when calling DELETE on endpoint", "status", resp.StatusCode, "response_body", string(body))
				return fmt.Errorf("HTTP DELETE request returned non-2xx status code: %v", resp.StatusCode)
			}

			r := expectedResponse{}
			if err := json.Unmarshal(body, &r); err != nil {
				level.Error(epLogger).Log("msg", "error decoding response from HTTP POST request to endpoint", "err", err)
				return err
			}

			if r.Timestamp == 0 {
				level.Error(epLogger).Log("msg", "invalid response from HTTP POST request to endpoint: no timestamp")
				return fmt.Errorf("no timestamp in response")
			}

			t := time.Unix(r.Timestamp, 0)

			maxTimeMu.Lock()
			if t.After(maxTime) {
				maxTime = t
			}
			maxTimeMu.Unlock()

			level.Debug(epLogger).Log("msg", "HTTP POST request to endpoint succeded", "timestamp", t.UTC().Format(time.RFC3339))
			return nil
		})
	}
	err := g.Wait()
	return maxTime, err
}

func callCancelDelayedDownscale(ctx context.Context, logger log.Logger, client httpClient, endpoints []endpoint) {
	if len(endpoints) == 0 {
		return
	}

	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(32)
	for ix := range endpoints {
		ep := endpoints[ix]
		g.Go(func() error {
			target := ep.url.String()

			epLogger := log.With(logger, "pod", ep.podName, "url", target)

			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
			if err != nil {
				level.Error(epLogger).Log("msg", "error creating HTTP DELETE request to endpoint", "err", err)
				return err
			}

			resp, err := client.Do(req)
			if err != nil {
				level.Error(epLogger).Log("msg", "error sending HTTP DELETE request to endpoint", "err", err)
				return err
			}

			defer resp.Body.Close()

			if resp.StatusCode/100 != 2 {
				err := errors.New("HTTP DELETE request returned non-2xx status code")
				body, readError := io.ReadAll(resp.Body)
				level.Error(epLogger).Log("msg", "unexpected status code returned when calling DELETE on endpoint", "status", resp.StatusCode, "response_body", string(body))
				return errors.Join(err, readError)
			}
			level.Debug(epLogger).Log("msg", "HTTP DELETE request to endpoint succeeded")
			return nil
		})
	}
	// We ignore returned error here, since all errors are already logged, and callers don't need the error.
	_ = g.Wait()
}
