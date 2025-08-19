package util

import (
	"context"
)

type NoTenantResolver struct{}

func (n NoTenantResolver) TenantID(ctx context.Context) (string, error) {
	return "", nil
}
func (n NoTenantResolver) TenantIDs(ctx context.Context) ([]string, error) {
	return nil, nil
}
