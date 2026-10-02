package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const leaderLabel = "rollout-operator.grafana.com/leader"

func setLeaderLabel(ctx context.Context, client kubernetes.Interface, cfg config, leader bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{"%s":"%t"}}}`, leaderLabel, leader))
	_, err := client.CoreV1().Pods(cfg.kubeNamespace).Patch(ctx, cfg.kubePodName, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}
